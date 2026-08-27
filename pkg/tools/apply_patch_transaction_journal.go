package tools

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	applyPatchTransactionJournalVersion = 1
	applyPatchTransactionPointerVersion = 1

	applyPatchTransactionJournalMaxBytes = 256 << 10
	applyPatchTransactionPointerMaxBytes = 8 << 10
	applyPatchTransactionJSONMaxDepth    = 16
	applyPatchTransactionMaxOperations   = 128
	applyPatchTransactionMaxEntries      = 1024
	applyPatchTransactionMaxBackupBytes  = 1 << 20
	applyPatchTransactionKeyBytes        = 32

	applyPatchTransactionDigestHexBytes = sha256.Size * 2
	applyPatchTransactionIDHexBytes     = 16 * 2
)

const (
	applyPatchTransactionJournalMACDomain = "picoclaw.apply-patch.journal.v1"
	applyPatchTransactionPointerMACDomain = "picoclaw.apply-patch.pointer.v1"
	applyPatchTransactionBackupMACDomain  = "picoclaw.apply-patch.backup.v1"
)

var (
	errApplyPatchTransactionJournalInvalid = errors.New(
		"apply-patch transaction journal is invalid",
	)
	errApplyPatchTransactionJournalAuthentication = errors.New(
		"apply-patch transaction journal authentication failed",
	)
	errApplyPatchTransactionPointerInvalid = errors.New(
		"apply-patch transaction pointer is invalid",
	)
	errApplyPatchTransactionPointerAuthentication = errors.New(
		"apply-patch transaction pointer authentication failed",
	)
	errApplyPatchTransactionBackupInvalid = errors.New(
		"apply-patch transaction backup is invalid",
	)
)

type applyPatchTransactionPhase string

const (
	applyPatchTransactionPhasePreparing   applyPatchTransactionPhase = "preparing"
	applyPatchTransactionPhasePrepared    applyPatchTransactionPhase = "prepared"
	applyPatchTransactionPhaseRollingBack applyPatchTransactionPhase = "rolling_back"
	applyPatchTransactionPhaseCommitted   applyPatchTransactionPhase = "committed"
)

type applyPatchTransactionWorkspaceBinding struct {
	CanonicalPath string                `json:"canonical_path"`
	PathSHA256    string                `json:"path_sha256"`
	Identity      applyPatchTxnIdentity `json:"identity"`
}

type applyPatchTransactionStateBinding struct {
	CanonicalRoot      string                `json:"canonical_root"`
	RootIdentity       applyPatchTxnIdentity `json:"root_identity"`
	KeyID              string                `json:"key_id"`
	WorkspaceDirectory string                `json:"workspace_directory"`
	ActiveDirectory    string                `json:"active_directory"`
	CommittedDirectory string                `json:"committed_directory"`
}

type applyPatchTransactionJournal struct {
	Version           int                                     `json:"version"`
	Workspace         applyPatchTransactionWorkspaceBinding   `json:"workspace"`
	State             applyPatchTransactionStateBinding       `json:"state"`
	TransactionID     string                                  `json:"transaction_id"`
	Phase             applyPatchTransactionPhase              `json:"phase"`
	DecisionAttempted bool                                    `json:"decision_attempted"`
	OperationCount    int                                     `json:"operation_count"`
	Operations        []applyPatchTransactionJournalOperation `json:"operations"`
	Artifacts         []applyPatchTransactionJournalArtifact  `json:"artifacts"`
	Forests           []applyPatchTransactionJournalForest    `json:"forests"`
}

type applyPatchTransactionJournalEndpoint struct {
	Label             string                 `json:"label"`
	CanonicalPath     string                 `json:"canonical_path"`
	PreflightIdentity *applyPatchTxnIdentity `json:"preflight_identity,omitempty"`
	PreflightLinks    uint64                 `json:"preflight_links"`
}

