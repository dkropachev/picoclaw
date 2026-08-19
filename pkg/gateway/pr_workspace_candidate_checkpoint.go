package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/prworkspace"
)

const (
	prWorkspaceCandidateCheckpointVersion = 2
	prWorkspaceCandidateCheckpointMaxSize = 64 << 10
	prWorkspaceCandidateCheckpointActive  = "active"
	prWorkspaceCandidateCheckpointParked  = "parked"
)

type prWorkspaceCandidateCheckpoint struct {
	Version        int                                         `json:"version"`
	State          string                                      `json:"state"`
	WorkspaceID    string                                      `json:"workspace_id"`
	Repository     string                                      `json:"repository"`
	SourceRef      string                                      `json:"source_ref"`
	HeadSHA        string                                      `json:"head_sha"`
	CharterID      string                                      `json:"charter_id"`
	CharterHeadSHA string                                      `json:"charter_head_sha"`
	GitWorkspaceID string                                      `json:"git_workspace_id"`
	LineID         string                                      `json:"line_id"`
	Lease          gitworkspace.PinnedLineLease                `json:"lease"`
	Candidate      gitworkspace.PinnedCandidate                `json:"candidate"`
	Fence          *prworkspace.ImplementationPublicationFence `json:"fence,omitempty"`
}

type prWorkspaceCandidateCheckpointStore struct {
	root string
	mu   sync.Mutex
}

func newPRWorkspaceCandidateCheckpointStore(root string) (*prWorkspaceCandidateCheckpointStore, error) {
	if stringsTrimmed(root) == "" || !filepath.IsAbs(root) {
		return nil, errors.New("PR workspace candidate checkpoint root must be absolute")
	}
	root = filepath.Clean(root)
	if err := ensurePrivatePRWorkspaceDirectory(root); err != nil {
		return nil, fmt.Errorf("prepare PR workspace candidate checkpoints: %w", err)
	}
	return &prWorkspaceCandidateCheckpointStore{root: root}, nil
}

