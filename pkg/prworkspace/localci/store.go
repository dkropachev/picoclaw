package localci

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maximumEvidenceBytes = 8 << 20

type EvidenceStore interface {
	PutPlan(ctx context.Context, plan Plan) error
	GetPlan(ctx context.Context, digest string) (Plan, bool, error)
	PutResolvedPlan(ctx context.Context, key string, plan ResolvedPlan) error
	GetResolvedPlan(ctx context.Context, key string) (ResolvedPlan, bool, error)
	PutExecution(ctx context.Context, execution Execution) error
	GetExecution(ctx context.Context, digest string) (Execution, bool, error)
	PutAttestation(ctx context.Context, attestation Attestation) error
	GetAttestation(ctx context.Context, id string) (Attestation, bool, error)
	LookupPassing(ctx context.Context, resultKey string) (Execution, bool, error)
	PromotePassing(ctx context.Context, resultKey, executionDigest string) error
}

type FileEvidenceStore struct {
	rootPath string
	root     *os.Root
	cacheDB  *sql.DB
	mu       sync.Mutex
	now      func() time.Time
	cacheTTL time.Duration
}

type cacheIndexRecord struct {
	Version         int       `json:"version"`
	ResultKey       string    `json:"result_key"`
	ExecutionDigest string    `json:"execution_digest"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	Digest          string    `json:"digest"`
}

func OpenFileEvidenceStore(root string) (*FileEvidenceStore, error) {
	if strings.TrimSpace(root) != root || root == "" {
		return nil, fmt.Errorf("%w: evidence root is required", ErrInvalid)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve local CI evidence root: %w", err)
	}
	info, err := os.Lstat(absolute)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.MkdirAll(absolute, 0o700); err != nil {
			return nil, fmt.Errorf("create local CI evidence root: %w", err)
		}
		info, err = os.Lstat(absolute)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: stat evidence root: %v", ErrInvalid, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !privateEvidenceMode(info) {
		return nil, fmt.Errorf(
			"%w: evidence root must be an owner-private real directory (mode %s)",
			ErrInvalid,
			info.Mode(),
		)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || resolved != absolute {
		return nil, fmt.Errorf("%w: evidence root must be canonical", ErrInvalid)
	}
	rootHandle, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, fmt.Errorf("open local CI evidence root: %w", err)
	}
	store := &FileEvidenceStore{
		rootPath: absolute,
		root:     rootHandle,
		now:      func() time.Time { return time.Now().UTC() },
		cacheTTL: 24 * time.Hour,
	}
	for _, directory := range []string{"plans", "executions", "attestations", "cache", "discovery"} {
		if err = rootHandle.MkdirAll(directory, 0o700); err != nil {
			_ = rootHandle.Close()
			return nil, fmt.Errorf("create local CI evidence directory: %w", err)
		}
		if err = store.validateDirectory(directory); err != nil {
			_ = rootHandle.Close()
			return nil, err
		}
	}
	store.cacheDB, err = store.openCacheDatabase(context.Background())
	if err != nil {
		_ = rootHandle.Close()
		store.root = nil
		return nil, fmt.Errorf("open local CI passing cache: %w", err)
	}
	return store, nil
}

func (store *FileEvidenceStore) Close() error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	var databaseErr, rootErr error
	if store.cacheDB != nil {
		databaseErr = store.cacheDB.Close()
		store.cacheDB = nil
	}
	if store.root != nil {
		rootErr = store.root.Close()
	}
	store.root = nil
	return errors.Join(databaseErr, rootErr)
}

func (store *FileEvidenceStore) PutPlan(ctx context.Context, plan Plan) error {
	normalized, err := normalizePlan(plan)
	if err != nil {
		return err
	}
	return store.putImmutable(ctx, "plans", normalized.Digest, normalized)
}

func (store *FileEvidenceStore) GetPlan(ctx context.Context, digest string) (Plan, bool, error) {
	var plan Plan
	found, err := store.readObject(ctx, "plans", digest, &plan)
	if err != nil || !found {
		return Plan{}, found, err
	}
	normalized, err := normalizePlan(plan)
	if err != nil || normalized.Digest != digest || !bytes.Equal(mustJSON(normalized), mustJSON(plan)) {
		return Plan{}, false, ErrEvidenceCorrupt
	}
	return normalized, true, nil
}

func (store *FileEvidenceStore) PutExecution(ctx context.Context, execution Execution) error {
	normalized, err := finalizeExecution(execution)
	if err != nil {
		return err
	}
	if err = store.validateExecutionPlan(ctx, normalized); err != nil {
		return err
	}
	return store.putImmutable(ctx, "executions", normalized.Digest, normalized)
}

func (store *FileEvidenceStore) GetExecution(
	ctx context.Context,
	digest string,
) (Execution, bool, error) {
	var execution Execution
	found, err := store.readObject(ctx, "executions", digest, &execution)
	if err != nil || !found {
		return Execution{}, found, err
	}
	normalized, err := finalizeExecution(execution)
	if err != nil || normalized.Digest != digest || !bytes.Equal(mustJSON(normalized), mustJSON(execution)) {
		return Execution{}, false, ErrEvidenceCorrupt
	}
	if err = store.validateExecutionPlan(ctx, normalized); err != nil {
		return Execution{}, false, ErrEvidenceCorrupt
	}
	return normalized, true, nil
}

func (store *FileEvidenceStore) validateExecutionPlan(ctx context.Context, execution Execution) error {
	plan, found, err := store.GetPlan(ctx, execution.Evidence.PlanDigest)
	if err != nil {
		return err
	}
	if !found || plan.DependencyDigest != execution.Evidence.DependencyDigest {
		return fmt.Errorf("%w: execution plan evidence is unavailable", ErrInvalid)
	}
	if len(execution.Steps) > len(plan.Steps) {
		return fmt.Errorf("%w: execution contains extra step results", ErrInvalid)
	}
	aggregate := StatusPassed
	for index, result := range execution.Steps {
		if result.StepID != plan.Steps[index].ID {
			return fmt.Errorf("%w: execution step order differs from plan", ErrInvalid)
		}
		aggregate = worseStatus(aggregate, result.Status)
	}
	if execution.Status == StatusPassed {
		if !plan.Complete || len(execution.Steps) != len(plan.Steps) || aggregate != StatusPassed {
			return fmt.Errorf("%w: passing execution did not complete its exact plan", ErrInvalid)
		}
		return nil
	}
	if len(execution.Steps) > 0 && execution.Status != aggregate {
		return fmt.Errorf("%w: execution status differs from step results", ErrInvalid)
	}
	if len(execution.Steps) == 0 && execution.Status != StatusIncomplete &&
		execution.Status != StatusPlanChanged && execution.Status != StatusEnvironmentUnavailable {
		return fmt.Errorf("%w: execution has no evidence for its status", ErrInvalid)
	}
	return nil
}

func (store *FileEvidenceStore) PutAttestation(ctx context.Context, attestation Attestation) error {
	normalized, err := finalizeAttestation(attestation)
	if err != nil {
		return err
	}
	if err = store.validateAttestationExecution(ctx, normalized); err != nil {
		return err
	}
	key := digestParts("picoclaw-local-ci-attestation-path-v1", []byte(normalized.ID))
	return store.putImmutable(ctx, "attestations", key, normalized)
}

func (store *FileEvidenceStore) GetAttestation(
	ctx context.Context,
	id string,
) (Attestation, bool, error) {
	if !localCIIDPattern.MatchString(id) {
		return Attestation{}, false, fmt.Errorf("%w: invalid attestation ID", ErrInvalid)
	}
	key := digestParts("picoclaw-local-ci-attestation-path-v1", []byte(id))
	var attestation Attestation
	found, err := store.readObject(ctx, "attestations", key, &attestation)
	if err != nil || !found {
		return Attestation{}, found, err
	}
	normalized, err := finalizeAttestation(attestation)
	if err != nil || normalized.ID != id || normalized.Digest != attestation.Digest ||
		!bytes.Equal(mustJSON(normalized), mustJSON(attestation)) {
		return Attestation{}, false, ErrEvidenceCorrupt
	}
	if err = store.validateAttestationExecution(ctx, normalized); err != nil {
		return Attestation{}, false, ErrEvidenceCorrupt
	}
	return normalized, true, nil
}

func (store *FileEvidenceStore) validateAttestationExecution(
	ctx context.Context,
	attestation Attestation,
) error {
	execution, found, err := store.GetExecution(ctx, attestation.ExecutionDigest)
	if err != nil {
		return err
	}
	if !found || execution.ResultKey != attestation.ResultKey ||
		execution.Status != attestation.Status || attestation.CreatedAt.Before(execution.CompletedAt) {
		return fmt.Errorf("%w: attestation execution is unavailable", ErrInvalid)
	}
	return nil
}

func (store *FileEvidenceStore) LookupPassing(
	ctx context.Context,
	resultKey string,
) (Execution, bool, error) {
	return store.lookupPassingCache(ctx, resultKey)
}

func (store *FileEvidenceStore) PromotePassing(
	ctx context.Context,
	resultKey, executionDigest string,
) error {
	return store.promotePassingCache(ctx, resultKey, executionDigest)
}

func (store *FileEvidenceStore) putImmutable(
	ctx context.Context,
	kind, key string,
	value any,
) error {
	if store == nil || store.root == nil || !validDigest(key) {
		return fmt.Errorf("%w: invalid evidence store operation", ErrInvalid)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := encodeEvidence(value)
	if err != nil {
		return err
	}
	path := store.objectRelative(kind, key)
	store.mu.Lock()
	defer store.mu.Unlock()
	if lstat, statErr := store.root.Lstat(path); statErr == nil {
		if !privateEvidenceFile(lstat) {
			return ErrEvidenceConflict
		}
		existing, readErr := store.readEncoded(path)
		if readErr != nil {
			return readErr
		}
		if bytes.Equal(existing, encoded) {
			return nil
		}
		return ErrEvidenceConflict
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if err = store.root.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err = store.validateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	return store.writeImmutable(path, encoded)
}

func (store *FileEvidenceStore) readObject(
	ctx context.Context,
	kind, key string,
	target any,
) (bool, error) {
	if store == nil || store.root == nil || !validDigest(key) {
		return false, fmt.Errorf("%w: invalid evidence read", ErrInvalid)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	relative := filepath.Join(kind, key[:2], key+".json")
	directory := filepath.Dir(relative)
	if err := store.validateDirectory(filepath.Dir(directory)); err != nil {
		return false, err
	}
	directoryInfo, err := store.root.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 ||
		!privateEvidenceMode(directoryInfo) {
		return false, ErrEvidenceCorrupt
	}
	raw, err := store.readEncoded(relative)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, ErrEvidenceCorrupt
	}
	if err = decodeStrictEvidence(raw, target); err != nil {
		return false, ErrEvidenceCorrupt
	}
	return true, nil
}

func (store *FileEvidenceStore) readEncoded(relative string) ([]byte, error) {
	lstat, err := store.root.Lstat(relative)
	if err != nil {
		return nil, err
	}
	if !privateEvidenceFile(lstat) {
		return nil, ErrEvidenceCorrupt
	}
	file, err := openEvidenceRegularFile(store.root, relative)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !privateEvidenceFile(info) || !os.SameFile(lstat, info) ||
		info.Size() < 1 || info.Size() > maximumEvidenceBytes {
		return nil, ErrEvidenceCorrupt
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximumEvidenceBytes+1))
	if err != nil || len(raw) > maximumEvidenceBytes {
		return nil, ErrEvidenceCorrupt
	}
	return raw, nil
}

func (store *FileEvidenceStore) objectPath(kind, key string) string {
	return filepath.Join(store.rootPath, store.objectRelative(kind, key))
}

func (store *FileEvidenceStore) objectRelative(kind, key string) string {
	return filepath.Join(kind, key[:2], key+".json")
}

func (store *FileEvidenceStore) validateDirectory(relative string) error {
	if store == nil || store.root == nil || !filepath.IsLocal(relative) {
		return fmt.Errorf("%w: invalid evidence directory", ErrInvalid)
	}
	current := ""
	for _, segment := range strings.Split(filepath.Clean(relative), string(filepath.Separator)) {
		if segment == "" || segment == "." {
			continue
		}
		current = filepath.Join(current, segment)
		info, err := store.root.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !privateEvidenceMode(info) {
			mode := os.FileMode(0)
			if info != nil {
				mode = info.Mode()
			}
			return fmt.Errorf(
				"%w: unsafe evidence directory %s (mode %s, stat %v)",
				ErrEvidenceCorrupt,
				current,
				mode,
				err,
			)
		}
	}
	return nil
}

func (store *FileEvidenceStore) writeImmutable(relative string, data []byte) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	directoryName := filepath.Dir(relative)
	temporaryBase := fmt.Sprintf(".%x.immutable", nonce[:])
	temporary := filepath.Join(directoryName, temporaryBase)
	file, err := store.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = store.root.Remove(temporary)
		}
	}()
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	directory, err := store.root.Open(directoryName)
	if err != nil {
		return err
	}
	publishErr := publishEvidenceNoReplace(
		directory,
		temporaryBase,
		filepath.Base(relative),
	)
	if errors.Is(publishErr, os.ErrExist) {
		closeDirectoryErr := directory.Close()
		existing, readErr := store.readEncoded(relative)
		if readErr == nil && bytes.Equal(existing, data) && closeDirectoryErr == nil {
			return nil
		}
		return errors.Join(ErrEvidenceConflict, readErr, closeDirectoryErr)
	}
	if publishErr != nil {
		_ = directory.Close()
		return publishErr
	}
	removeTemporary = false
	directorySyncErr := directory.Sync()
	directoryCloseErr := directory.Close()
	return errors.Join(directorySyncErr, directoryCloseErr)
}

func encodeEvidence(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode local CI evidence: %w", err)
	}
	if len(encoded) > maximumEvidenceBytes {
		return nil, fmt.Errorf("%w: local CI evidence exceeds byte limit", ErrInvalid)
	}
	return append(encoded, '\n'), nil
}

func mustJSON(value any) []byte {
	encoded, _ := encodeEvidence(value)
	return encoded
}

func finalizeCacheIndex(record cacheIndexRecord) (cacheIndexRecord, error) {
	record.Version = EvidenceVersion
	record.Digest = ""
	if !validDigest(record.ResultKey) || !validDigest(record.ExecutionDigest) ||
		record.CreatedAt.IsZero() || record.ExpiresAt.IsZero() ||
		record.CreatedAt.Location() != time.UTC || record.ExpiresAt.Location() != time.UTC ||
		!record.ExpiresAt.After(record.CreatedAt) || record.ExpiresAt.Sub(record.CreatedAt) > 7*24*time.Hour {
		return cacheIndexRecord{}, fmt.Errorf("%w: invalid cache index", ErrInvalid)
	}
	digest, err := digestJSON("picoclaw-local-ci-cache-index-v1", record)
	if err != nil {
		return cacheIndexRecord{}, err
	}
	record.Digest = digest
	return record, nil
}

func decodeStrictEvidence(raw []byte, target any) error {
	if err := validateJSONDocument(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("evidence contains trailing data")
	}
	return nil
}