type applyPatchTransactionJournalFileState struct {
	Exists bool   `json:"exists"`
	Length uint64 `json:"length"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
}

type applyPatchTransactionJournalOperation struct {
	Index    int                                   `json:"index"`
	Kind     string                                `json:"kind"`
	Source   *applyPatchTransactionJournalEndpoint `json:"source,omitempty"`
	Target   *applyPatchTransactionJournalEndpoint `json:"target,omitempty"`
	Before   applyPatchTransactionJournalFileState `json:"before"`
	After    applyPatchTransactionJournalFileState `json:"after"`
	ForestID string                                `json:"forest_id,omitempty"`
}

type applyPatchTransactionJournalRootedLocation struct {
	AnchorCanonicalPath string                 `json:"anchor_canonical_path"`
	AnchorIdentity      applyPatchTxnIdentity  `json:"anchor_identity"`
	Basename            string                 `json:"basename"`
	RemovalBasename     string                 `json:"-"`
	RemovalAttempted    bool                   `json:"removal_attempted,omitempty"`
	Identity            *applyPatchTxnIdentity `json:"identity,omitempty"`
	Links               uint64                 `json:"links"`
}

type applyPatchTransactionBackupRecord struct {
	Length     uint64 `json:"length"`
	SHA256     string `json:"sha256"`
	HMACSHA256 string `json:"hmac_sha256"`
}

type applyPatchTransactionArtifactRole string

const (
	applyPatchTransactionArtifactBackupBlob               applyPatchTransactionArtifactRole = "backup_blob"
	applyPatchTransactionArtifactSourceRestoreStage       applyPatchTransactionArtifactRole = "source_restore_stage"
	applyPatchTransactionArtifactSourceProbeWitness       applyPatchTransactionArtifactRole = "source_probe_witness"
	applyPatchTransactionArtifactSourceWitness            applyPatchTransactionArtifactRole = "source_witness"
	applyPatchTransactionArtifactSourceQuarantine         applyPatchTransactionArtifactRole = "source_quarantine"
	applyPatchTransactionArtifactPostimageStage           applyPatchTransactionArtifactRole = "postimage_stage"
	applyPatchTransactionArtifactPostimageWitness         applyPatchTransactionArtifactRole = "postimage_witness"
	applyPatchTransactionArtifactTargetRollbackQuarantine applyPatchTransactionArtifactRole = "target_rollback_quarantine"
)

type applyPatchTransactionJournalArtifact struct {
	OperationIndex int                                         `json:"operation_index"`
	Role           applyPatchTransactionArtifactRole           `json:"role"`
	Rooted         *applyPatchTransactionJournalRootedLocation `json:"rooted,omitempty"`
	StateName      string                                      `json:"state_name,omitempty"`
	StateIdentity  *applyPatchTxnIdentity                      `json:"state_identity,omitempty"`
	StateLinks     uint64                                      `json:"state_links"`
	Expected       applyPatchTransactionJournalFileState       `json:"expected"`
	Backup         *applyPatchTransactionBackupRecord          `json:"backup,omitempty"`
}

type applyPatchTransactionJournalForestEntry struct {
	RelativePath     string                 `json:"relative_path"`
	CanonicalPath    string                 `json:"canonical_path"`
	Kind             string                 `json:"kind"`
	OperationIndex   *int                   `json:"operation_index,omitempty"`
	Mode             uint32                 `json:"mode"`
	Length           uint64                 `json:"length"`
	SHA256           string                 `json:"sha256"`
	Identity         *applyPatchTxnIdentity `json:"identity,omitempty"`
	Links            uint64                 `json:"links"`
	RemovalBasename  string                 `json:"-"`
	RemovalAttempted bool                   `json:"removal_attempted,omitempty"`
}

type applyPatchTransactionJournalForest struct {
	ID                   string                                     `json:"id"`
	OperationIndexes     []int                                      `json:"operation_indexes"`
	PublicRoot           string                                     `json:"public_root"`
	StageRoot            applyPatchTransactionJournalRootedLocation `json:"stage_root"`
	RollbackRoot         applyPatchTransactionJournalRootedLocation `json:"rollback_root"`
	SentinelRelativePath string                                     `json:"sentinel_relative_path"`
	SentinelWitness      applyPatchTransactionJournalRootedLocation `json:"sentinel_witness"`
	Entries              []applyPatchTransactionJournalForestEntry  `json:"entries"`
}

type applyPatchTransactionPointer struct {
	Version           int                                   `json:"version"`
	Workspace         applyPatchTransactionWorkspaceBinding `json:"workspace"`
	State             applyPatchTransactionStateBinding     `json:"state"`
	TransactionID     string                                `json:"transaction_id"`
	Phase             applyPatchTransactionPhase            `json:"phase"`
	DecisionAttempted bool                                  `json:"decision_attempted"`
	JournalSHA256     string                                `json:"journal_sha256"`
}

type applyPatchTransactionAuthenticatedEnvelope struct {
	Payload    json.RawMessage `json:"payload"`
	HMACSHA256 string          `json:"hmac_sha256"`
}

func encodeApplyPatchTransactionJournal(
	key []byte,
	journal *applyPatchTransactionJournal,
) ([]byte, error) {
	if err := validateApplyPatchTransactionJournal(key, journal); err != nil {
		return nil, err
	}
	return encodeApplyPatchTransactionAuthenticated(
		key,
		applyPatchTransactionJournalMACDomain,
		journal,
		applyPatchTransactionJournalMaxBytes,
		errApplyPatchTransactionJournalInvalid,
	)
}

func decodeApplyPatchTransactionJournal(
	key []byte,
	data []byte,
) (*applyPatchTransactionJournal, error) {
	var journal applyPatchTransactionJournal
	if err := decodeApplyPatchTransactionAuthenticated(
		key,
		applyPatchTransactionJournalMACDomain,
		data,
		applyPatchTransactionJournalMaxBytes,
		errApplyPatchTransactionJournalInvalid,
		errApplyPatchTransactionJournalAuthentication,
		&journal,
	); err != nil {
		return nil, err
	}
	if err := validateApplyPatchTransactionJournal(key, &journal); err != nil {
		return nil, err
	}
	return &journal, nil
}

func encodeApplyPatchTransactionPointer(
	key []byte,
	pointer *applyPatchTransactionPointer,
) ([]byte, error) {
	if err := validateApplyPatchTransactionPointer(key, pointer); err != nil {
		return nil, err
	}
	return encodeApplyPatchTransactionAuthenticated(
		key,
		applyPatchTransactionPointerMACDomain,
		pointer,
		applyPatchTransactionPointerMaxBytes,
		errApplyPatchTransactionPointerInvalid,
	)
}

func decodeApplyPatchTransactionPointer(
	key []byte,
	data []byte,
) (*applyPatchTransactionPointer, error) {
	var pointer applyPatchTransactionPointer
	if err := decodeApplyPatchTransactionAuthenticated(
		key,
		applyPatchTransactionPointerMACDomain,
		data,
		applyPatchTransactionPointerMaxBytes,
		errApplyPatchTransactionPointerInvalid,
		errApplyPatchTransactionPointerAuthentication,
		&pointer,
	); err != nil {
		return nil, err
	}
	if err := validateApplyPatchTransactionPointer(key, &pointer); err != nil {
		return nil, err
	}
	return &pointer, nil
}

func newApplyPatchTransactionBackupRecord(
	key []byte,
	transactionID string,
	stateName string,
	data []byte,
) (applyPatchTransactionBackupRecord, error) {
	if err := validateApplyPatchTransactionKey(key, errApplyPatchTransactionBackupInvalid); err != nil {
		return applyPatchTransactionBackupRecord{}, err
	}
	if !validApplyPatchTransactionHex(transactionID, applyPatchTransactionIDHexBytes) {
		return applyPatchTransactionBackupRecord{}, fmt.Errorf(
			"%w: transaction ID",
			errApplyPatchTransactionBackupInvalid,
		)
	}
	if err := validateApplyPatchTransactionRandomPrivateName(stateName, "backup"); err != nil {
		return applyPatchTransactionBackupRecord{}, fmt.Errorf(
			"%w: private name",
			errApplyPatchTransactionBackupInvalid,
		)
	}
	if len(data) > applyPatchTransactionMaxBackupBytes {
		return applyPatchTransactionBackupRecord{}, fmt.Errorf(
			"%w: size limit",
			errApplyPatchTransactionBackupInvalid,
		)
	}
	digest := sha256.Sum256(data)
	mac := applyPatchTransactionBackupMAC(key, transactionID, stateName, data)
	return applyPatchTransactionBackupRecord{
		Length:     uint64(len(data)),
		SHA256:     hex.EncodeToString(digest[:]),
		HMACSHA256: hex.EncodeToString(mac),
	}, nil
}

func verifyApplyPatchTransactionBackup(
	key []byte,
	transactionID string,
	stateName string,
	record applyPatchTransactionBackupRecord,
	data []byte,
) error {
	if err := validateApplyPatchTransactionKey(key, errApplyPatchTransactionBackupInvalid); err != nil {
		return err
	}
	if !validApplyPatchTransactionHex(transactionID, applyPatchTransactionIDHexBytes) {
		return fmt.Errorf("%w: transaction ID", errApplyPatchTransactionBackupInvalid)
	}
	if err := validateApplyPatchTransactionRandomPrivateName(stateName, "backup"); err != nil {
		return fmt.Errorf("%w: private name", errApplyPatchTransactionBackupInvalid)
	}
	if err := validateApplyPatchTransactionBackupRecord(record); err != nil {
		return err
	}
	if len(data) > applyPatchTransactionMaxBackupBytes || uint64(len(data)) != record.Length {
		return fmt.Errorf("%w: length mismatch", errApplyPatchTransactionBackupInvalid)
	}
	digest := sha256.Sum256(data)
	recordedDigest, _ := hex.DecodeString(record.SHA256)
	if !hmac.Equal(digest[:], recordedDigest) {
		return fmt.Errorf("%w: digest mismatch", errApplyPatchTransactionBackupInvalid)
	}
	expectedMAC := applyPatchTransactionBackupMAC(key, transactionID, stateName, data)
	recordedMAC, _ := hex.DecodeString(record.HMACSHA256)
	if !hmac.Equal(expectedMAC, recordedMAC) {
		return fmt.Errorf("%w: authentication failed", errApplyPatchTransactionBackupInvalid)
	}
	return nil
}

func encodeApplyPatchTransactionAuthenticated(
	key []byte,
	domain string,
	payloadValue any,
	maxBytes int,
	invalidError error,
) ([]byte, error) {
	if err := validateApplyPatchTransactionKey(key, invalidError); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(payloadValue)
	if err != nil {
		return nil, fmt.Errorf("%w: encode payload", invalidError)
	}
	mac := applyPatchTransactionMAC(key, domain, payload)
	encoded := encodeApplyPatchTransactionEnvelope(payload, hex.EncodeToString(mac))
	if len(encoded) > maxBytes {
		return nil, fmt.Errorf("%w: size limit", invalidError)
	}
	return encoded, nil
}

func decodeApplyPatchTransactionAuthenticated(
	key []byte,
	domain string,
	data []byte,
	maxBytes int,
	invalidError error,
	authenticationError error,
	destination any,
) error {
	if err := validateApplyPatchTransactionKey(key, invalidError); err != nil {
		return err
	}
	if len(data) == 0 || len(data) > maxBytes {
		return fmt.Errorf("%w: size limit", invalidError)
	}
	if err := validateApplyPatchTransactionJSON(data); err != nil {
		return fmt.Errorf("%w: malformed JSON", invalidError)
	}
	var envelope applyPatchTransactionAuthenticatedEnvelope
	if err := decodeApplyPatchTransactionJSON(data, &envelope); err != nil {
		return fmt.Errorf("%w: malformed envelope", invalidError)
	}
	if len(envelope.Payload) == 0 ||
		!validApplyPatchTransactionHex(envelope.HMACSHA256, applyPatchTransactionDigestHexBytes) {
		return fmt.Errorf("%w: malformed envelope", invalidError)
	}
	recordedMAC, _ := hex.DecodeString(envelope.HMACSHA256)
	expectedMAC := applyPatchTransactionMAC(key, domain, envelope.Payload)
	if !hmac.Equal(expectedMAC, recordedMAC) {
		return authenticationError
	}
	if err := validateApplyPatchTransactionJSON(envelope.Payload); err != nil {
		return fmt.Errorf("%w: malformed payload", invalidError)
	}
	if err := decodeApplyPatchTransactionJSON(envelope.Payload, destination); err != nil {
		return fmt.Errorf("%w: malformed payload", invalidError)
	}
	canonicalPayload, err := json.Marshal(destination)
	if err != nil || !bytes.Equal(canonicalPayload, envelope.Payload) {
		return fmt.Errorf("%w: noncanonical payload", invalidError)
	}
	canonicalEnvelope := encodeApplyPatchTransactionEnvelope(
		canonicalPayload,
		envelope.HMACSHA256,
	)
	if !bytes.Equal(canonicalEnvelope, data) {
		return fmt.Errorf("%w: noncanonical envelope", invalidError)
	}
	return nil
}

func encodeApplyPatchTransactionEnvelope(payload []byte, mac string) []byte {
	encoded := make([]byte, 0, len(payload)+len(mac)+32)
	encoded = append(encoded, `{"payload":`...)
	encoded = append(encoded, payload...)
	encoded = append(encoded, `,"hmac_sha256":"`...)
	encoded = append(encoded, mac...)
	encoded = append(encoded, `"}`...)
	return encoded
}

func applyPatchTransactionMAC(key []byte, domain string, payload []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(domain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

func applyPatchTransactionBackupMAC(
	key []byte,
	transactionID string,
	stateName string,
	data []byte,
) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(applyPatchTransactionBackupMACDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(transactionID))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(stateName))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(data)
	return mac.Sum(nil)
}

func validateApplyPatchTransactionJournal(
	key []byte,
	journal *applyPatchTransactionJournal,
) error {
	if err := validateApplyPatchTransactionKey(key, errApplyPatchTransactionJournalInvalid); err != nil {
		return err
	}
	if journal == nil {
		return fmt.Errorf("%w: missing value", errApplyPatchTransactionJournalInvalid)
	}
	populateApplyPatchTxnRemovalNames(journal)
	if journal.Version != applyPatchTransactionJournalVersion {
		return fmt.Errorf("%w: version", errApplyPatchTransactionJournalInvalid)
	}
	if err := validateApplyPatchTransactionBindings(
		key,
		journal.Workspace,
		journal.State,
		errApplyPatchTransactionJournalInvalid,
	); err != nil {
		return err
	}
	if !validApplyPatchTransactionHex(journal.TransactionID, applyPatchTransactionIDHexBytes) {
		return fmt.Errorf("%w: transaction ID", errApplyPatchTransactionJournalInvalid)
	}
	if err := validateApplyPatchTransactionStateNames(
		journal.TransactionID,
		journal.State,
		errApplyPatchTransactionJournalInvalid,
	); err != nil {
		return err
	}
	if err := validateApplyPatchTransactionPhase(
		journal.Phase,
		journal.DecisionAttempted,
		errApplyPatchTransactionJournalInvalid,
	); err != nil {
		return err
	}
	if journal.Operations == nil || journal.Artifacts == nil || journal.Forests == nil {
		return fmt.Errorf("%w: null collection", errApplyPatchTransactionJournalInvalid)
	}
	if journal.OperationCount != len(journal.Operations) ||
		journal.OperationCount < 1 || journal.OperationCount > applyPatchTransactionMaxOperations {
		return fmt.Errorf("%w: operation count", errApplyPatchTransactionJournalInvalid)
	}
	if err := validateApplyPatchTransactionOperations(journal); err != nil {
		return err
	}
	if err := validateApplyPatchTransactionArtifacts(journal); err != nil {
		return err
	}
	if err := validateApplyPatchTransactionForests(journal); err != nil {
		return err
	}
	entryCount := len(journal.Artifacts)
	for index := range journal.Forests {
		forest := &journal.Forests[index]
		if len(forest.Entries) > applyPatchTransactionMaxEntries-entryCount {
			return fmt.Errorf("%w: entry count", errApplyPatchTransactionJournalInvalid)
		}
		entryCount += len(forest.Entries)
		if 3 > applyPatchTransactionMaxEntries-entryCount {
			return fmt.Errorf("%w: entry count", errApplyPatchTransactionJournalInvalid)
		}
		entryCount += 3
	}
	return nil
}

func populateApplyPatchTxnRemovalNames(journal *applyPatchTransactionJournal) {
	if journal == nil {
		return
	}
	for index := range journal.Artifacts {
		artifact := &journal.Artifacts[index]
		if artifact.Rooted != nil {
			artifact.Rooted.RemovalBasename = applyPatchTxnDerivedRemovalBasename(
				journal.TransactionID,
				artifact.Rooted.AnchorCanonicalPath,
				artifact.Rooted.Basename,
			)
		}
	}
	for forestIndex := range journal.Forests {
		forest := &journal.Forests[forestIndex]
		for _, location := range []*applyPatchTransactionJournalRootedLocation{
			&forest.StageRoot,
			&forest.RollbackRoot,
			&forest.SentinelWitness,
		} {
			location.RemovalBasename = applyPatchTxnDerivedRemovalBasename(
				journal.TransactionID,
				location.AnchorCanonicalPath,
				location.Basename,
			)
		}
		for entryIndex := range forest.Entries {
			entry := &forest.Entries[entryIndex]
			if entry.RelativePath == "." {
				entry.RemovalBasename = ""
				continue
			}
			entry.RemovalBasename = applyPatchTxnDerivedRemovalBasename(
				journal.TransactionID,
				forest.ID,
				entry.RelativePath,
			)
		}
	}
}

func applyPatchTxnDerivedRemovalBasename(transactionID string, parts ...string) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("picoclaw.apply-patch.remove.v1"))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(transactionID))
	for _, part := range parts {
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(part))
	}
	sum := digest.Sum(nil)
	return ".picoclaw-apply-patch-remove-" + hex.EncodeToString(sum[:16])
}