func (store *prWorkspaceCandidateCheckpointStore) Save(checkpoint prWorkspaceCandidateCheckpoint) error {
	if store == nil || !validPRWorkspaceCandidateCheckpointShape(checkpoint) {
		return errors.New("PR workspace candidate checkpoint is invalid")
	}
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	if len(encoded) > prWorkspaceCandidateCheckpointMaxSize {
		return errors.New("PR workspace candidate checkpoint is too large")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	path := store.path(checkpoint.WorkspaceID)
	if err = requireSafePRWorkspaceCheckpointFile(path, true); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err = fileutil.WriteFileAtomic(path, encoded, 0o600); err != nil {
		return fmt.Errorf("write PR workspace candidate checkpoint: %w", err)
	}
	if err = requireSafePRWorkspaceCheckpointFile(path, false); err != nil {
		return err
	}
	return nil
}

func (store *prWorkspaceCandidateCheckpointStore) Load(
	workspaceID string,
) (prWorkspaceCandidateCheckpoint, bool, error) {
	if store == nil || stringsTrimmed(workspaceID) == "" {
		return prWorkspaceCandidateCheckpoint{}, false, errors.New(
			"PR workspace candidate checkpoint lookup is invalid",
		)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	path := store.path(workspaceID)
	if err := requireSafePRWorkspaceCheckpointFile(path, true); err != nil {
		if os.IsNotExist(err) {
			return prWorkspaceCandidateCheckpoint{}, false, nil
		}
		return prWorkspaceCandidateCheckpoint{}, false, err
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return prWorkspaceCandidateCheckpoint{}, false, err
	}
	if len(encoded) == 0 || len(encoded) > prWorkspaceCandidateCheckpointMaxSize {
		return prWorkspaceCandidateCheckpoint{}, false, errors.New("PR workspace candidate checkpoint size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var checkpoint prWorkspaceCandidateCheckpoint
	if err = decoder.Decode(&checkpoint); err != nil {
		return prWorkspaceCandidateCheckpoint{}, false, fmt.Errorf("decode PR workspace candidate checkpoint: %w", err)
	}
	if err = requireJSONEOF(decoder); err != nil {
		return prWorkspaceCandidateCheckpoint{}, false, err
	}
	if checkpoint.WorkspaceID != workspaceID || !validPRWorkspaceCandidateCheckpointShape(checkpoint) {
		return prWorkspaceCandidateCheckpoint{}, false, errors.New(
			"PR workspace candidate checkpoint identity is invalid",
		)
	}
	return checkpoint, true, nil
}

func (store *prWorkspaceCandidateCheckpointStore) Remove(workspaceID string) error {
	if store == nil || stringsTrimmed(workspaceID) == "" {
		return errors.New("PR workspace candidate checkpoint removal is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	path := store.path(workspaceID)
	if err := requireSafePRWorkspaceCheckpointFile(path, true); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := fileutil.RemoveDurable(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove PR workspace candidate checkpoint: %w", err)
	}
	return nil
}

func (store *prWorkspaceCandidateCheckpointStore) path(workspaceID string) string {
	digest := sha256.Sum256([]byte(workspaceID))
	return filepath.Join(store.root, hex.EncodeToString(digest[:])+".json")
}

func validPRWorkspaceCandidateCheckpointShape(checkpoint prWorkspaceCandidateCheckpoint) bool {
	if checkpoint.Version != prWorkspaceCandidateCheckpointVersion ||
		(checkpoint.State != prWorkspaceCandidateCheckpointActive && checkpoint.State != prWorkspaceCandidateCheckpointParked) ||
		stringsTrimmed(checkpoint.WorkspaceID) == "" || stringsTrimmed(checkpoint.Repository) == "" ||
		stringsTrimmed(checkpoint.SourceRef) == "" || stringsTrimmed(checkpoint.HeadSHA) == "" ||
		stringsTrimmed(checkpoint.CharterID) == "" || checkpoint.CharterHeadSHA != checkpoint.HeadSHA ||
		stringsTrimmed(checkpoint.GitWorkspaceID) == "" || stringsTrimmed(checkpoint.LineID) == "" ||
		checkpoint.Lease.WorkspaceID != checkpoint.GitWorkspaceID || checkpoint.Lease.MutationEpoch <= 0 ||
		stringsTrimmed(checkpoint.Lease.Tip) == "" || stringsTrimmed(checkpoint.Lease.Tree) == "" ||
		checkpoint.Candidate.WorkspaceID != checkpoint.GitWorkspaceID ||
		checkpoint.Candidate.ParentCommit != checkpoint.Lease.Tip ||
		stringsTrimmed(checkpoint.Candidate.Tree) == "" ||
		stringsTrimmed(checkpoint.Candidate.CandidateDigest) == "" || checkpoint.Candidate.ChangedFiles < 0 {
		return false
	}
	if checkpoint.State == prWorkspaceCandidateCheckpointActive {
		return checkpoint.Fence == nil
	}
	if checkpoint.Fence == nil {
		return false
	}
	fence := checkpoint.Fence
	return fence.GitWorkspaceID == checkpoint.GitWorkspaceID && fence.LineID == checkpoint.LineID &&
		fence.LineVersion == checkpoint.Lease.Version+1 &&
		fence.MutationEpoch == checkpoint.Lease.MutationEpoch && stringsTrimmed(fence.ParkIntentID) != "" &&
		fence.BaseCommit == checkpoint.HeadSHA && stringsTrimmed(fence.Tip) != "" &&
		fence.Tree == checkpoint.Candidate.Tree
}

func requireSafePRWorkspaceCheckpointFile(path string, allowMissing bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		if allowMissing && os.IsNotExist(err) {
			return err
		}
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("PR workspace candidate checkpoint must be a private regular file")
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("PR workspace candidate checkpoint contains trailing JSON")
	}
	return fmt.Errorf("decode PR workspace candidate checkpoint trailer: %w", err)
}

func stringsTrimmed(value string) string {
	for len(value) > 0 && (value[0] == ' ' || value[0] == '\t' || value[0] == '\r' || value[0] == '\n') {
		value = value[1:]
	}
	for len(value) > 0 {
		last := value[len(value)-1]
		if last != ' ' && last != '\t' && last != '\r' && last != '\n' {
			break
		}
		value = value[:len(value)-1]
	}
	return value
}