func validateApplyPatchTransactionPointer(
	key []byte,
	pointer *applyPatchTransactionPointer,
) error {
	if err := validateApplyPatchTransactionKey(key, errApplyPatchTransactionPointerInvalid); err != nil {
		return err
	}
	if pointer == nil {
		return fmt.Errorf("%w: missing value", errApplyPatchTransactionPointerInvalid)
	}
	if pointer.Version != applyPatchTransactionPointerVersion {
		return fmt.Errorf("%w: version", errApplyPatchTransactionPointerInvalid)
	}
	if err := validateApplyPatchTransactionBindings(
		key,
		pointer.Workspace,
		pointer.State,
		errApplyPatchTransactionPointerInvalid,
	); err != nil {
		return err
	}
	if !validApplyPatchTransactionHex(pointer.TransactionID, applyPatchTransactionIDHexBytes) {
		return fmt.Errorf("%w: transaction ID", errApplyPatchTransactionPointerInvalid)
	}
	if err := validateApplyPatchTransactionStateNames(
		pointer.TransactionID,
		pointer.State,
		errApplyPatchTransactionPointerInvalid,
	); err != nil {
		return err
	}
	if err := validateApplyPatchTransactionPhase(
		pointer.Phase,
		pointer.DecisionAttempted,
		errApplyPatchTransactionPointerInvalid,
	); err != nil {
		return err
	}
	if !validApplyPatchTransactionHex(pointer.JournalSHA256, applyPatchTransactionDigestHexBytes) {
		return fmt.Errorf("%w: journal digest", errApplyPatchTransactionPointerInvalid)
	}
	return nil
}

func validateApplyPatchTransactionBindings(
	key []byte,
	workspace applyPatchTransactionWorkspaceBinding,
	state applyPatchTransactionStateBinding,
	invalidError error,
) error {
	if err := validateApplyPatchTransactionCanonicalPath(workspace.CanonicalPath); err != nil {
		return fmt.Errorf("%w: workspace path", invalidError)
	}
	workspaceDigest := sha256.Sum256([]byte(workspace.CanonicalPath))
	if workspace.PathSHA256 != hex.EncodeToString(workspaceDigest[:]) {
		return fmt.Errorf("%w: workspace digest", invalidError)
	}
	if !workspace.Identity.valid("directory") {
		return fmt.Errorf("%w: workspace identity", invalidError)
	}
	if err := validateApplyPatchTransactionCanonicalPath(state.CanonicalRoot); err != nil {
		return fmt.Errorf("%w: state root", invalidError)
	}
	if !state.RootIdentity.valid("directory") {
		return fmt.Errorf("%w: state root identity", invalidError)
	}
	keyDigest := sha256.Sum256(key)
	if state.KeyID != hex.EncodeToString(keyDigest[:]) {
		return fmt.Errorf("%w: key identity", invalidError)
	}
	physicalDigest, err := applyPatchTxnWorkspaceIdentityDigest(workspace.Identity)
	if err != nil || state.WorkspaceDirectory != "workspaces/"+physicalDigest {
		return fmt.Errorf("%w: workspace state directory", invalidError)
	}
	if err := validateApplyPatchTransactionPrivateBasename(state.ActiveDirectory); err != nil {
		return fmt.Errorf("%w: active directory", invalidError)
	}
	if err := validateApplyPatchTransactionPrivateBasename(state.CommittedDirectory); err != nil {
		return fmt.Errorf("%w: committed directory", invalidError)
	}
	if state.ActiveDirectory == state.CommittedDirectory ||
		applyPatchPathsOverlap(workspace.CanonicalPath, state.CanonicalRoot) {
		return fmt.Errorf("%w: state binding overlap", invalidError)
	}
	return nil
}

func validateApplyPatchTransactionPhase(
	phase applyPatchTransactionPhase,
	decisionAttempted bool,
	invalidError error,
) error {
	switch phase {
	case applyPatchTransactionPhasePreparing:
		if decisionAttempted {
			return fmt.Errorf("%w: preparing decision", invalidError)
		}
	case applyPatchTransactionPhasePrepared, applyPatchTransactionPhaseRollingBack:
	case applyPatchTransactionPhaseCommitted:
		if !decisionAttempted {
			return fmt.Errorf("%w: committed decision", invalidError)
		}
	default:
		return fmt.Errorf("%w: phase", invalidError)
	}
	return nil
}

func validateApplyPatchTransactionStateNames(
	transactionID string,
	state applyPatchTransactionStateBinding,
	invalidError error,
) error {
	if state.ActiveDirectory != "active-"+transactionID ||
		state.CommittedDirectory != "committed-"+transactionID {
		return fmt.Errorf("%w: transaction state names", invalidError)
	}
	return nil
}

func validateApplyPatchTransactionOperations(journal *applyPatchTransactionJournal) error {
	totalBytes := uint64(0)
	totalLabels := 0
	type operationPath struct {
		path      string
		operation int
	}
	seenPaths := make([]operationPath, 0, journal.OperationCount*2)
	for index := range journal.Operations {
		op := &journal.Operations[index]
		if op.Index != index {
			return fmt.Errorf("%w: operation order", errApplyPatchTransactionJournalInvalid)
		}
		if err := validateApplyPatchTransactionFileState(op.Before); err != nil {
			return err
		}
		if err := validateApplyPatchTransactionFileState(op.After); err != nil {
			return err
		}
		if op.Before.Length > applyPatchTransactionMaxBackupBytes-totalBytes {
			return fmt.Errorf("%w: input byte limit", errApplyPatchTransactionJournalInvalid)
		}
		totalBytes += op.Before.Length
		if op.After.Length > applyPatchTransactionMaxBackupBytes-totalBytes {
			return fmt.Errorf("%w: input byte limit", errApplyPatchTransactionJournalInvalid)
		}
		totalBytes += op.After.Length

		logicalEndpoints, err := validateApplyPatchTransactionOperationShape(op)
		if err != nil {
			return err
		}
		for _, endpoint := range logicalEndpoints {
			quoted, quoteErr := quoteApplyPatchCandidatePath("", endpoint.Label)
			if quoteErr != nil || len(quoted) > applyPatchCandidateMaxPathBytes {
				return fmt.Errorf("%w: label", errApplyPatchTransactionJournalInvalid)
			}
			if len(quoted) > applyPatchCandidateMaxAllPathBytes-totalLabels {
				return fmt.Errorf("%w: aggregate labels", errApplyPatchTransactionJournalInvalid)
			}
			totalLabels += len(quoted)
		}
		for _, endpoint := range []*applyPatchTransactionJournalEndpoint{op.Source, op.Target} {
			if endpoint == nil {
				continue
			}
			if err := validateApplyPatchTransactionEndpoint(endpoint); err != nil {
				return err
			}
			if applyPatchPathWithinExact(endpoint.CanonicalPath, journal.State.CanonicalRoot) {
				return fmt.Errorf("%w: protected endpoint", errApplyPatchTransactionJournalInvalid)
			}
			for _, prior := range seenPaths {
				if (endpoint.CanonicalPath == prior.path && index != prior.operation) ||
					(endpoint.CanonicalPath != prior.path &&
						applyPatchPathsOverlap(endpoint.CanonicalPath, prior.path)) {
					return fmt.Errorf("%w: overlapping operation paths", errApplyPatchTransactionJournalInvalid)
				}
			}
			seenPaths = append(seenPaths, operationPath{path: endpoint.CanonicalPath, operation: index})
		}
	}
	return nil
}

func validateApplyPatchTransactionOperationShape(
	op *applyPatchTransactionJournalOperation,
) ([]*applyPatchTransactionJournalEndpoint, error) {
	invalid := func() ([]*applyPatchTransactionJournalEndpoint, error) {
		return nil, fmt.Errorf("%w: operation shape", errApplyPatchTransactionJournalInvalid)
	}
	switch op.Kind {
	case "add":
		if op.Source != nil || op.Target == nil || op.Before.Exists || !op.After.Exists {
			return invalid()
		}
		if op.Target.PreflightIdentity != nil {
			return invalid()
		}
		return []*applyPatchTransactionJournalEndpoint{op.Target}, nil
	case "delete":
		if op.Source == nil || op.Target != nil || !op.Before.Exists || op.After.Exists ||
			op.ForestID != "" || op.Source.PreflightIdentity == nil {
			return invalid()
		}
		return []*applyPatchTransactionJournalEndpoint{op.Source}, nil
	case "update":
		if op.Source == nil || op.Target == nil || !op.Before.Exists || !op.After.Exists ||
			op.ForestID != "" || op.Source.Label != op.Target.Label ||
			op.Source.CanonicalPath != op.Target.CanonicalPath ||
			op.Source.PreflightIdentity == nil || op.Target.PreflightIdentity == nil ||
			!op.Source.PreflightIdentity.equal(*op.Target.PreflightIdentity) {
			return invalid()
		}
		return []*applyPatchTransactionJournalEndpoint{op.Source}, nil
	case "move":
		if op.Source == nil || op.Target == nil || !op.Before.Exists || !op.After.Exists ||
			op.Source.CanonicalPath == op.Target.CanonicalPath ||
			op.Source.PreflightIdentity == nil || op.Target.PreflightIdentity != nil {
			return invalid()
		}
		return []*applyPatchTransactionJournalEndpoint{op.Source, op.Target}, nil
	default:
		return invalid()
	}
}

func validateApplyPatchTransactionEndpoint(
	endpoint *applyPatchTransactionJournalEndpoint,
) error {
	if endpoint == nil || endpoint.Label == "" || !utf8.ValidString(endpoint.Label) ||
		strings.ContainsRune(endpoint.Label, '\x00') {
		return fmt.Errorf("%w: endpoint label", errApplyPatchTransactionJournalInvalid)
	}
	if err := validateApplyPatchTransactionCanonicalPath(endpoint.CanonicalPath); err != nil {
		return fmt.Errorf("%w: endpoint path", errApplyPatchTransactionJournalInvalid)
	}
	if endpoint.PreflightIdentity != nil && !endpoint.PreflightIdentity.valid("regular") {
		return fmt.Errorf("%w: endpoint identity", errApplyPatchTransactionJournalInvalid)
	}
	if endpoint.PreflightIdentity == nil && endpoint.PreflightLinks != 0 ||
		endpoint.PreflightIdentity != nil && endpoint.PreflightLinks != 1 {
		return fmt.Errorf("%w: endpoint link count", errApplyPatchTransactionJournalInvalid)
	}
	return nil
}

func validateApplyPatchTransactionFileState(
	state applyPatchTransactionJournalFileState,
) error {
	if state.Mode&^uint32(0o777) != 0 {
		return fmt.Errorf("%w: file mode", errApplyPatchTransactionJournalInvalid)
	}
	if !state.Exists {
		if state.Length != 0 || state.SHA256 != "" || state.Mode != 0 {
			return fmt.Errorf("%w: absent file state", errApplyPatchTransactionJournalInvalid)
		}
		return nil
	}
	if !validApplyPatchTransactionHex(state.SHA256, applyPatchTransactionDigestHexBytes) {
		return fmt.Errorf("%w: file digest", errApplyPatchTransactionJournalInvalid)
	}
	return nil
}

func validateApplyPatchTransactionArtifacts(journal *applyPatchTransactionJournal) error {
	type artifactKey struct {
		operation int
		role      applyPatchTransactionArtifactRole
	}
	seen := make(map[artifactKey]struct{}, len(journal.Artifacts))
	rootedNames := make(map[string]struct{}, len(journal.Artifacts))
	stateNames := make(map[string]struct{}, len(journal.Artifacts))
	backupBytes := uint64(0)
	for index := range journal.Artifacts {
		artifact := &journal.Artifacts[index]
		if artifact.OperationIndex < 0 || artifact.OperationIndex >= journal.OperationCount {
			return fmt.Errorf("%w: artifact operation", errApplyPatchTransactionJournalInvalid)
		}
		key := artifactKey{operation: artifact.OperationIndex, role: artifact.Role}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("%w: duplicate artifact", errApplyPatchTransactionJournalInvalid)
		}
		seen[key] = struct{}{}
		op := &journal.Operations[artifact.OperationIndex]
		expected, sourceRole, targetRole, err := applyPatchTransactionArtifactExpectation(
			op,
			artifact.Role,
		)
		if err != nil || artifact.Expected != expected {
			return fmt.Errorf("%w: artifact role", errApplyPatchTransactionJournalInvalid)
		}
		if sourceRole && op.Source == nil || targetRole && (op.Target == nil || op.ForestID != "") {
			return fmt.Errorf("%w: artifact endpoint", errApplyPatchTransactionJournalInvalid)
		}
		privateName := artifact.StateName
		if artifact.Rooted != nil {
			privateName = artifact.Rooted.Basename
		}
		if err := validateApplyPatchTransactionRandomPrivateName(
			privateName,
			applyPatchTransactionArtifactNamePrefix(artifact.Role),
		); err != nil {
			return fmt.Errorf("%w: artifact random name", errApplyPatchTransactionJournalInvalid)
		}
		if artifact.Role == applyPatchTransactionArtifactBackupBlob {
			if artifact.Rooted != nil || artifact.StateName == "" || artifact.Backup == nil {
				return fmt.Errorf("%w: backup artifact", errApplyPatchTransactionJournalInvalid)
			}
			if err := validateApplyPatchTransactionPrivateBasename(artifact.StateName); err != nil {
				return fmt.Errorf("%w: backup name", errApplyPatchTransactionJournalInvalid)
			}
			if _, duplicate := stateNames[artifact.StateName]; duplicate {
				return fmt.Errorf("%w: duplicate state name", errApplyPatchTransactionJournalInvalid)
			}
			stateNames[artifact.StateName] = struct{}{}
			if artifact.StateIdentity != nil && !artifact.StateIdentity.valid("regular") {
				return fmt.Errorf("%w: backup identity", errApplyPatchTransactionJournalInvalid)
			}
			if artifact.StateIdentity == nil && artifact.StateLinks != 0 ||
				artifact.StateIdentity != nil && artifact.StateLinks != 1 {
				return fmt.Errorf("%w: backup link count", errApplyPatchTransactionJournalInvalid)
			}
			if err := validateApplyPatchTransactionBackupRecord(*artifact.Backup); err != nil ||
				artifact.Backup.Length != expected.Length ||
				artifact.Backup.SHA256 != expected.SHA256 {
				return fmt.Errorf("%w: backup record", errApplyPatchTransactionJournalInvalid)
			}
			if expected.Length > applyPatchTransactionMaxBackupBytes-backupBytes {
				return fmt.Errorf("%w: backup byte limit", errApplyPatchTransactionJournalInvalid)
			}
			backupBytes += expected.Length
			continue
		}
		if artifact.Rooted == nil || artifact.StateName != "" ||
			artifact.StateIdentity != nil || artifact.StateLinks != 0 || artifact.Backup != nil {
			return fmt.Errorf("%w: rooted artifact", errApplyPatchTransactionJournalInvalid)
		}
		if err := validateApplyPatchTransactionRootedLocation(artifact.Rooted, "regular"); err != nil {
			return err
		}
		if applyPatchPathWithinExact(
			filepath.Join(artifact.Rooted.AnchorCanonicalPath, artifact.Rooted.Basename),
			journal.State.CanonicalRoot,
		) {
			return fmt.Errorf("%w: protected artifact", errApplyPatchTransactionJournalInvalid)
		}
		if artifact.Rooted.Identity != nil {
			switch artifact.Role {
			case applyPatchTransactionArtifactSourceRestoreStage:
				if journal.Phase != applyPatchTransactionPhaseRollingBack &&
					journal.Phase != applyPatchTransactionPhasePreparing {
					return fmt.Errorf("%w: restore stage phase", errApplyPatchTransactionJournalInvalid)
				}
			case applyPatchTransactionArtifactSourceProbeWitness:
				if journal.Phase != applyPatchTransactionPhasePreparing {
					return fmt.Errorf("%w: source probe witness phase", errApplyPatchTransactionJournalInvalid)
				}
				if artifact.Rooted.Links != 2 {
					return fmt.Errorf("%w: witness link count", errApplyPatchTransactionJournalInvalid)
				}
			case applyPatchTransactionArtifactSourceWitness,
				applyPatchTransactionArtifactSourceQuarantine,
				applyPatchTransactionArtifactPostimageWitness,
				applyPatchTransactionArtifactTargetRollbackQuarantine:
				if artifact.Rooted.Links != 2 {
					return fmt.Errorf("%w: witness link count", errApplyPatchTransactionJournalInvalid)
				}
			}
		}
		nameKey := artifact.Rooted.AnchorCanonicalPath + "\x00" + artifact.Rooted.Basename
		if _, duplicate := rootedNames[nameKey]; duplicate {
			return fmt.Errorf("%w: duplicate rooted name", errApplyPatchTransactionJournalInvalid)
		}
		rootedNames[nameKey] = struct{}{}
		removalKey := artifact.Rooted.AnchorCanonicalPath + "\x00" +
			artifact.Rooted.RemovalBasename
		if _, duplicate := rootedNames[removalKey]; duplicate {
			return fmt.Errorf("%w: duplicate rooted removal name", errApplyPatchTransactionJournalInvalid)
		}
		rootedNames[removalKey] = struct{}{}
	}
	for index := range journal.Operations {
		op := &journal.Operations[index]
		required := []applyPatchTransactionArtifactRole{}
		if op.Kind != "add" {
			required = append(required,
				applyPatchTransactionArtifactBackupBlob,
				applyPatchTransactionArtifactSourceRestoreStage,
				applyPatchTransactionArtifactSourceProbeWitness,
				applyPatchTransactionArtifactSourceWitness,
				applyPatchTransactionArtifactSourceQuarantine,
			)
		}
		if op.Kind != "delete" && op.ForestID == "" {
			required = append(required,
				applyPatchTransactionArtifactPostimageStage,
				applyPatchTransactionArtifactPostimageWitness,
				applyPatchTransactionArtifactTargetRollbackQuarantine,
			)
		}
		for _, role := range required {
			if _, found := seen[artifactKey{operation: index, role: role}]; !found {
				return fmt.Errorf("%w: missing artifact", errApplyPatchTransactionJournalInvalid)
			}
		}
		if err := validateApplyPatchTransactionArtifactIdentities(index, journal); err != nil {
			return err
		}
	}
	if journal.Phase != applyPatchTransactionPhasePreparing {
		for index := range journal.Artifacts {
			artifact := &journal.Artifacts[index]
			switch artifact.Role {
			case applyPatchTransactionArtifactBackupBlob:
				if artifact.StateIdentity == nil || artifact.StateLinks != 1 {
					return fmt.Errorf("%w: uncheckpointed backup", errApplyPatchTransactionJournalInvalid)
				}
			case applyPatchTransactionArtifactPostimageStage,
				applyPatchTransactionArtifactPostimageWitness:
				if artifact.Rooted.Identity == nil || artifact.Rooted.Links != 2 {
					return fmt.Errorf("%w: uncheckpointed stage", errApplyPatchTransactionJournalInvalid)
				}
			}
		}
	}
	return nil
}

func validateApplyPatchTransactionArtifactIdentities(
	operationIndex int,
	journal *applyPatchTransactionJournal,
) error {
	artifacts := make(map[applyPatchTransactionArtifactRole]*applyPatchTransactionJournalArtifact)
	for index := range journal.Artifacts {
		artifact := &journal.Artifacts[index]
		if artifact.OperationIndex == operationIndex {
			artifacts[artifact.Role] = artifact
		}
	}
	op := &journal.Operations[operationIndex]
	for _, role := range []applyPatchTransactionArtifactRole{
		applyPatchTransactionArtifactSourceWitness,
		applyPatchTransactionArtifactSourceQuarantine,
	} {
		artifact := artifacts[role]
		if artifact != nil && artifact.Rooted.Identity != nil &&
			(op.Source == nil || op.Source.PreflightIdentity == nil ||
				!artifact.Rooted.Identity.equal(*op.Source.PreflightIdentity)) {
			return fmt.Errorf("%w: source witness identity", errApplyPatchTransactionJournalInvalid)
		}
	}
	sourceWitness := artifacts[applyPatchTransactionArtifactSourceWitness]
	sourceQuarantine := artifacts[applyPatchTransactionArtifactSourceQuarantine]
	if !matchingApplyPatchTransactionCheckpointIdentities(sourceWitness, sourceQuarantine) {
		return fmt.Errorf("%w: original witness link state", errApplyPatchTransactionJournalInvalid)
	}
	sourceProbeStage := artifacts[applyPatchTransactionArtifactSourceRestoreStage]
	sourceProbeWitness := artifacts[applyPatchTransactionArtifactSourceProbeWitness]
	if !matchingApplyPatchTransactionCheckpointIdentities(sourceProbeStage, sourceProbeWitness) {
		return fmt.Errorf("%w: source probe witness link state", errApplyPatchTransactionJournalInvalid)
	}
	stage := artifacts[applyPatchTransactionArtifactPostimageStage]
	witness := artifacts[applyPatchTransactionArtifactPostimageWitness]
	rollback := artifacts[applyPatchTransactionArtifactTargetRollbackQuarantine]
	if !matchingApplyPatchTransactionCheckpointIdentities(stage, witness) ||
		!matchingApplyPatchTransactionCheckpointIdentities(stage, rollback) ||
		!matchingApplyPatchTransactionCheckpointIdentities(witness, rollback) {
		return fmt.Errorf("%w: postimage witness identity", errApplyPatchTransactionJournalInvalid)
	}
	if journal.Phase == applyPatchTransactionPhaseCommitted && op.Kind != "add" {
		for _, role := range []applyPatchTransactionArtifactRole{
			applyPatchTransactionArtifactSourceWitness,
			applyPatchTransactionArtifactSourceQuarantine,
		} {
			if artifacts[role] == nil || artifacts[role].Rooted.Identity == nil {
				return fmt.Errorf("%w: committed source identity", errApplyPatchTransactionJournalInvalid)
			}
		}
	}
	return nil
}

func matchingApplyPatchTransactionCheckpointIdentities(
	left *applyPatchTransactionJournalArtifact,
	right *applyPatchTransactionJournalArtifact,
) bool {
	if left == nil || right == nil || left.Rooted == nil || right.Rooted == nil ||
		left.Rooted.Identity == nil || right.Rooted.Identity == nil {
		return true
	}
	return left.Rooted.Identity.equal(*right.Rooted.Identity) &&
		left.Rooted.Links == right.Rooted.Links
}

func applyPatchTransactionArtifactExpectation(
	op *applyPatchTransactionJournalOperation,
	role applyPatchTransactionArtifactRole,
) (applyPatchTransactionJournalFileState, bool, bool, error) {
	switch role {
	case applyPatchTransactionArtifactBackupBlob,
		applyPatchTransactionArtifactSourceRestoreStage,
		applyPatchTransactionArtifactSourceProbeWitness,
		applyPatchTransactionArtifactSourceWitness,
		applyPatchTransactionArtifactSourceQuarantine:
		return op.Before, true, false, nil
	case applyPatchTransactionArtifactPostimageStage,
		applyPatchTransactionArtifactPostimageWitness,
		applyPatchTransactionArtifactTargetRollbackQuarantine:
		return op.After, false, true, nil
	default:
		return applyPatchTransactionJournalFileState{}, false, false,
			fmt.Errorf("%w: artifact role", errApplyPatchTransactionJournalInvalid)
	}
}

func applyPatchTransactionArtifactNamePrefix(
	role applyPatchTransactionArtifactRole,
) string {
	switch role {
	case applyPatchTransactionArtifactBackupBlob:
		return "backup"
	case applyPatchTransactionArtifactSourceRestoreStage:
		return "source-restore"
	case applyPatchTransactionArtifactSourceProbeWitness:
		return "source-probe-witness"
	case applyPatchTransactionArtifactSourceWitness:
		return "source-witness"
	case applyPatchTransactionArtifactSourceQuarantine:
		return "source-quarantine"
	case applyPatchTransactionArtifactPostimageStage:
		return "stage"
	case applyPatchTransactionArtifactPostimageWitness:
		return "post-witness"
	case applyPatchTransactionArtifactTargetRollbackQuarantine:
		return "target-rollback"
	default:
		return ""
	}
}

func validateApplyPatchTransactionRootedLocation(
	location *applyPatchTransactionJournalRootedLocation,
	expectedKind string,
) error {
	if location == nil {
		return fmt.Errorf("%w: rooted location", errApplyPatchTransactionJournalInvalid)
	}
	if err := validateApplyPatchTransactionCanonicalPath(location.AnchorCanonicalPath); err != nil {
		return fmt.Errorf("%w: anchor path", errApplyPatchTransactionJournalInvalid)
	}
	if !location.AnchorIdentity.valid("directory") {
		return fmt.Errorf("%w: anchor identity", errApplyPatchTransactionJournalInvalid)
	}
	if err := validateApplyPatchTransactionPrivateBasename(location.Basename); err != nil {
		return fmt.Errorf("%w: private basename", errApplyPatchTransactionJournalInvalid)
	}
	if err := validateApplyPatchTransactionRandomPrivateName(
		location.RemovalBasename,
		"remove",
	); err != nil || location.RemovalBasename == location.Basename {
		return fmt.Errorf("%w: removal basename", errApplyPatchTransactionJournalInvalid)
	}
	if location.Identity != nil && !location.Identity.valid(expectedKind) {
		return fmt.Errorf("%w: checkpoint identity", errApplyPatchTransactionJournalInvalid)
	}
	if location.Identity == nil && location.Links != 0 {
		return fmt.Errorf("%w: uncheckpointed link count", errApplyPatchTransactionJournalInvalid)
	}
	if location.RemovalAttempted && location.Identity == nil {
		return fmt.Errorf("%w: removal without identity", errApplyPatchTransactionJournalInvalid)
	}
	if location.Identity != nil && expectedKind == "regular" &&
		(location.Links == 0 || location.Links > applyPatchTransactionMaxEntries+2) {
		return fmt.Errorf("%w: checkpoint link count", errApplyPatchTransactionJournalInvalid)
	}
	if expectedKind == "directory" && location.Links != 0 {
		return fmt.Errorf("%w: directory link count", errApplyPatchTransactionJournalInvalid)
	}
	return nil
}

func validateApplyPatchTransactionForests(journal *applyPatchTransactionJournal) error {
	forestByID := make(map[string]*applyPatchTransactionJournalForest, len(journal.Forests))
	rootedNames := make(map[string]struct{})
	publicRoots := make([]string, 0, len(journal.Forests))
	for index := range journal.Artifacts {
		artifact := &journal.Artifacts[index]
		if artifact.Rooted != nil {
			rootedNames[artifact.Rooted.AnchorCanonicalPath+"\x00"+artifact.Rooted.Basename] = struct{}{}
			rootedNames[artifact.Rooted.AnchorCanonicalPath+"\x00"+
				artifact.Rooted.RemovalBasename] = struct{}{}
		}
	}
	for index := range journal.Forests {
		forest := &journal.Forests[index]
		if !validApplyPatchTransactionHex(forest.ID, applyPatchTransactionIDHexBytes) {
			return fmt.Errorf("%w: forest ID", errApplyPatchTransactionJournalInvalid)
		}
		if _, duplicate := forestByID[forest.ID]; duplicate {
			return fmt.Errorf("%w: duplicate forest", errApplyPatchTransactionJournalInvalid)
		}
		forestByID[forest.ID] = forest
		if err := validateApplyPatchTransactionCanonicalPath(forest.PublicRoot); err != nil {
			return fmt.Errorf("%w: forest root", errApplyPatchTransactionJournalInvalid)
		}
		if applyPatchPathWithinExact(forest.PublicRoot, journal.State.CanonicalRoot) {
			return fmt.Errorf("%w: protected forest", errApplyPatchTransactionJournalInvalid)
		}
		for _, priorRoot := range publicRoots {
			if applyPatchPathsOverlap(forest.PublicRoot, priorRoot) {
				return fmt.Errorf("%w: overlapping forest roots", errApplyPatchTransactionJournalInvalid)
			}
		}
		publicRoots = append(publicRoots, forest.PublicRoot)
		if forest.OperationIndexes == nil || len(forest.OperationIndexes) == 0 ||
			forest.Entries == nil || len(forest.Entries) == 0 {
			return fmt.Errorf("%w: forest collection", errApplyPatchTransactionJournalInvalid)
		}
		forestAnchor := filepath.Dir(forest.PublicRoot)
		if forest.StageRoot.AnchorCanonicalPath != forestAnchor ||
			forest.RollbackRoot.AnchorCanonicalPath != forestAnchor ||
			forest.SentinelWitness.AnchorCanonicalPath != forestAnchor ||
			!forest.StageRoot.AnchorIdentity.equal(forest.RollbackRoot.AnchorIdentity) ||
			!forest.StageRoot.AnchorIdentity.equal(forest.SentinelWitness.AnchorIdentity) {
			return fmt.Errorf("%w: forest anchor binding", errApplyPatchTransactionJournalInvalid)
		}
		for location, properties := range map[*applyPatchTransactionJournalRootedLocation]struct {
			kind   string
			prefix string
		}{
			&forest.StageRoot:       {kind: "directory", prefix: "forest-stage"},
			&forest.RollbackRoot:    {kind: "directory", prefix: "forest-rollback"},
			&forest.SentinelWitness: {kind: "regular", prefix: "forest-witness"},
		} {
			if err := validateApplyPatchTransactionRootedLocation(location, properties.kind); err != nil {
				return err
			}
			if err := validateApplyPatchTransactionRandomPrivateName(
				location.Basename,
				properties.prefix,
			); err != nil {
				return fmt.Errorf("%w: forest random name", errApplyPatchTransactionJournalInvalid)
			}
			if applyPatchPathWithinExact(
				filepath.Join(location.AnchorCanonicalPath, location.Basename),
				journal.State.CanonicalRoot,
			) {
				return fmt.Errorf("%w: protected forest artifact", errApplyPatchTransactionJournalInvalid)
			}
			nameKey := location.AnchorCanonicalPath + "\x00" + location.Basename
			if _, duplicate := rootedNames[nameKey]; duplicate {
				return fmt.Errorf("%w: duplicate forest name", errApplyPatchTransactionJournalInvalid)
			}
			rootedNames[nameKey] = struct{}{}
			removalKey := location.AnchorCanonicalPath + "\x00" + location.RemovalBasename
			if _, duplicate := rootedNames[removalKey]; duplicate {
				return fmt.Errorf("%w: duplicate forest removal name", errApplyPatchTransactionJournalInvalid)
			}
			rootedNames[removalKey] = struct{}{}
		}
		if err := validateApplyPatchTransactionForestEntries(journal, forest); err != nil {
			return err
		}
		if forest.StageRoot.Identity != nil && forest.Entries[0].Identity != nil &&
			!forest.StageRoot.Identity.equal(*forest.Entries[0].Identity) {
			return fmt.Errorf("%w: forest root identity", errApplyPatchTransactionJournalInvalid)
		}
		if journal.Phase != applyPatchTransactionPhasePreparing {
			if forest.StageRoot.Identity == nil || forest.SentinelWitness.Identity == nil ||
				forest.SentinelWitness.Links != 2 {
				return fmt.Errorf("%w: uncheckpointed forest", errApplyPatchTransactionJournalInvalid)
			}
			for entryIndex := range forest.Entries {
				entry := &forest.Entries[entryIndex]
				if entry.Identity == nil || entry.Kind == "file" &&
					(entry.RelativePath == forest.SentinelRelativePath && entry.Links != 2 ||
						entry.RelativePath != forest.SentinelRelativePath && entry.Links != 1) {
					return fmt.Errorf("%w: uncheckpointed forest entry", errApplyPatchTransactionJournalInvalid)
				}
			}
		}
	}
	for index := range journal.Operations {
		op := &journal.Operations[index]
		if op.ForestID == "" {
			continue
		}
		forest, found := forestByID[op.ForestID]
		if !found || !containsApplyPatchTransactionIndex(forest.OperationIndexes, index) {
			return fmt.Errorf("%w: operation forest", errApplyPatchTransactionJournalInvalid)
		}
		if op.Kind != "add" && op.Kind != "move" {
			return fmt.Errorf("%w: forest operation kind", errApplyPatchTransactionJournalInvalid)
		}
	}
	return nil
}

func validateApplyPatchTransactionForestEntries(
	journal *applyPatchTransactionJournal,
	forest *applyPatchTransactionJournalForest,
) error {
	seenOperations := make(map[int]struct{}, len(forest.OperationIndexes))
	previousOperation := -1
	for _, operationIndex := range forest.OperationIndexes {
		if operationIndex <= previousOperation || operationIndex < 0 ||
			operationIndex >= journal.OperationCount {
			return fmt.Errorf("%w: forest operation order", errApplyPatchTransactionJournalInvalid)
		}
		previousOperation = operationIndex
		op := &journal.Operations[operationIndex]
		if op.ForestID != forest.ID || op.Target == nil {
			return fmt.Errorf("%w: forest operation binding", errApplyPatchTransactionJournalInvalid)
		}
		seenOperations[operationIndex] = struct{}{}
	}
	if forest.Entries[0].RelativePath != "." || forest.Entries[0].Kind != "directory" ||
		forest.Entries[0].CanonicalPath != forest.PublicRoot {
		return fmt.Errorf("%w: forest root entry", errApplyPatchTransactionJournalInvalid)
	}
	seenPaths := make(map[string]struct{}, len(forest.Entries))
	seenDirectories := map[string]struct{}{path.Clean("."): {}}
	seenParentNames := make(map[string]map[string]struct{})
	previousPath := ""
	var sentinel *applyPatchTransactionJournalForestEntry
	for index := range forest.Entries {
		entry := &forest.Entries[index]
		if err := validateApplyPatchTransactionForestRelativePath(entry.RelativePath); err != nil {
			return fmt.Errorf("%w: forest relative path", errApplyPatchTransactionJournalInvalid)
		}
		if index > 0 && entry.RelativePath <= previousPath {
			return fmt.Errorf("%w: forest entry order", errApplyPatchTransactionJournalInvalid)
		}
		previousPath = entry.RelativePath
		if _, duplicate := seenPaths[entry.RelativePath]; duplicate {
			return fmt.Errorf("%w: duplicate forest entry", errApplyPatchTransactionJournalInvalid)
		}
		seenPaths[entry.RelativePath] = struct{}{}
		if index > 0 {
			parent := path.Dir(entry.RelativePath)
			if _, found := seenDirectories[parent]; !found {
				return fmt.Errorf("%w: forest parent manifest", errApplyPatchTransactionJournalInvalid)
			}
			if err := validateApplyPatchTransactionRandomPrivateName(
				entry.RemovalBasename,
				"remove",
			); err != nil {
				return fmt.Errorf("%w: forest entry removal name", errApplyPatchTransactionJournalInvalid)
			}
			names := seenParentNames[parent]
			if names == nil {
				names = make(map[string]struct{})
				seenParentNames[parent] = names
			}
			for _, name := range []string{
				path.Base(entry.RelativePath),
				entry.RemovalBasename,
			} {
				if _, duplicate := names[name]; duplicate {
					return fmt.Errorf("%w: duplicate forest child/removal name", errApplyPatchTransactionJournalInvalid)
				}
				names[name] = struct{}{}
			}
		} else if entry.RemovalBasename != "" {
			return fmt.Errorf("%w: forest root removal name", errApplyPatchTransactionJournalInvalid)
		}
		if entry.RemovalAttempted && entry.Identity == nil {
			return fmt.Errorf("%w: forest removal without identity", errApplyPatchTransactionJournalInvalid)
		}
		if err := validateApplyPatchTransactionCanonicalPath(entry.CanonicalPath); err != nil {
			return fmt.Errorf("%w: forest entry path", errApplyPatchTransactionJournalInvalid)
		}
		relative, err := filepath.Rel(forest.PublicRoot, entry.CanonicalPath)
		if err != nil || filepath.ToSlash(relative) != entry.RelativePath {
			return fmt.Errorf("%w: forest path binding", errApplyPatchTransactionJournalInvalid)
		}
		if entry.Mode&^uint32(0o777) != 0 {
			return fmt.Errorf("%w: forest entry mode", errApplyPatchTransactionJournalInvalid)
		}
		switch entry.Kind {
		case "directory":
			if entry.OperationIndex != nil || entry.Length != 0 || entry.SHA256 != "" ||
				entry.Links != 0 {
				return fmt.Errorf("%w: forest directory entry", errApplyPatchTransactionJournalInvalid)
			}
			if entry.Identity != nil && !entry.Identity.valid("directory") {
				return fmt.Errorf("%w: forest directory identity", errApplyPatchTransactionJournalInvalid)
			}
			seenDirectories[entry.RelativePath] = struct{}{}
		case "file":
			if entry.OperationIndex == nil ||
				!validApplyPatchTransactionHex(entry.SHA256, applyPatchTransactionDigestHexBytes) {
				return fmt.Errorf("%w: forest file entry", errApplyPatchTransactionJournalInvalid)
			}
			opIndex := *entry.OperationIndex
			if _, found := seenOperations[opIndex]; !found {
				return fmt.Errorf("%w: forest file operation", errApplyPatchTransactionJournalInvalid)
			}
			op := &journal.Operations[opIndex]
			if op.Target.CanonicalPath != entry.CanonicalPath ||
				op.After.Length != entry.Length || op.After.SHA256 != entry.SHA256 ||
				op.After.Mode != entry.Mode {
				return fmt.Errorf("%w: forest file state", errApplyPatchTransactionJournalInvalid)
			}
			delete(seenOperations, opIndex)
			if entry.Identity != nil && !entry.Identity.valid("regular") {
				return fmt.Errorf("%w: forest file identity", errApplyPatchTransactionJournalInvalid)
			}
			if entry.Identity == nil && entry.Links != 0 ||
				entry.Identity != nil &&
					(entry.Links == 0 || entry.Links > applyPatchTransactionMaxEntries+2) {
				return fmt.Errorf("%w: forest file link count", errApplyPatchTransactionJournalInvalid)
			}
		default:
			return fmt.Errorf("%w: forest entry kind", errApplyPatchTransactionJournalInvalid)
		}
		if entry.RelativePath == forest.SentinelRelativePath {
			sentinel = entry
		}
	}
	if len(seenOperations) != 0 || sentinel == nil || sentinel.Kind != "file" {
		return fmt.Errorf("%w: forest sentinel", errApplyPatchTransactionJournalInvalid)
	}
	if sentinel.Identity != nil && forest.SentinelWitness.Identity != nil &&
		(!sentinel.Identity.equal(*forest.SentinelWitness.Identity) ||
			sentinel.Links != forest.SentinelWitness.Links) {
		return fmt.Errorf("%w: forest sentinel identity", errApplyPatchTransactionJournalInvalid)
	}
	return nil
}

func validateApplyPatchTransactionBackupRecord(
	record applyPatchTransactionBackupRecord,
) error {
	if record.Length > applyPatchTransactionMaxBackupBytes ||
		!validApplyPatchTransactionHex(record.SHA256, applyPatchTransactionDigestHexBytes) ||
		!validApplyPatchTransactionHex(record.HMACSHA256, applyPatchTransactionDigestHexBytes) {
		return fmt.Errorf("%w: record", errApplyPatchTransactionBackupInvalid)
	}
	return nil
}

func validateApplyPatchTransactionKey(key []byte, invalidError error) error {
	if len(key) != applyPatchTransactionKeyBytes {
		return fmt.Errorf("%w: authentication key", invalidError)
	}
	return nil
}

func validateApplyPatchTransactionCanonicalPath(value string) error {
	if value == "" || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') ||
		!filepath.IsAbs(value) || filepath.Clean(value) != value {
		return errors.New("path is not canonical")
	}
	return nil
}

func validateApplyPatchTransactionPrivateBasename(value string) error {
	if !utf8.ValidString(value) || len(value) > 255 || validateApplyPatchTxnBasename(value) != nil {
		return errors.New("private basename is invalid")
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '.' || char == '_' || char == '-' {
			continue
		}
		return errors.New("private basename is invalid")
	}
	return nil
}

func validateApplyPatchTransactionRandomPrivateName(value, prefix string) error {
	if prefix == "" {
		return errors.New("random private name is invalid")
	}
	wantedPrefix := ".picoclaw-apply-patch-" + prefix + "-"
	if !strings.HasPrefix(value, wantedPrefix) ||
		!validApplyPatchTransactionHex(
			strings.TrimPrefix(value, wantedPrefix),
			applyPatchTransactionIDHexBytes,
		) {
		return errors.New("random private name is invalid")
	}
	return validateApplyPatchTransactionPrivateBasename(value)
}

func validateApplyPatchTransactionForestRelativePath(value string) error {
	if value == "" || len(value) > applyPatchCandidateMaxPathBytes ||
		!utf8.ValidString(value) || strings.ContainsAny(value, "\\\x00") ||
		strings.HasPrefix(value, "/") || path.Clean(value) != value ||
		value == ".." || strings.HasPrefix(value, "../") {
		return errors.New("relative path is invalid")
	}
	return nil
}

func validApplyPatchTransactionHex(value string, exactLength int) bool {
	if len(value) != exactLength {
		return false
	}
	for _, char := range value {
		if char >= '0' && char <= '9' || char >= 'a' && char <= 'f' {
			continue
		}
		return false
	}
	return true
}

func containsApplyPatchTransactionIndex(values []int, wanted int) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func validateApplyPatchTransactionJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	limits := applyPatchTransactionJSONLimits{}
	if err := scanApplyPatchTransactionJSONValue(decoder, 0, "", &limits); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains trailing data")
		}
		return err
	}
	return nil
}

type applyPatchTransactionJSONLimits struct {
	declaredEntries int
}

// scanApplyPatchTransactionJSONValue rejects structural amplification before
// encoding/json can allocate the typed operation, artifact, or manifest
// slices. The same pass also rejects duplicate keys recursively.
func scanApplyPatchTransactionJSONValue(
	decoder *json.Decoder,
	depth int,
	field string,
	limits *applyPatchTransactionJSONLimits,
) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	depth++
	if depth > applyPatchTransactionJSONMaxDepth {
		return errors.New("JSON nesting exceeds limit")
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return keyErr
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is invalid")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("JSON object contains duplicate key")
			}
			seen[key] = struct{}{}
			if err := scanApplyPatchTransactionJSONValue(decoder, depth, key, limits); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim('}') {
			if closeErr != nil {
				return closeErr
			}
			return errors.New("JSON object is not closed")
		}
	case '[':
		count := 0
		for decoder.More() {
			count++
			if err := limits.addArrayElement(field, count); err != nil {
				return err
			}
			if err := scanApplyPatchTransactionJSONValue(decoder, depth, "", limits); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim(']') {
			if closeErr != nil {
				return closeErr
			}
			return errors.New("JSON array is not closed")
		}
	default:
		return errors.New("JSON delimiter is invalid")
	}
	return nil
}

func (limits *applyPatchTransactionJSONLimits) addArrayElement(
	field string,
	count int,
) error {
	if limits == nil {
		return errors.New("JSON limits are unavailable")
	}
	switch strings.ToLower(field) {
	case "operations", "operation_indexes":
		if count > applyPatchTransactionMaxOperations {
			return errors.New("JSON operation count exceeds limit")
		}
	case "artifacts", "entries":
		limits.declaredEntries++
	case "forests":
		if count > applyPatchTransactionMaxOperations {
			return errors.New("JSON forest count exceeds limit")
		}
		limits.declaredEntries += 3
	default:
		if count > applyPatchTransactionMaxEntries {
			return errors.New("JSON array count exceeds limit")
		}
	}
	if limits.declaredEntries > applyPatchTransactionMaxEntries {
		return errors.New("JSON declared entry count exceeds limit")
	}
	return nil
}

func decodeApplyPatchTransactionJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains trailing value")
		}
		return err
	}
	return nil
}
