package gitworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

const (
	stateVersion                    = 4
	historyLimit                    = 1000
	inventoryLockFile               = "inventory.lock"
	maxPinnedGitMetadataEntries     = 1_000_000
	maxPinnedGitPackMetadataEntries = 100_000
)

var errLegacyPinnedWorkspaceMigration = errors.New(
	"git workspace inventory requires version-1 cleanup before upgrade",
)

var errFreshWorkspaceDirty = errors.New("fresh git workspace is not clean")

type Options struct {
	RootDir             string
	MaxTotalSizeBytes   int64
	IgnoredCleanupDelay time.Duration
	DropDelay           time.Duration
	Now                 func() time.Time
}

type Manager struct {
	rootDir          string
	checkoutRoot     string
	lockRoot         string
	rootIdentity     fs.FileInfo
	checkoutIdentity fs.FileInfo
	lockIdentity     fs.FileInfo
	opts             Options
	now              func() time.Time
	mu               sync.Mutex

	// pinnedLinePushTransport is an internal transport seam for hermetic tests.
	// Production constructors leave it nil, so push targets remain the exact
	// canonical SCP repository stored in inventory.
	pinnedLinePushTransport func(repository string) (string, error)
}

type AcquireRequest struct {
	Repository string
	Ref        string
	Fresh      bool
	SessionKey string
	AgentID    string
}

// PinnedAcquireRequest reserves one checkout at an exact provider-observed
// branch tip. It is intentionally separate from the agent-facing generic
// acquire path: callers must already own the trusted repository, branch, and
// commit selection.
type PinnedAcquireRequest struct {
	Repository     string
	SourceRef      string
	ExpectedCommit string
	ReservationKey string
	AgentID        string
}

type PinnedReleaseRequest struct {
	ReservationKey string
	AgentID        string
}

type ReleaseRequest struct {
	SessionKey string
	AgentID    string
}

type RepositoryRecord struct {
	ID           string    `json:"id"`
	RemoteURL    string    `json:"remote_url"`
	FirstSeenAt  time.Time `json:"first_seen_at"`
	LastSeenAt   time.Time `json:"last_seen_at"`
	LastWorkAt   time.Time `json:"last_work_at,omitempty"`
	WorkspaceIDs []string  `json:"workspace_ids,omitempty"`
}

type LockInfo struct {
	SessionKey  string    `json:"session_key"`
	AgentID     string    `json:"agent_id,omitempty"`
	LockedAt    time.Time `json:"locked_at"`
	HeartbeatAt time.Time `json:"heartbeat_at"`
}

type WorkspaceRecord struct {
	ID                                string     `json:"id"`
	RepoID                            string     `json:"repo_id"`
	RemoteURL                         string     `json:"remote_url"`
	UpstreamURL                       string     `json:"upstream_url,omitempty"`
	FreshSnapshot                     bool       `json:"fresh_snapshot,omitempty"`
	Ref                               string     `json:"ref,omitempty"`
	PinnedSourceRef                   string     `json:"pinned_source_ref,omitempty"`
	PinnedCommit                      string     `json:"pinned_commit,omitempty"`
	Path                              string     `json:"path"`
	CreatedAt                         time.Time  `json:"created_at"`
	UpdatedAt                         time.Time  `json:"updated_at"`
	LastWorkAt                        time.Time  `json:"last_work_at,omitempty"`
	LastCleanedAt                     time.Time  `json:"last_cleaned_at,omitempty"`
	PreservedBranch                   string     `json:"preserved_branch,omitempty"`
	DevelopmentLineID                 string     `json:"development_line_id,omitempty"`
	PinnedReservationRotationCount    int        `json:"pinned_reservation_rotation_count"`
	PinnedReservationRotationTailHash string     `json:"pinned_reservation_rotation_tail_hash"`
	LockedBy                          *LockInfo  `json:"locked_by,omitempty"`
	DroppedAt                         *time.Time `json:"dropped_at,omitempty"`
}

type HistoryEntry struct {
	ID          string    `json:"id"`
	Time        time.Time `json:"time"`
	Action      string    `json:"action"`
	RepoID      string    `json:"repo_id,omitempty"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	SessionKey  string    `json:"session_key,omitempty"`
	AgentID     string    `json:"agent_id,omitempty"`
	Detail      string    `json:"detail,omitempty"`
}

type RepositoryInfo struct {
	ID             string    `json:"id"`
	RemoteURL      string    `json:"remote_url"`
	FirstSeenAt    time.Time `json:"first_seen_at"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	LastWorkAt     time.Time `json:"last_work_at,omitempty"`
	WorkspaceCount int       `json:"workspace_count"`
	LockedCount    int       `json:"locked_count"`
	SizeBytes      int64     `json:"size_bytes"`
	IgnoredBytes   int64     `json:"ignored_bytes"`
}

type WorkspaceInfo struct {
	ID              string     `json:"id"`
	RepoID          string     `json:"repo_id"`
	RemoteURL       string     `json:"remote_url"`
	UpstreamURL     string     `json:"upstream_url,omitempty"`
	Ref             string     `json:"ref,omitempty"`
	Path            string     `json:"path"`
	CurrentBranch   string     `json:"current_branch,omitempty"`
	PreservedBranch string     `json:"preserved_branch,omitempty"`
	Dirty           bool       `json:"dirty"`
	SizeBytes       int64      `json:"size_bytes"`
	IgnoredBytes    int64      `json:"ignored_bytes"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	LastWorkAt      time.Time  `json:"last_work_at,omitempty"`
	LastCleanedAt   time.Time  `json:"last_cleaned_at,omitempty"`
	LockedBy        *LockInfo  `json:"locked_by,omitempty"`
	DroppedAt       *time.Time `json:"dropped_at,omitempty"`
	Status          string     `json:"status"`
}

type Stats struct {
	RootDir                    string           `json:"root_dir"`
	MaxTotalSizeBytes          int64            `json:"max_total_size_bytes"`
	IgnoredCleanupDelaySeconds int64            `json:"ignored_cleanup_delay_seconds"`
	DropDelaySeconds           int64            `json:"drop_delay_seconds"`
	TotalSizeBytes             int64            `json:"total_size_bytes"`
	IgnoredBytes               int64            `json:"ignored_bytes"`
	RepositoryCount            int              `json:"repository_count"`
	WorkspaceCount             int              `json:"workspace_count"`
	LockedWorkspaceCount       int              `json:"locked_workspace_count"`
	Repositories               []RepositoryInfo `json:"repositories"`
	Workspaces                 []WorkspaceInfo  `json:"workspaces"`
	History                    []HistoryEntry   `json:"history"`
}

type CleanupResult struct {
	Workspace WorkspaceInfo `json:"workspace"`
	Before    int64         `json:"before_ignored_bytes"`
	After     int64         `json:"after_ignored_bytes"`
}

type ReconcileResult struct {
	Cleaned []WorkspaceInfo `json:"cleaned"`
	Dropped []WorkspaceInfo `json:"dropped"`
	Stats   Stats           `json:"stats"`
}

type storeState struct {
	Version                    int                                          `json:"version"`
	Repositories               map[string]*RepositoryRecord                 `json:"repositories"`
	Workspaces                 map[string]*WorkspaceRecord                  `json:"workspaces"`
	DevelopmentLines           map[string]*developmentLineRecord            `json:"development_lines,omitempty"`
	PinnedReservationRotations map[string][]pinnedReservationRotationRecord `json:"pinned_reservation_rotations,omitempty"`
	History                    []HistoryEntry                               `json:"history,omitempty"`
	DevelopmentLineHistory     []HistoryEntry                               `json:"development_line_history,omitempty"`
	generation                 int64                                        `json:"-"`
}

// Version 2 and later use a string discriminator on disk. Older binaries
// decode this field as an int, so they fail before rewriting an inventory that
// contains rollback-fenced controller state. A version-2 binary parses the
// version-3 string but rejects it against its compiled maximum before it can
// ignore and rewrite reservation-rotation evidence. A version-3 binary likewise
// rejects version 4 before it can discard suspended-line evidence. Numeric
// versions remain accepted only for the legacy version-0/version-1 migration
// path.
func (state storeState) MarshalJSON() ([]byte, error) {
	type inventoryWire struct {
		Version                    string                                       `json:"version"`
		Repositories               map[string]*RepositoryRecord                 `json:"repositories"`
		Workspaces                 map[string]*WorkspaceRecord                  `json:"workspaces"`
		DevelopmentLines           map[string]*developmentLineRecord            `json:"development_lines,omitempty"`
		PinnedReservationRotations map[string][]pinnedReservationRotationRecord `json:"pinned_reservation_rotations,omitempty"`
		History                    []HistoryEntry                               `json:"history,omitempty"`
		DevelopmentLineHistory     []HistoryEntry                               `json:"development_line_history,omitempty"`
	}
	return json.Marshal(inventoryWire{
		Version:                    strconv.Itoa(state.Version),
		Repositories:               state.Repositories,
		Workspaces:                 state.Workspaces,
		DevelopmentLines:           state.DevelopmentLines,
		PinnedReservationRotations: state.PinnedReservationRotations,
		History:                    state.History,
		DevelopmentLineHistory:     state.DevelopmentLineHistory,
	})
}

func (state *storeState) UnmarshalJSON(data []byte) error {
	if state == nil {
		return errors.New("git workspace inventory target is nil")
	}
	type inventoryWire struct {
		Version                    json.RawMessage                              `json:"version"`
		Repositories               map[string]*RepositoryRecord                 `json:"repositories"`
		Workspaces                 map[string]*WorkspaceRecord                  `json:"workspaces"`
		DevelopmentLines           map[string]*developmentLineRecord            `json:"development_lines,omitempty"`
		PinnedReservationRotations map[string][]pinnedReservationRotationRecord `json:"pinned_reservation_rotations,omitempty"`
		History                    []HistoryEntry                               `json:"history,omitempty"`
		DevelopmentLineHistory     []HistoryEntry                               `json:"development_line_history,omitempty"`
	}
	var wire inventoryWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	version := 0
	if len(wire.Version) > 0 {
		if wire.Version[0] == '"' {
			var tagged string
			if err := json.Unmarshal(wire.Version, &tagged); err != nil {
				return fmt.Errorf("decode tagged inventory version: %w", err)
			}
			parsed, err := strconv.Atoi(tagged)
			if err != nil || parsed < 2 || tagged != strconv.Itoa(parsed) {
				return errors.New("git workspace inventory version tag is invalid")
			}
			version = parsed
		} else {
			if err := json.Unmarshal(wire.Version, &version); err != nil {
				return fmt.Errorf("decode legacy inventory version: %w", err)
			}
			if version > 1 {
				return errors.New(
					"git workspace inventory version 2 or later must use its rollback fence",
				)
			}
		}
	}
	state.Version = version
	state.Repositories = wire.Repositories
	state.Workspaces = wire.Workspaces
	state.DevelopmentLines = wire.DevelopmentLines
	state.PinnedReservationRotations = wire.PinnedReservationRotations
	state.History = wire.History
	state.DevelopmentLineHistory = wire.DevelopmentLineHistory
	return nil
}

func NewManager(opts Options) (*Manager, error) {
	root := strings.TrimSpace(opts.RootDir)
	if root == "" {
		return nil, errors.New("git workspace root is required")
	}
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, fmt.Errorf("resolve absolute git workspace root: %w", err)
	}
	if mkdirErr := os.MkdirAll(root, 0o700); mkdirErr != nil {
		return nil, fmt.Errorf("create git workspace root: %w", mkdirErr)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve git workspace root: %w", err)
	}
	root = filepath.Clean(root)
	rootIdentity, err := os.Lstat(root)
	if err != nil || rootIdentity.Mode()&os.ModeSymlink != 0 || !rootIdentity.IsDir() {
		return nil, errors.New("git workspace root is not a real directory")
	}
	rootIdentity, err = fileutil.SecurePrivateDirectory(root)
	if err != nil || !managedDirectoryModePrivate(root, rootIdentity) {
		return nil, errors.New("git workspace root is not private")
	}
	checkoutRoot := filepath.Join(root, "checkouts")
	checkoutRootIdentity, err := preparePrivateManagedDirectory(
		checkoutRoot, "git workspace checkout root",
	)
	if err != nil {
		return nil, err
	}
	lockRoot := filepath.Join(root, pinnedOperationLockDirectory)
	lockRootIdentity, err := preparePrivateManagedDirectory(
		lockRoot, "git workspace operation lock root",
	)
	if err != nil {
		return nil, err
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	manager := &Manager{
		rootDir:          root,
		checkoutRoot:     checkoutRoot,
		lockRoot:         lockRoot,
		rootIdentity:     rootIdentity,
		checkoutIdentity: checkoutRootIdentity,
		lockIdentity:     lockRootIdentity,
		opts:             opts,
		now:              now,
	}
	database, openErr := manager.openInventoryDatabase(context.Background())
	if openErr != nil {
		return nil, openErr
	}
	if closeErr := database.Close(); closeErr != nil {
		return nil, fmt.Errorf("close git workspace inventory: %w", closeErr)
	}
	return manager, nil
}

func preparePrivateManagedDirectory(path, label string) (fs.FileInfo, error) {
	if err := os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
		return nil, fmt.Errorf("create %s: %w", label, err)
	}
	identity, err := os.Lstat(path)
	if err != nil || identity.Mode()&os.ModeSymlink != 0 || !identity.IsDir() {
		return nil, fmt.Errorf("%s is not a real directory", label)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", label, err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return nil, fmt.Errorf("resolve absolute %s: %w", label, err)
	}
	if filepath.Clean(canonical) != filepath.Clean(path) {
		return nil, fmt.Errorf("%s is not canonical", label)
	}
	secured, err := fileutil.SecurePrivateDirectory(path)
	if err != nil || !os.SameFile(identity, secured) || !managedDirectoryModePrivate(path, secured) {
		return nil, fmt.Errorf("%s changed while being secured", label)
	}
	return secured, nil
}

func (m *Manager) RootDir() string {
	if m == nil {
		return ""
	}
	return m.rootDir
}

func (m *Manager) Acquire(ctx context.Context, req AcquireRequest) (WorkspaceInfo, error) {
	if m == nil {
		return WorkspaceInfo{}, errors.New("git workspace manager is not configured")
	}
	repo, err := normalizeRepository(req.Repository)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	ref, err := normalizeGenericAcquireRef(ctx, req.Ref)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	sessionKey := strings.TrimSpace(req.SessionKey)
	if sessionKey == "" {
		return WorkspaceInfo{}, errors.New("session key is required to lock a git workspace")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := m.lockInventory(ctx)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	defer unlock()

	st, err := m.loadLocked()
	if err != nil {
		return WorkspaceInfo{}, err
	}
	now := m.now().UTC()
	repoID := repoID(repo)
	repoRec := st.Repositories[repoID]
	if repoRec == nil {
		repoRec = &RepositoryRecord{
			ID:          repoID,
			RemoteURL:   repo,
			FirstSeenAt: now,
		}
		st.Repositories[repoID] = repoRec
	} else if repoRec.RemoteURL != repo {
		return WorkspaceInfo{}, errors.New("git workspace repository identity collision")
	}
	repoRec.LastSeenAt = now

	if ws := m.findSessionWorkspaceLocked(st, repoID, sessionKey); ws != nil {
		if ws.PinnedSourceRef != "" || ws.PinnedCommit != "" {
			return WorkspaceInfo{}, errors.New(
				"pinned git workspace reservation requires AcquirePinned",
			)
		}
		if ws.Ref != ref {
			return WorkspaceInfo{}, errors.New(
				"session already holds a git workspace for a different ref; release it before acquiring another ref",
			)
		}
		ws.LockedBy.HeartbeatAt = now
		ws.UpdatedAt = now
		m.addHistoryLocked(st, now, "heartbeat", repoID, ws.ID, sessionKey, req.AgentID, "")
		if saveErr := m.saveLocked(st); saveErr != nil {
			return WorkspaceInfo{}, saveErr
		}
		return m.workspaceInfo(ctx, ws)
	}

	var ws *WorkspaceRecord
	if req.Fresh {
		ws = m.findFreshReusableWorkspaceLocked(st, repoID)
		if ws != nil {
			if recloneErr := m.recloneFreshWorkspaceLocked(
				ctx, st, ws, repoRec, repo, ref, now,
			); recloneErr != nil {
				if !errors.Is(recloneErr, errFreshWorkspaceDirty) {
					return WorkspaceInfo{}, recloneErr
				}
				ws.FreshSnapshot = false
				ws = nil
			}
		}
	} else {
		ws = m.findReusableWorkspaceLocked(st, repoID, ref)
	}
	if ws == nil {
		ws, err = m.createWorkspaceLocked(ctx, st, repoRec, repo, ref, now)
		if err != nil {
			return WorkspaceInfo{}, err
		}
	}
	if req.Fresh {
		ws.FreshSnapshot = true
	}

	ws.LockedBy = &LockInfo{
		SessionKey:  sessionKey,
		AgentID:     strings.TrimSpace(req.AgentID),
		LockedAt:    now,
		HeartbeatAt: now,
	}
	ws.PinnedSourceRef = ""
	ws.PinnedCommit = ""
	ws.DroppedAt = nil
	ws.UpdatedAt = now
	repoRec.LastWorkAt = now
	m.addHistoryLocked(st, now, "allocated", repoID, ws.ID, sessionKey, req.AgentID, ws.Path)

	if err := m.saveLocked(st); err != nil {
		return WorkspaceInfo{}, err
	}
	return m.workspaceInfo(ctx, ws)
}

// AcquirePinned reserves a fresh checkout whose fetched source branch resolves
// to ExpectedCommit. The checkout is cleanly detached at that commit before
// its lock is persisted. Reacquiring the same reservation only
// heartbeats it: existing dirty or descendant work is retained, but repository,
// commit, agent, origin, and ancestry must still match exactly.
//
// This capability is for trusted controllers. The git_workspace agent tool
// deliberately exposes only Acquire.
func (m *Manager) AcquirePinned(
	ctx context.Context,
	req PinnedAcquireRequest,
) (WorkspaceInfo, error) {
	if m == nil {
		return WorkspaceInfo{}, errors.New("git workspace manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repo, err := validatePinnedAcquireRequest(ctx, req)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	sourceRef := req.SourceRef
	expectedCommit := req.ExpectedCommit
	reservationKey := req.ReservationKey
	agentID := req.AgentID

	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := m.lockInventory(ctx)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	defer unlock()

	st, err := m.loadLocked()
	if err != nil {
		return WorkspaceInfo{}, err
	}
	reservationHash := developmentLineReservationHash(reservationKey)
	owned, duplicate := findPinnedReservationWorkspaceLocked(st, reservationKey)
	if duplicate {
		return WorkspaceInfo{}, errors.New("pinned reservation owns multiple git workspaces")
	}
	if pinnedReservationRotationRevoked(st, reservationHash) ||
		(pinnedReservationRotationHashUsed(st, reservationHash) && owned == nil) {
		return WorkspaceInfo{}, errors.New(
			"pinned reservation was released by a reservation rotation",
		)
	}
	for _, line := range st.DevelopmentLines {
		if line == nil {
			continue
		}
		if line.PendingParkSet && line.MutationReservationHash == reservationHash {
			return WorkspaceInfo{}, errors.New(
				"pinned reservation is sealed by a pending development line park",
			)
		}
		if developmentLineReservationRetired(line, reservationHash) &&
			line.MutationReservationHash != reservationHash {
			return WorkspaceInfo{}, errors.New(
				"pinned reservation was released from a development line",
			)
		}
	}
	pinnedEnvironment, cleanupPinnedEnvironment, err := m.newPinnedGitEnvironment()
	if err != nil {
		return WorkspaceInfo{}, err
	}
	defer cleanupPinnedEnvironment()
	now := m.now().UTC()
	repositoryID := repoID(repo)

	if owned != nil {
		if owned.RepoID != repositoryID || owned.RemoteURL != repo ||
			owned.Ref != sourceRef || owned.PinnedSourceRef != sourceRef ||
			owned.PinnedCommit != expectedCommit || owned.LockedBy == nil ||
			owned.LockedBy.AgentID != agentID {
			return WorkspaceInfo{}, errors.New("pinned workspace reservation does not match request")
		}
		if verifyErr := m.verifyPinnedWorkspace(
			ctx,
			owned,
			repo,
			expectedCommit,
			false,
			pinnedEnvironment,
		); verifyErr != nil {
			return WorkspaceInfo{}, verifyErr
		}
		owned.LockedBy.HeartbeatAt = now
		owned.UpdatedAt = now
		if repository := st.Repositories[repositoryID]; repository != nil {
			repository.LastSeenAt = now
			repository.LastWorkAt = now
		}
		m.addHistoryLocked(
			st,
			now,
			"pinned_heartbeat",
			repositoryID,
			owned.ID,
			reservationKey,
			agentID,
			expectedCommit,
		)
		if saveErr := m.saveLocked(st); saveErr != nil {
			return WorkspaceInfo{}, saveErr
		}
		return m.workspaceInfo(ctx, owned)
	}

	repository := st.Repositories[repositoryID]
	if repository == nil {
		repository = &RepositoryRecord{
			ID:          repositoryID,
			RemoteURL:   repo,
			FirstSeenAt: now,
		}
		st.Repositories[repositoryID] = repository
	} else if repository.RemoteURL != repo {
		return WorkspaceInfo{}, errors.New("pinned repository identity collision")
	}
	repository.LastSeenAt = now

	workspace, err := m.createPinnedWorkspaceLocked(
		ctx,
		st,
		repository,
		repo,
		sourceRef,
		expectedCommit,
		now,
		pinnedEnvironment,
	)
	if err != nil {
		return WorkspaceInfo{}, err
	}

	workspace.Ref = sourceRef
	workspace.PinnedSourceRef = sourceRef
	workspace.PinnedCommit = expectedCommit
	workspace.LockedBy = &LockInfo{
		SessionKey:  reservationKey,
		AgentID:     agentID,
		LockedAt:    now,
		HeartbeatAt: now,
	}
	workspace.DroppedAt = nil
	workspace.UpdatedAt = now
	repository.LastWorkAt = now
	m.addHistoryLocked(
		st,
		now,
		"pinned_allocated",
		repositoryID,
		workspace.ID,
		reservationKey,
		agentID,
		sourceRef+"@"+expectedCommit,
	)
	if err := m.saveLocked(st); err != nil {
		return WorkspaceInfo{}, err
	}
	return m.workspaceInfo(ctx, workspace)
}

func (m *Manager) ReleaseSession(ctx context.Context, req ReleaseRequest) ([]WorkspaceInfo, error) {
	if m == nil {
		return nil, errors.New("git workspace manager is not configured")
	}
	sessionKey := strings.TrimSpace(req.SessionKey)
	if sessionKey == "" {
		return nil, errors.New("session key is required")
	}
	return m.releaseReservations(ctx, sessionKey, strings.TrimSpace(req.AgentID), false)
}

// ReleasePinned preserves and unlocks only a controller-owned pinned
// reservation. Generic session release deliberately skips pinned workspaces so
// agent tool calls and turn finalization cannot release controller state.
func (m *Manager) ReleasePinned(
	ctx context.Context,
	req PinnedReleaseRequest,
) ([]WorkspaceInfo, error) {
	if m == nil {
		return nil, errors.New("git workspace manager is not configured")
	}
	reservationKey := strings.TrimSpace(req.ReservationKey)
	agentID := strings.TrimSpace(req.AgentID)
	if reservationKey != req.ReservationKey ||
		!validPinnedOperationIdentity(reservationKey, 256) {
		return nil, errors.New("pinned reservation key must be an exact bounded identity")
	}
	if agentID != req.AgentID || !validPinnedOperationIdentity(agentID, 256) {
		return nil, errors.New("pinned release agent ID must be an exact bounded identity")
	}
	_, _, releaseOperation, err := m.acquirePinnedOperation(ctx, reservationKey)
	if err != nil {
		return nil, err
	}
	defer releaseOperation()
	return m.releaseReservations(ctx, reservationKey, agentID, true)
}

func (m *Manager) releaseReservations(
	ctx context.Context,
	reservationKey, agentID string,
	pinned bool,
) ([]WorkspaceInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := m.lockInventory(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()

	st, err := m.loadLocked()
	if err != nil {
		return nil, err
	}
	if pinned {
		reservationHash := developmentLineReservationHash(reservationKey)
		owned, duplicate := findPinnedReservationWorkspaceLocked(st, reservationKey)
		if duplicate || pinnedReservationRotationRevoked(st, reservationHash) ||
			(pinnedReservationRotationHashUsed(st, reservationHash) && owned == nil) {
			return nil, errors.New(
				"pinned reservation was revoked by a reservation rotation",
			)
		}
		for _, line := range st.DevelopmentLines {
			if line != nil && (line.MutationReservationHash == reservationHash ||
				developmentLineReservationRetired(line, reservationHash)) {
				return nil, errors.New(
					"development line reservation must be parked through its controller boundary",
				)
			}
		}
		matchingReservations := 0
		for _, workspace := range st.Workspaces {
			if workspace == nil || workspace.LockedBy == nil ||
				workspace.LockedBy.SessionKey != reservationKey ||
				(workspace.PinnedSourceRef == "" && workspace.PinnedCommit == "") {
				continue
			}
			matchingReservations++
			if workspace.PinnedSourceRef == "" || workspace.PinnedCommit == "" {
				return nil, errors.New("pinned workspace release does not match its reservation")
			}
			if workspace.LockedBy.AgentID != agentID {
				return nil, errors.New(
					"pinned workspace release agent does not match its reservation",
				)
			}
		}
		if matchingReservations > 1 {
			return nil, errors.New("pinned reservation owns multiple git workspaces")
		}
	}
	now := m.now().UTC()
	var released []*WorkspaceRecord
	for _, ws := range st.Workspaces {
		if ws == nil || ws.LockedBy == nil || ws.LockedBy.SessionKey != reservationKey {
			continue
		}
		if !pinned && ws.DevelopmentLineID != "" {
			continue
		}
		workspaceIsPinned := ws.PinnedSourceRef != "" || ws.PinnedCommit != ""
		if workspaceIsPinned != pinned {
			continue
		}
		branch, changed, err := m.preserveWorkspaceLocked(ctx, ws, reservationKey, now)
		if err != nil {
			m.addHistoryLocked(
				st,
				now,
				"preserve_failed",
				ws.RepoID,
				ws.ID,
				reservationKey,
				agentID,
				err.Error(),
			)
			_ = m.saveLocked(st)
			return nil, err
		}
		if changed {
			ws.PreservedBranch = branch
			ws.FreshSnapshot = false
		}
		ws.LockedBy = nil
		ws.LastWorkAt = now
		ws.UpdatedAt = now
		if repo := st.Repositories[ws.RepoID]; repo != nil {
			repo.LastWorkAt = now
			repo.LastSeenAt = now
		}
		detail := ""
		if changed {
			detail = "preserved on " + branch
		}
		action := "released"
		if pinned {
			action = "pinned_released"
		}
		m.addHistoryLocked(st, now, action, ws.RepoID, ws.ID, reservationKey, agentID, detail)
		released = append(released, ws)
	}
	if err := m.saveLocked(st); err != nil {
		return nil, err
	}

	out := make([]WorkspaceInfo, 0, len(released))
	for _, ws := range released {
		info, err := m.workspaceInfo(ctx, ws)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

func (m *Manager) Stats(ctx context.Context) (Stats, error) {
	if m == nil {
		return Stats{}, errors.New("git workspace manager is not configured")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := m.lockInventory(ctx)
	if err != nil {
		return Stats{}, err
	}
	defer unlock()

	st, err := m.loadLocked()
	if err != nil {
		return Stats{}, err
	}
	return m.statsLocked(ctx, st)
}

func (m *Manager) CleanupIgnored(ctx context.Context, workspaceID string) (CleanupResult, error) {
	if m == nil {
		return CleanupResult{}, errors.New("git workspace manager is not configured")
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return CleanupResult{}, errors.New("workspace id is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := m.lockInventory(ctx)
	if err != nil {
		return CleanupResult{}, err
	}
	defer unlock()

	st, err := m.loadLocked()
	if err != nil {
		return CleanupResult{}, err
	}
	ws := st.Workspaces[workspaceID]
	if ws == nil || ws.DroppedAt != nil || controllerPrivateWorkspace(ws) {
		return CleanupResult{}, fmt.Errorf("git workspace %q not found", workspaceID)
	}
	if ws.LockedBy != nil {
		return CleanupResult{}, fmt.Errorf(
			"git workspace %q is locked by session %s",
			workspaceID,
			ws.LockedBy.SessionKey,
		)
	}
	before, _ := ignoredSize(ctx, ws.Path)
	if cleanErr := cleanIgnored(ctx, ws.Path); cleanErr != nil {
		return CleanupResult{}, cleanErr
	}
	now := m.now().UTC()
	ws.LastCleanedAt = now
	ws.UpdatedAt = now
	after, _ := ignoredSize(ctx, ws.Path)
	m.addHistoryLocked(
		st,
		now,
		"cleaned_ignored",
		ws.RepoID,
		ws.ID,
		"",
		"",
		fmt.Sprintf("%d -> %d bytes", before, after),
	)
	if saveErr := m.saveLocked(st); saveErr != nil {
		return CleanupResult{}, saveErr
	}
	info, err := m.workspaceInfo(ctx, ws)
	if err != nil {
		return CleanupResult{}, err
	}
	return CleanupResult{Workspace: info, Before: before, After: after}, nil
}

func (m *Manager) Drop(ctx context.Context, workspaceID string) (WorkspaceInfo, error) {
	if m == nil {
		return WorkspaceInfo{}, errors.New("git workspace manager is not configured")
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return WorkspaceInfo{}, errors.New("workspace id is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := m.lockInventory(ctx)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	defer unlock()

	st, err := m.loadLocked()
	if err != nil {
		return WorkspaceInfo{}, err
	}
	ws := st.Workspaces[workspaceID]
	if ws == nil || ws.DroppedAt != nil || controllerPrivateWorkspace(ws) {
		return WorkspaceInfo{}, fmt.Errorf("git workspace %q not found", workspaceID)
	}
	if ws.LockedBy != nil {
		return WorkspaceInfo{}, fmt.Errorf(
			"git workspace %q is locked by session %s",
			workspaceID,
			ws.LockedBy.SessionKey,
		)
	}
	if err := m.dropWorkspaceLocked(ctx, st, ws, "manual_drop"); err != nil {
		return WorkspaceInfo{}, err
	}
	if err := m.saveLocked(st); err != nil {
		return WorkspaceInfo{}, err
	}
	return m.workspaceInfo(ctx, ws)
}

func (m *Manager) Reconcile(ctx context.Context) (ReconcileResult, error) {
	if m == nil {
		return ReconcileResult{}, errors.New("git workspace manager is not configured")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := m.lockInventory(ctx)
	if err != nil {
		return ReconcileResult{}, err
	}
	defer unlock()

	st, err := m.loadLocked()
	if err != nil {
		return ReconcileResult{}, err
	}
	var cleaned []*WorkspaceRecord
	var dropped []*WorkspaceRecord
	now := m.now().UTC()

	workspaceList := sortedWorkspaceRecords(st.Workspaces)
	for _, ws := range workspaceList {
		if ws == nil || ws.DroppedAt != nil || ws.LockedBy != nil ||
			controllerPrivateWorkspace(ws) || ws.LastWorkAt.IsZero() {
			continue
		}
		if m.opts.IgnoredCleanupDelay > 0 && now.Sub(ws.LastWorkAt) >= m.opts.IgnoredCleanupDelay {
			before, _ := ignoredSize(ctx, ws.Path)
			if before > 0 {
				if cleanErr := cleanIgnored(ctx, ws.Path); cleanErr != nil {
					return ReconcileResult{}, cleanErr
				}
				ws.LastCleanedAt = now
				ws.UpdatedAt = now
				m.addHistoryLocked(
					st,
					now,
					"auto_cleaned_ignored",
					ws.RepoID,
					ws.ID,
					"",
					"",
					fmt.Sprintf("%d bytes", before),
				)
				cleaned = append(cleaned, ws)
			}
		}
	}

	for _, ws := range workspaceList {
		if ws == nil || ws.DroppedAt != nil || ws.LockedBy != nil ||
			controllerPrivateWorkspace(ws) || ws.LastWorkAt.IsZero() {
			continue
		}
		if m.opts.DropDelay > 0 && now.Sub(ws.LastWorkAt) >= m.opts.DropDelay {
			if dropErr := m.dropWorkspaceLocked(ctx, st, ws, "auto_drop_age"); dropErr != nil {
				return ReconcileResult{}, dropErr
			}
			dropped = append(dropped, ws)
		}
	}

	stats, err := m.statsLocked(ctx, st)
	if err != nil {
		return ReconcileResult{}, err
	}
	if m.opts.MaxTotalSizeBytes > 0 && stats.TotalSizeBytes > m.opts.MaxTotalSizeBytes {
		for _, ws := range workspaceList {
			if ws == nil || ws.DroppedAt != nil || ws.LockedBy != nil ||
				controllerPrivateWorkspace(ws) {
				continue
			}
			if dropErr := m.dropWorkspaceLocked(ctx, st, ws, "auto_drop_size"); dropErr != nil {
				return ReconcileResult{}, dropErr
			}
			dropped = append(dropped, ws)
			stats, err = m.statsLocked(ctx, st)
			if err != nil {
				return ReconcileResult{}, err
			}
			if stats.TotalSizeBytes <= m.opts.MaxTotalSizeBytes {
				break
			}
		}
	}

	if saveErr := m.saveLocked(st); saveErr != nil {
		return ReconcileResult{}, saveErr
	}
	stats, err = m.statsLocked(ctx, st)
	if err != nil {
		return ReconcileResult{}, err
	}
	return ReconcileResult{
		Cleaned: workspaceInfos(ctx, m, cleaned),
		Dropped: workspaceInfos(ctx, m, dropped),
		Stats:   stats,
	}, nil
}

func (m *Manager) statePath() string {
	return filepath.Join(m.rootDir, "inventory.json")
}

func (m *Manager) databasePath() string {
	return filepath.Join(m.rootDir, "inventory.db")
}

func (m *Manager) lockInventory(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := m.validateRoot(); err != nil {
		return nil, err
	}
	unlock, err := lockInventoryFile(ctx, filepath.Join(m.rootDir, inventoryLockFile))
	if err != nil {
		return nil, fmt.Errorf("lock git workspace inventory: %w", err)
	}
	return unlock, nil
}

func (m *Manager) loadLocked() (*storeState, error) {
	database, err := m.openInventoryDatabase(context.Background())
	if err != nil {
		return nil, err
	}
	defer database.Close()
	return loadInventoryState(context.Background(), database)
}

func validateGitWorkspaceInventoryVersion(version, maximum int) error {
	if version < 0 || version > maximum {
		return fmt.Errorf("unsupported git workspace inventory version %d", version)
	}
	return nil
}

func (m *Manager) migrateLegacyPinnedWorkspaces(st *storeState) error {
	if m == nil || st == nil {
		return errLegacyPinnedWorkspaceMigration
	}
	if rootErr := m.validatePinnedCheckoutRoot(); rootErr != nil {
		return errLegacyPinnedWorkspaceMigration
	}
	legacyIDs := make([]string, 0)
	for oldID, workspace := range st.Workspaces {
		if workspace == nil ||
			(workspace.PinnedSourceRef == "" && workspace.PinnedCommit == "") {
			continue
		}
		if workspace.PinnedSourceRef != "" && workspace.PinnedCommit != "" &&
			validControllerPinnedWorkspaceID(workspace.RepoID, workspace.ID) {
			continue
		}
		legacyIDs = append(legacyIDs, oldID)
	}
	sort.Strings(legacyIDs)
	for _, oldID := range legacyIDs {
		workspace := st.Workspaces[oldID]
		if workspace == nil {
			return errLegacyPinnedWorkspaceMigration
		}
		repository := st.Repositories[workspace.RepoID]
		if workspace.ID != oldID || workspace.PinnedSourceRef == "" ||
			workspace.PinnedCommit == "" ||
			len(workspace.PinnedSourceRef) > 4<<10 ||
			workspace.PinnedSourceRef != strings.TrimSpace(workspace.PinnedSourceRef) ||
			!utf8.ValidString(workspace.PinnedSourceRef) ||
			containsPinnedControlCharacter(workspace.PinnedSourceRef) ||
			workspace.Ref != workspace.PinnedSourceRef ||
			!validPinnedCommit(workspace.PinnedCommit) ||
			!validLegacyPinnedWorkspaceID(workspace.RepoID, workspace.ID) ||
			workspace.RepoID != repoID(workspace.RemoteURL) || repository == nil ||
			repository.ID != workspace.RepoID ||
			repository.RemoteURL != workspace.RemoteURL {
			return errLegacyPinnedWorkspaceMigration
		}
		workspaceIDCount := 0
		for _, workspaceID := range repository.WorkspaceIDs {
			if workspaceID == oldID {
				workspaceIDCount++
			}
		}
		if workspaceIDCount != 1 {
			return errLegacyPinnedWorkspaceMigration
		}
		if workspace.DroppedAt == nil || workspace.LockedBy != nil {
			return errLegacyPinnedWorkspaceMigration
		}
		expectedPath := filepath.Join(
			m.checkoutRoot,
			safePathName(workspace.RemoteURL)+"-"+workspace.ID,
		)
		if workspace.Path != expectedPath {
			return errLegacyPinnedWorkspaceMigration
		}
		if _, statErr := os.Lstat(expectedPath); !os.IsNotExist(statErr) {
			return errLegacyPinnedWorkspaceMigration
		}
		publicHistory := st.History[:0]
		for _, entry := range st.History {
			if entry.WorkspaceID == oldID {
				st.DevelopmentLineHistory = append(st.DevelopmentLineHistory, entry)
				continue
			}
			publicHistory = append(publicHistory, entry)
		}
		st.History = publicHistory
		delete(st.Workspaces, oldID)
		workspaceIDs := repository.WorkspaceIDs[:0]
		for _, workspaceID := range repository.WorkspaceIDs {
			if workspaceID != oldID {
				workspaceIDs = append(workspaceIDs, workspaceID)
			}
		}
		repository.WorkspaceIDs = workspaceIDs
	}
	return nil
}

func (m *Manager) saveLocked(st *storeState) error {
	if st == nil {
		return nil
	}
	if err := m.validateRoot(); err != nil {
		return err
	}
	partitionDevelopmentLineHistory(st)
	if err := validateDevelopmentLineInventory(st); err != nil {
		return err
	}
	st.Version = stateVersion
	if len(st.History) > historyLimit {
		st.History = st.History[len(st.History)-historyLimit:]
	}
	if len(st.DevelopmentLineHistory) > historyLimit {
		st.DevelopmentLineHistory = st.DevelopmentLineHistory[len(st.DevelopmentLineHistory)-historyLimit:]
	}
	database, err := m.openInventoryDatabase(context.Background())
	if err != nil {
		return err
	}
	defer database.Close()
	return saveInventoryState(context.Background(), database, st)
}

func (m *Manager) findSessionWorkspaceLocked(
	st *storeState,
	repoID, sessionKey string,
) *WorkspaceRecord {
	for _, ws := range st.Workspaces {
		if ws == nil || ws.RepoID != repoID || ws.DroppedAt != nil || ws.LockedBy == nil {
			continue
		}
		if controllerPrivateWorkspace(ws) {
			continue
		}
		if ws.LockedBy.SessionKey == sessionKey {
			return ws
		}
	}
	return nil
}

func (m *Manager) findReusableWorkspaceLocked(st *storeState, repoID, ref string) *WorkspaceRecord {
	for _, ws := range sortedWorkspaceRecords(st.Workspaces) {
		if ws == nil || ws.RepoID != repoID || ws.DroppedAt != nil || ws.LockedBy != nil {
			continue
		}
		if controllerPrivateWorkspace(ws) || ws.FreshSnapshot {
			continue
		}
		if ref == "" || ws.Ref == "" || ws.Ref == ref {
			return ws
		}
	}
	return nil
}

func (m *Manager) findFreshReusableWorkspaceLocked(st *storeState, repoID string) *WorkspaceRecord {
	for _, ws := range sortedWorkspaceRecords(st.Workspaces) {
		if ws == nil || ws.RepoID != repoID || ws.DroppedAt != nil || ws.LockedBy != nil ||
			controllerPrivateWorkspace(ws) || !ws.FreshSnapshot || ws.PreservedBranch != "" {
			continue
		}
		return ws
	}
	return nil
}

func (m *Manager) recloneFreshWorkspaceLocked(
	ctx context.Context,
	st *storeState,
	workspace *WorkspaceRecord,
	repository *RepositoryRecord,
	remoteURL, ref string,
	now time.Time,
) error {
	if workspace == nil || repository == nil || workspace.LockedBy != nil ||
		workspace.PreservedBranch != "" || !workspace.FreshSnapshot || controllerPrivateWorkspace(workspace) {
		return errors.New("fresh git workspace reuse is not safe")
	}
	status, err := runGit(ctx, workspace.Path, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil || strings.TrimSpace(status) != "" {
		return errFreshWorkspaceDirty
	}
	origins, err := runGit(ctx, workspace.Path, "remote", "get-url", "--all", "origin")
	if err != nil {
		return err
	}
	originValues := strings.Fields(strings.TrimSpace(origins))
	if len(originValues) != 1 {
		return errors.New("fresh git workspace origin is invalid")
	}
	normalizedOrigin, err := normalizeRepository(originValues[0])
	if err != nil || normalizedOrigin != remoteURL {
		return errors.New("fresh git workspace origin changed")
	}
	hooksPath, err := os.MkdirTemp(m.rootDir, ".fresh-hooks-")
	if err != nil {
		return fmt.Errorf("create fresh git workspace hooks directory: %w", err)
	}
	defer os.RemoveAll(hooksPath)
	if _, err := runGit(
		ctx, workspace.Path, "-c", "core.hooksPath="+hooksPath,
		"fetch", "--prune", "--prune-tags", "--force", "--tags", "origin",
	); err != nil {
		return err
	}
	if _, err := runGit(ctx, workspace.Path, "remote", "set-head", "origin", "--auto"); err != nil {
		return fmt.Errorf("refresh fresh git workspace default branch: %w", err)
	}
	candidates := []string{ref}
	switch {
	case ref == "" || ref == "HEAD":
		candidates = []string{"refs/remotes/origin/HEAD"}
	case strings.HasPrefix(ref, "refs/heads/"):
		candidates = []string{"refs/remotes/origin/" + strings.TrimPrefix(ref, "refs/heads/")}
	case !validPinnedCommit(strings.ToLower(ref)) && !strings.HasPrefix(ref, "refs/tags/"):
		candidates = []string{"refs/remotes/origin/" + ref, "refs/tags/" + ref}
	}
	commit := ""
	for _, candidate := range candidates {
		resolved, resolveErr := runGit(
			ctx, workspace.Path, "rev-parse", "--verify", "--end-of-options", candidate+"^{commit}",
		)
		if resolveErr == nil && validPinnedCommit(strings.TrimSpace(resolved)) {
			commit = strings.TrimSpace(resolved)
			break
		}
	}
	if commit == "" {
		return errors.New("requested fresh git workspace ref is unavailable")
	}
	if _, err := runGit(
		ctx, workspace.Path, "-c", "core.hooksPath="+hooksPath,
		"checkout", "--detach", "--force", commit,
	); err != nil {
		return err
	}
	workspace.Ref = ref
	workspace.RemoteURL = remoteURL
	workspace.UpstreamURL = localRepositoryRemoteOrigin(ctx, remoteURL)
	if err := configureFreshWorkspaceUpstream(ctx, workspace.Path, workspace.UpstreamURL); err != nil {
		return err
	}
	workspace.UpdatedAt = now
	workspace.PreservedBranch = ""
	workspace.DroppedAt = nil
	repository.LastSeenAt = now
	m.addHistoryLocked(st, now, "recloned", repository.ID, workspace.ID, "", "", workspace.Path)
	return nil
}

func findPinnedReservationWorkspaceLocked(
	st *storeState,
	reservationKey string,
) (*WorkspaceRecord, bool) {
	var found *WorkspaceRecord
	for _, workspace := range st.Workspaces {
		if workspace == nil || workspace.DroppedAt != nil || workspace.LockedBy == nil ||
			workspace.LockedBy.SessionKey != reservationKey ||
			(workspace.PinnedSourceRef == "" && workspace.PinnedCommit == "") {
			continue
		}
		if found != nil {
			return nil, true
		}
		found = workspace
	}
	return found, false
}

func validControllerPinnedWorkspaceID(repositoryID, workspaceID string) bool {
	base := repositoryID + "-pinned"
	if workspaceID == base {
		return true
	}
	suffix, found := strings.CutPrefix(workspaceID, base+"-")
	if !found {
		return false
	}
	number, err := strconv.Atoi(suffix)
	return err == nil && number >= 2 && suffix == strconv.Itoa(number)
}

func validLegacyPinnedWorkspaceID(repositoryID, workspaceID string) bool {
	if workspaceID == repositoryID {
		return true
	}
	suffix, found := strings.CutPrefix(workspaceID, repositoryID+"-")
	if !found {
		return false
	}
	number, err := strconv.Atoi(suffix)
	return err == nil && number >= 2 && suffix == strconv.Itoa(number)
}

func (m *Manager) createPinnedWorkspaceLocked(
	ctx context.Context,
	st *storeState,
	repository *RepositoryRecord,
	remoteURL, sourceRef, expectedCommit string,
	now time.Time,
	environment []string,
) (*WorkspaceRecord, error) {
	if st == nil || repository == nil {
		return nil, errors.New("pinned workspace inventory is unavailable")
	}
	if err := m.validatePinnedCheckoutRoot(); err != nil {
		return nil, err
	}
	idBase := repository.ID + "-pinned"
	id := idBase
	for suffix := 2; ; suffix++ {
		if _, exists := st.Workspaces[id]; !exists {
			break
		}
		id = fmt.Sprintf("%s-%d", idBase, suffix)
	}
	finalPath := filepath.Join(m.checkoutRoot, safePathName(remoteURL)+"-"+id)
	stagingRoot, err := os.MkdirTemp(m.checkoutRoot, ".pinned-stage-")
	if err != nil {
		return nil, fmt.Errorf("create pinned workspace staging root: %w", err)
	}
	defer os.RemoveAll(stagingRoot)
	stagedPath := filepath.Join(stagingRoot, "checkout")
	hooksPath := filepath.Join(stagingRoot, "hooks")
	if err := os.Mkdir(hooksPath, 0o700); err != nil {
		return nil, fmt.Errorf("create pinned workspace empty hooks directory: %w", err)
	}
	if _, err := runPinnedGit(
		ctx,
		"",
		environment,
		"clone",
		"--no-checkout",
		"--no-local",
		"--no-tags",
		"--origin",
		"origin",
		"--",
		remoteURL,
		stagedPath,
	); err != nil {
		return nil, fmt.Errorf("clone pinned git workspace: %w", err)
	}
	workspace := &WorkspaceRecord{
		ID:                                id,
		RepoID:                            repository.ID,
		RemoteURL:                         remoteURL,
		Ref:                               sourceRef,
		PinnedSourceRef:                   sourceRef,
		PinnedCommit:                      expectedCommit,
		Path:                              stagedPath,
		CreatedAt:                         now,
		UpdatedAt:                         now,
		PinnedReservationRotationTailHash: emptyPinnedReservationRotationDigest(),
	}
	if err := m.preparePinnedWorkspace(
		ctx,
		workspace,
		remoteURL,
		sourceRef,
		expectedCommit,
		hooksPath,
		stagingRoot,
		environment,
	); err != nil {
		return nil, err
	}
	if err := m.validatePinnedCheckoutRoot(); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(finalPath); err != nil {
		return nil, fmt.Errorf("prepare pinned workspace path: %w", err)
	}
	if err := os.Rename(stagedPath, finalPath); err != nil {
		return nil, fmt.Errorf("publish pinned workspace path: %w", err)
	}
	workspace.Path = finalPath
	if err := m.verifyPinnedWorkspace(
		ctx,
		workspace,
		remoteURL,
		expectedCommit,
		true,
		environment,
	); err != nil {
		_ = os.RemoveAll(finalPath)
		return nil, fmt.Errorf("verify published pinned workspace: %w", err)
	}
	st.Workspaces[id] = workspace
	repository.WorkspaceIDs = appendUnique(repository.WorkspaceIDs, id)
	m.addHistoryLocked(st, now, "pinned_cloned", repository.ID, id, "", "", finalPath)
	return workspace, nil
}

func (m *Manager) preparePinnedWorkspace(
	ctx context.Context,
	workspace *WorkspaceRecord,
	repository, sourceRef, expectedCommit, hooksPath, stagingRoot string,
	environment []string,
) error {
	expectedStagedPath := filepath.Join(stagingRoot, "checkout")
	if filepath.Clean(workspace.Path) != filepath.Clean(expectedStagedPath) {
		return errors.New("pinned workspace staging path is invalid")
	}
	if err := validatePinnedCheckoutPath(
		ctx,
		workspace.Path,
		stagingRoot,
		environment,
		false,
	); err != nil {
		return err
	}
	if err := verifyPinnedOrigin(ctx, workspace, repository, environment); err != nil {
		return err
	}
	if _, err := runPinnedGit(
		ctx,
		workspace.Path,
		environment,
		"fetch",
		"--no-tags",
		"--force",
		"--no-recurse-submodules",
		"--no-auto-maintenance",
		"origin",
		"refs/heads/"+sourceRef,
	); err != nil {
		return fmt.Errorf("fetch pinned source ref: %w", err)
	}
	fetched, err := resolvePinnedGitCommit(ctx, workspace.Path, "FETCH_HEAD", environment)
	if err != nil {
		return fmt.Errorf("resolve fetched pinned source ref: %w", err)
	}
	if fetched != expectedCommit {
		return fmt.Errorf(
			"pinned source ref resolved to %s, want %s",
			fetched,
			expectedCommit,
		)
	}
	if _, err := runPinnedGit(
		ctx,
		workspace.Path,
		environment,
		"-c",
		"core.hooksPath="+hooksPath,
		"checkout",
		"--detach",
		"--no-recurse-submodules",
		expectedCommit,
	); err != nil {
		return fmt.Errorf("checkout pinned commit: %w", err)
	}
	if err := validatePinnedCheckoutPath(
		ctx,
		workspace.Path,
		stagingRoot,
		environment,
		true,
	); err != nil {
		return err
	}
	return verifyPinnedWorkspaceContents(
		ctx,
		workspace,
		repository,
		expectedCommit,
		true,
		environment,
	)
}

func (m *Manager) verifyPinnedWorkspace(
	ctx context.Context,
	workspace *WorkspaceRecord,
	repository, expectedCommit string,
	requireExactHead bool,
	environment []string,
) error {
	if err := m.validatePinnedWorkspacePath(ctx, workspace, environment); err != nil {
		return err
	}
	return verifyPinnedWorkspaceContents(
		ctx,
		workspace,
		repository,
		expectedCommit,
		requireExactHead,
		environment,
	)
}

func verifyPinnedWorkspaceContents(
	ctx context.Context,
	workspace *WorkspaceRecord,
	repository, expectedCommit string,
	requireExactHead bool,
	environment []string,
) error {
	if err := verifyPinnedOrigin(ctx, workspace, repository, environment); err != nil {
		return err
	}
	if err := verifyPinnedGitControlPlane(ctx, workspace.Path, environment); err != nil {
		return err
	}
	pinned, err := resolvePinnedGitCommit(ctx, workspace.Path, expectedCommit, environment)
	if err != nil {
		return fmt.Errorf("resolve pinned commit: %w", err)
	}
	if pinned != expectedCommit {
		return fmt.Errorf("pinned commit resolved to %s, want %s", pinned, expectedCommit)
	}
	head, err := resolvePinnedGitCommit(ctx, workspace.Path, "HEAD", environment)
	if err != nil {
		return fmt.Errorf("resolve pinned workspace HEAD: %w", err)
	}
	if requireExactHead {
		if head != expectedCommit {
			return fmt.Errorf("pinned workspace HEAD is %s, want %s", head, expectedCommit)
		}
		status, statusErr := runPinnedGit(
			ctx,
			workspace.Path,
			environment,
			"status",
			"--porcelain=v1",
			"--untracked-files=all",
		)
		if statusErr != nil {
			return fmt.Errorf("inspect pinned workspace: %w", statusErr)
		}
		if strings.TrimSpace(status) != "" {
			return errors.New("newly pinned workspace is not clean")
		}
		expectedTree, treeErr := runPinnedGit(
			ctx,
			workspace.Path,
			environment,
			"rev-parse",
			"--verify",
			expectedCommit+"^{tree}",
		)
		if treeErr != nil {
			return fmt.Errorf("resolve pinned commit tree: %w", treeErr)
		}
		indexTree, treeErr := runPinnedGit(
			ctx,
			workspace.Path,
			environment,
			"write-tree",
		)
		if treeErr != nil {
			return fmt.Errorf("resolve pinned index tree: %w", treeErr)
		}
		expectedTree = strings.TrimSpace(expectedTree)
		indexTree = strings.TrimSpace(indexTree)
		if !validPinnedCommit(expectedTree) || !validPinnedCommit(indexTree) ||
			indexTree != expectedTree {
			return fmt.Errorf(
				"pinned index tree is %s, want %s",
				indexTree,
				expectedTree,
			)
		}
		return nil
	}
	if _, err := runPinnedGit(
		ctx,
		workspace.Path,
		environment,
		"merge-base",
		"--is-ancestor",
		expectedCommit,
		head,
	); err != nil {
		return fmt.Errorf("pinned commit is not an ancestor of workspace HEAD: %w", err)
	}
	return nil
}

func (m *Manager) validatePinnedWorkspacePath(
	ctx context.Context,
	workspace *WorkspaceRecord,
	environment []string,
) error {
	if m == nil || workspace == nil || strings.TrimSpace(workspace.Path) == "" {
		return errors.New("pinned workspace path is unavailable")
	}
	if err := m.validatePinnedCheckoutRoot(); err != nil {
		return err
	}
	root := m.checkoutRoot
	expectedPath := filepath.Join(
		root,
		safePathName(workspace.RemoteURL)+"-"+workspace.ID,
	)
	if filepath.Clean(workspace.Path) != filepath.Clean(expectedPath) {
		return errors.New("pinned workspace path does not match its inventory identity")
	}
	return validatePinnedCheckoutPath(ctx, workspace.Path, root, environment, true)
}

func (m *Manager) validatePinnedCheckoutRoot() error {
	if m == nil {
		return errors.New("git workspace manager is not configured")
	}
	return validatePrivateManagedDirectory(
		m.checkoutRoot,
		m.checkoutIdentity,
		"git workspace checkout root",
	)
}

func (m *Manager) validatePinnedOperationLockRoot() error {
	if m == nil {
		return errors.New("git workspace manager is not configured")
	}
	return validatePrivateManagedDirectory(
		m.lockRoot,
		m.lockIdentity,
		"git workspace operation lock root",
	)
}

func (m *Manager) validateRoot() error {
	if m == nil {
		return errors.New("git workspace manager is not configured")
	}
	return validatePrivateManagedDirectory(m.rootDir, m.rootIdentity, "git workspace root")
}

func validateManagedDirectory(path string, identity fs.FileInfo, label string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s is not a real directory", label)
	}
	if identity == nil || !os.SameFile(info, identity) {
		return fmt.Errorf("%s identity changed after manager initialization", label)
	}
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", label, err)
	}
	canonicalPath, err = filepath.Abs(canonicalPath)
	if err != nil {
		return fmt.Errorf("resolve absolute %s: %w", label, err)
	}
	if filepath.Clean(canonicalPath) != filepath.Clean(path) {
		return fmt.Errorf("%s resolves outside its initialized path", label)
	}
	return nil
}

func validatePrivateManagedDirectory(path string, identity fs.FileInfo, label string) error {
	if err := validateManagedDirectory(path, identity, label); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s permissions: %w", label, err)
	}
	if !managedDirectoryModePrivate(path, info) {
		return fmt.Errorf("%s is not private", label)
	}
	return nil
}

func validatePinnedCheckoutPath(
	ctx context.Context,
	path, root string,
	environment []string,
	requireIndex bool,
) error {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(root) == "" {
		return errors.New("pinned checkout path is unavailable")
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve pinned checkout root: %w", err)
	}
	canonicalRoot, err = filepath.Abs(canonicalRoot)
	if err != nil {
		return fmt.Errorf("resolve absolute pinned checkout root: %w", err)
	}
	workspacePath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve pinned workspace path: %w", err)
	}
	info, err := os.Lstat(workspacePath)
	if err != nil {
		return fmt.Errorf("inspect pinned workspace path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("pinned workspace path is not a real directory")
	}
	canonicalPath, err := filepath.EvalSymlinks(workspacePath)
	if err != nil {
		return fmt.Errorf("resolve pinned workspace symlinks: %w", err)
	}
	canonicalPath, err = filepath.Abs(canonicalPath)
	if err != nil {
		return fmt.Errorf("resolve absolute pinned workspace path: %w", err)
	}
	relative, err := filepath.Rel(canonicalRoot, canonicalPath)
	if err != nil || relative == "." || relative == "" || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("pinned workspace resolves outside the checkout root")
	}
	gitInfo, err := os.Lstat(filepath.Join(workspacePath, ".git"))
	if err != nil {
		return fmt.Errorf("inspect pinned workspace git directory: %w", err)
	}
	if gitInfo.Mode()&os.ModeSymlink != 0 || !gitInfo.IsDir() {
		return errors.New("pinned workspace git metadata is not a real directory")
	}
	if layoutErr := verifyPinnedGitMetadataLayout(
		ctx,
		filepath.Join(workspacePath, ".git"),
		requireIndex,
	); layoutErr != nil {
		return layoutErr
	}
	topLevel, err := runPinnedGit(
		ctx,
		workspacePath,
		environment,
		"rev-parse",
		"--show-toplevel",
	)
	if err != nil {
		return fmt.Errorf("resolve pinned workspace top level: %w", err)
	}
	topLevel, err = filepath.EvalSymlinks(strings.TrimSpace(topLevel))
	if err != nil || filepath.Clean(topLevel) != filepath.Clean(canonicalPath) {
		return errors.New("pinned workspace Git top level does not match its path")
	}
	gitDirectory, err := runPinnedGit(
		ctx,
		workspacePath,
		environment,
		"rev-parse",
		"--absolute-git-dir",
	)
	if err != nil {
		return fmt.Errorf("resolve pinned workspace git directory: %w", err)
	}
	gitDirectory, err = filepath.EvalSymlinks(strings.TrimSpace(gitDirectory))
	if err != nil || filepath.Clean(gitDirectory) !=
		filepath.Clean(filepath.Join(canonicalPath, ".git")) {
		return errors.New("pinned workspace Git directory does not match its path")
	}
	return nil
}

func verifyPinnedGitMetadataLayout(
	ctx context.Context,
	gitDirectory string,
	requireIndex bool,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	requiredFiles := []string{"HEAD", "config"}
	if requireIndex {
		requiredFiles = append(requiredFiles, "index")
	}
	for _, relativePath := range requiredFiles {
		info, err := os.Lstat(filepath.Join(gitDirectory, relativePath))
		if err != nil {
			return fmt.Errorf("inspect pinned Git metadata file %s: %w", relativePath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("pinned Git metadata file %s is not a real file", relativePath)
		}
	}
	for _, relativePath := range []string{
		"info",
		"objects",
		"objects/info",
		"objects/pack",
		"refs",
	} {
		info, err := os.Lstat(filepath.Join(gitDirectory, filepath.FromSlash(relativePath)))
		if err != nil {
			return fmt.Errorf("inspect pinned Git metadata directory %s: %w", relativePath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("pinned Git metadata directory %s is not a real directory", relativePath)
		}
	}
	packedRefsPath := filepath.Join(gitDirectory, "packed-refs")
	if info, err := os.Lstat(packedRefsPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("pinned Git packed-refs is not a real file")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect pinned Git packed-refs: %w", err)
	}
	if err := verifyPinnedPackDirectory(
		ctx,
		filepath.Join(gitDirectory, "objects", "pack"),
		false,
	); err != nil {
		return err
	}
	if err := verifyPinnedLooseObjectLayout(ctx, gitDirectory); err != nil {
		return err
	}
	logsPath := filepath.Join(gitDirectory, "logs")
	if logsInfo, err := os.Lstat(logsPath); err == nil {
		if logsInfo.Mode()&os.ModeSymlink != 0 || !logsInfo.IsDir() {
			return errors.New("pinned Git logs path is not a real directory")
		}
		headLogPath := filepath.Join(logsPath, "HEAD")
		if headInfo, headErr := os.Lstat(headLogPath); headErr == nil {
			if headInfo.Mode()&os.ModeSymlink != 0 || !headInfo.Mode().IsRegular() ||
				!pinnedMetadataFileHasSingleLink(headLogPath, headInfo) {
				return errors.New("pinned Git HEAD log is not an exclusive real file")
			}
		} else if !os.IsNotExist(headErr) {
			return fmt.Errorf("inspect pinned Git HEAD log: %w", headErr)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect pinned Git logs path: %w", err)
	}
	return nil
}

func verifyPinnedLooseObjectLayout(ctx context.Context, gitDirectory string) error {
	objectsPath := filepath.Join(gitDirectory, "objects")
	directory, err := os.Open(objectsPath)
	if err != nil {
		return fmt.Errorf("inspect pinned Git object directory: %w", err)
	}
	defer directory.Close()
	remaining := maxPinnedGitMetadataEntries
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := directory.ReadDir(256)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("inspect pinned Git object directory: %w", readErr)
		}
		for _, entry := range entries {
			remaining--
			if remaining < 0 {
				return errors.New("pinned Git loose-object metadata exceeds its limit")
			}
			name := entry.Name()
			if name == "info" || name == "pack" {
				continue
			}
			path := filepath.Join(objectsPath, name)
			info, statErr := os.Lstat(path)
			if statErr != nil {
				return fmt.Errorf("inspect pinned Git object fanout %s: %w", name, statErr)
			}
			if len(name) != 2 || !validLowerHex(name, 2) ||
				info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("pinned Git object fanout %s is invalid", name)
			}
			if fanoutErr := verifyPinnedLooseObjectFanout(
				ctx,
				path,
				name,
				&remaining,
			); fanoutErr != nil {
				return fanoutErr
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
	}
}

func verifyPinnedLooseObjectFanout(
	ctx context.Context,
	path, name string,
	remaining *int,
) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("inspect pinned Git loose objects in %s: %w", name, err)
	}
	defer directory.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		objects, readErr := directory.ReadDir(256)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("inspect pinned Git loose objects in %s: %w", name, readErr)
		}
		for _, object := range objects {
			*remaining--
			if *remaining < 0 {
				return errors.New("pinned Git loose-object metadata exceeds its limit")
			}
			objectInfo, objectErr := os.Lstat(filepath.Join(path, object.Name()))
			if objectErr != nil {
				return fmt.Errorf(
					"inspect pinned Git loose object %s/%s: %w",
					name,
					object.Name(),
					objectErr,
				)
			}
			if objectInfo.Mode()&os.ModeSymlink != 0 || !objectInfo.Mode().IsRegular() {
				return fmt.Errorf(
					"pinned Git loose object %s/%s is not a real file",
					name,
					object.Name(),
				)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
	}
}

func verifyPinnedPackDirectory(
	ctx context.Context,
	path string,
	rejectPromisor bool,
) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("inspect pinned Git pack directory: %w", err)
	}
	defer directory.Close()
	remaining := maxPinnedGitPackMetadataEntries
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := directory.ReadDir(256)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("inspect pinned Git pack directory: %w", readErr)
		}
		for _, entry := range entries {
			remaining--
			if remaining < 0 {
				return errors.New("pinned Git pack metadata exceeds its limit")
			}
			info, statErr := os.Lstat(filepath.Join(path, entry.Name()))
			if statErr != nil {
				return fmt.Errorf("inspect pinned Git pack entry %s: %w", entry.Name(), statErr)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return fmt.Errorf("pinned Git pack entry %s is not a real file", entry.Name())
			}
			if rejectPromisor && strings.HasSuffix(strings.ToLower(entry.Name()), ".promisor") {
				return errors.New("pinned workspace uses partial-clone promisor objects")
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
	}
}

func verifyPinnedOrigin(
	ctx context.Context,
	workspace *WorkspaceRecord,
	expectedRepository string,
	environment []string,
) error {
	configured, err := runPinnedGit(
		ctx,
		workspace.Path,
		environment,
		"config",
		"--local",
		"--get-all",
		"remote.origin.url",
	)
	if err != nil {
		return fmt.Errorf("read pinned workspace origin: %w", err)
	}
	configured = strings.TrimSuffix(configured, "\n")
	configured = strings.TrimSuffix(configured, "\r")
	urls := strings.Split(configured, "\n")
	if len(urls) != 1 || urls[0] != expectedRepository ||
		workspace.RemoteURL != expectedRepository {
		return errors.New("pinned workspace origin does not match its repository")
	}
	return nil
}

func resolvePinnedGitCommit(
	ctx context.Context,
	directory, revision string,
	environment []string,
) (string, error) {
	resolved, err := runPinnedGit(
		ctx,
		directory,
		environment,
		"rev-parse",
		"--verify",
		revision+"^{commit}",
	)
	if err != nil {
		return "", err
	}
	resolved = strings.TrimSpace(resolved)
	if !validPinnedCommit(resolved) {
		return "", errors.New("git resolved a noncanonical commit")
	}
	return resolved, nil
}

func (m *Manager) newPinnedGitEnvironment() ([]string, func(), error) {
	if m == nil {
		return nil, nil, errors.New("git workspace manager is not configured")
	}
	if err := m.validateRoot(); err != nil {
		return nil, nil, err
	}
	controlRoot, err := os.MkdirTemp(m.rootDir, ".pinned-git-control-")
	if err != nil {
		return nil, nil, fmt.Errorf("create pinned Git control directory: %w", err)
	}
	configPath := filepath.Join(controlRoot, "global.config")
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		_ = os.RemoveAll(controlRoot)
		return nil, nil, fmt.Errorf("create pinned Git configuration: %w", err)
	}
	hooksPath := filepath.Join(controlRoot, "hooks")
	if err := os.Mkdir(hooksPath, 0o700); err != nil {
		_ = os.RemoveAll(controlRoot)
		return nil, nil, fmt.Errorf("create pinned Git hooks directory: %w", err)
	}
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() { _ = os.RemoveAll(controlRoot) })
	}
	return pinnedGitEnvironment(configPath, hooksPath), cleanup, nil
}

func pinnedGitEnvironment(globalConfigPath, hooksPath string) []string {
	if strings.TrimSpace(globalConfigPath) == "" {
		globalConfigPath = os.DevNull
	}
	if strings.TrimSpace(hooksPath) == "" {
		hooksPath = os.DevNull
	}
	environment := make([]string, 0, len(os.Environ())+9)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upperName := strings.ToUpper(name)
		if strings.HasPrefix(upperName, "GIT_") || upperName == "SSH_ASKPASS" ||
			upperName == "SSH_ASKPASS_REQUIRE" {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment,
		"GIT_AUTHOR_NAME=PicoClaw",
		"GIT_AUTHOR_EMAIL=picoclaw@localhost",
		"GIT_COMMITTER_NAME=PicoClaw",
		"GIT_COMMITTER_EMAIL=picoclaw@localhost",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+globalConfigPath,
		"GIT_CONFIG_COUNT=4",
		"GIT_CONFIG_KEY_0=core.excludesFile",
		"GIT_CONFIG_VALUE_0="+os.DevNull,
		"GIT_CONFIG_KEY_1=core.attributesFile",
		"GIT_CONFIG_VALUE_1="+os.DevNull,
		"GIT_CONFIG_KEY_2=core.hooksPath",
		"GIT_CONFIG_VALUE_2="+hooksPath,
		"GIT_CONFIG_KEY_3=core.commitGraph",
		"GIT_CONFIG_VALUE_3=false",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_TERMINAL_PROMPT=0",
	)
}

func runPinnedGit(
	ctx context.Context,
	directory string,
	environment []string,
	args ...string,
) (string, error) {
	output, err := runPinnedGitPlumbing(
		ctx,
		directory,
		environment,
		nil,
		maxPinnedControlGitOutputBytes,
		args...,
	)
	return string(output), err
}

func verifyPinnedGitControlPlane(
	ctx context.Context,
	directory string,
	environment []string,
) error {
	gitDirectory := filepath.Join(directory, ".git")
	for _, relativePath := range []string{
		"commondir",
		"info/attributes",
		"info/grafts",
		"info/sparse-checkout",
		"objects/info/commit-graph",
		"objects/info/commit-graphs",
		"objects/info/alternates",
		"shallow",
		"config.worktree",
	} {
		_, err := os.Lstat(filepath.Join(gitDirectory, filepath.FromSlash(relativePath)))
		if err == nil {
			return fmt.Errorf("pinned workspace uses unsupported Git control file %s", relativePath)
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("inspect pinned Git control file %s: %w", relativePath, err)
		}
	}
	if err := verifyPinnedExcludeFile(filepath.Join(gitDirectory, "info", "exclude")); err != nil {
		return err
	}
	if err := verifyPinnedPackDirectory(
		ctx,
		filepath.Join(gitDirectory, "objects", "pack"),
		true,
	); err != nil {
		return err
	}
	replacements, err := runPinnedGit(
		ctx,
		directory,
		environment,
		"for-each-ref",
		"--format=%(refname)",
		"refs/replace",
	)
	if err != nil {
		return fmt.Errorf("inspect pinned Git replacement refs: %w", err)
	}
	if strings.TrimSpace(replacements) != "" {
		return errors.New("pinned workspace uses Git replacement refs")
	}
	configuration, err := runPinnedGit(
		ctx,
		directory,
		environment,
		"config",
		"--local",
		"--no-includes",
		"--null",
		"--list",
	)
	if err != nil {
		return fmt.Errorf("inspect pinned Git configuration: %w", err)
	}
	for _, entry := range strings.Split(configuration, "\x00") {
		if entry == "" {
			continue
		}
		key, _, _ := strings.Cut(entry, "\n")
		if unsafePinnedGitConfigKey(strings.ToLower(strings.TrimSpace(key))) {
			return fmt.Errorf("pinned workspace uses unsafe Git configuration %s", key)
		}
	}
	tracked, err := runPinnedGit(ctx, directory, environment, "ls-files", "-v", "-z")
	if err != nil {
		return fmt.Errorf("inspect pinned Git assume-unchanged entries: %w", err)
	}
	for _, entry := range strings.Split(tracked, "\x00") {
		if entry != "" && entry[0] >= 'a' && entry[0] <= 'z' {
			return errors.New("pinned workspace contains assume-unchanged index entries")
		}
	}
	tracked, err = runPinnedGit(ctx, directory, environment, "ls-files", "-t", "-z")
	if err != nil {
		return fmt.Errorf("inspect pinned Git skip-worktree entries: %w", err)
	}
	for _, entry := range strings.Split(tracked, "\x00") {
		if strings.HasPrefix(entry, "S ") {
			return errors.New("pinned workspace contains skip-worktree index entries")
		}
	}
	tracked, err = runPinnedGit(ctx, directory, environment, "ls-files", "-f", "-z")
	if err != nil {
		return fmt.Errorf("inspect pinned Git fsmonitor-valid entries: %w", err)
	}
	for _, entry := range strings.Split(tracked, "\x00") {
		if entry != "" && entry[0] >= 'a' && entry[0] <= 'z' {
			return errors.New("pinned workspace contains fsmonitor-valid index entries")
		}
	}
	return nil
}

func unsafePinnedGitConfigKey(key string) bool {
	switch key {
	case "core.worktree",
		"core.hookspath",
		"core.sparsecheckout",
		"core.sparsecheckoutcone",
		"core.fsmonitor",
		"core.sshcommand",
		"core.askpass",
		"core.attributesfile",
		"core.excludesfile",
		"core.commitgraph",
		"core.alternaterefscommand",
		"core.prefersymlinkrefs",
		"core.bigfilethreshold",
		"core.quotepath",
		"core.fsync",
		"core.fsyncmethod",
		"core.fsyncobjectfiles",
		"extensions.worktreeconfig",
		"extensions.refstorage",
		"extensions.partialclone",
		"fetch.recursesubmodules",
		"submodule.recurse",
		"remote.origin.uploadpack",
		"remote.origin.receivepack",
		"remote.origin.proxy":
		return true
	}
	for _, prefix := range []string{
		"include.",
		"includeif.",
		"url.",
		"diff.",
		"filter.",
		"credential.",
		"protocol.",
		"submodule.",
	} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	if strings.HasPrefix(key, "remote.") &&
		(strings.HasSuffix(key, ".pushurl") ||
			strings.HasSuffix(key, ".promisor") ||
			strings.HasSuffix(key, ".partialclonefilter")) {
		return true
	}
	return false
}

func verifyPinnedExcludeFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect pinned Git exclude file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("pinned Git exclude file is not a real file")
	}
	if info.Size() > 1<<20 {
		return errors.New("pinned Git exclude file is too large")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read pinned Git exclude file: %w", err)
	}
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			return errors.New("pinned Git exclude file contains local patterns")
		}
	}
	return nil
}

func containsPinnedControlCharacter(value string) bool {
	return strings.ContainsAny(value, "\x00\r\n")
}

func validPinnedSourceRef(ctx context.Context, sourceRef string) bool {
	if sourceRef == "" || strings.HasPrefix(sourceRef, "-") ||
		strings.ContainsRune(sourceRef, '\x00') {
		return false
	}
	_, err := runPinnedGit(
		ctx,
		"",
		pinnedGitEnvironment(os.DevNull, os.DevNull),
		"check-ref-format",
		"refs/heads/"+sourceRef,
	)
	return err == nil
}

func validPinnedCommit(commit string) bool {
	if len(commit) != 40 && len(commit) != 64 {
		return false
	}
	for index := range len(commit) {
		character := commit[index]
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func normalizeGenericAcquireRef(ctx context.Context, raw string) (string, error) {
	ref := strings.TrimSpace(raw)
	if ref == "" {
		return "", nil
	}
	if ref != raw || len(ref) > 4096 || !utf8.ValidString(ref) ||
		strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\x00\r\n\t") {
		return "", errors.New("git workspace ref is invalid")
	}
	if ref == "HEAD" || validPinnedCommit(strings.ToLower(ref)) {
		return ref, nil
	}
	if _, err := runPinnedGit(
		ctx,
		"",
		pinnedGitEnvironment(os.DevNull, os.DevNull),
		"check-ref-format",
		"refs/heads/"+ref,
	); err != nil {
		return "", errors.New("git workspace ref is invalid")
	}
	return ref, nil
}

func controllerPrivateWorkspace(workspace *WorkspaceRecord) bool {
	return workspace != nil && (workspace.DevelopmentLineID != "" ||
		workspace.PinnedSourceRef != "" || workspace.PinnedCommit != "")
}

func (m *Manager) createWorkspaceLocked(
	ctx context.Context,
	st *storeState,
	repo *RepositoryRecord,
	remoteURL string,
	ref string,
	now time.Time,
) (*WorkspaceRecord, error) {
	idBase := repo.ID
	id := idBase
	for i := 2; ; i++ {
		if _, exists := st.Workspaces[id]; !exists {
			break
		}
		id = fmt.Sprintf("%s-%d", idBase, i)
	}
	path := filepath.Join(m.rootDir, "checkouts", safePathName(remoteURL)+"-"+id)
	if err := os.RemoveAll(path); err != nil {
		return nil, fmt.Errorf("prepare git workspace path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create git workspace parent: %w", err)
	}
	if _, err := runGit(ctx, "", "clone", "--", remoteURL, path); err != nil {
		_ = os.RemoveAll(path)
		return nil, err
	}
	if ref != "" {
		if _, err := runGit(ctx, path, "checkout", ref); err != nil {
			_ = os.RemoveAll(path)
			return nil, err
		}
	}
	upstreamURL := localRepositoryRemoteOrigin(ctx, remoteURL)
	if err := configureFreshWorkspaceUpstream(ctx, path, upstreamURL); err != nil {
		_ = os.RemoveAll(path)
		return nil, err
	}
	ws := &WorkspaceRecord{
		ID:          id,
		RepoID:      repo.ID,
		RemoteURL:   remoteURL,
		UpstreamURL: upstreamURL,
		Ref:         ref,
		Path:        path,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	st.Workspaces[id] = ws
	repo.WorkspaceIDs = appendUnique(repo.WorkspaceIDs, id)
	m.addHistoryLocked(st, now, "cloned", repo.ID, id, "", "", path)
	return ws, nil
}

func configureFreshWorkspaceUpstream(ctx context.Context, workspacePath, upstreamURL string) error {
	if upstreamURL == "" {
		_, _ = runGit(ctx, workspacePath, "remote", "remove", "picoclaw-upstream")
		return nil
	}
	if _, err := runGit(ctx, workspacePath, "remote", "get-url", "picoclaw-upstream"); err == nil {
		_, err = runGit(ctx, workspacePath, "remote", "set-url", "picoclaw-upstream", upstreamURL)
		return err
	}
	_, err := runGit(ctx, workspacePath, "remote", "add", "picoclaw-upstream", upstreamURL)
	return err
}

func (m *Manager) dropWorkspaceLocked(
	ctx context.Context,
	st *storeState,
	ws *WorkspaceRecord,
	action string,
) error {
	if controllerPrivateWorkspace(ws) {
		return errors.New("controller-private git workspace cannot be dropped")
	}
	now := m.now().UTC()
	branch, changed, err := m.preserveWorkspaceLocked(ctx, ws, "", now)
	if err != nil {
		m.addHistoryLocked(st, now, "preserve_failed", ws.RepoID, ws.ID, "", "", err.Error())
		return err
	}
	if changed {
		ws.PreservedBranch = branch
	}
	if err := os.RemoveAll(ws.Path); err != nil {
		return fmt.Errorf("drop git workspace %s: %w", ws.ID, err)
	}
	ws.LockedBy = nil
	ws.UpdatedAt = now
	ws.LastWorkAt = now
	ws.DroppedAt = &now
	m.addHistoryLocked(st, now, action, ws.RepoID, ws.ID, "", "", "")
	return nil
}

func (m *Manager) preserveWorkspaceLocked(
	ctx context.Context,
	ws *WorkspaceRecord,
	sessionKey string,
	now time.Time,
) (string, bool, error) {
	if ws == nil || ws.Path == "" {
		return "", false, nil
	}
	if _, err := os.Stat(filepath.Join(ws.Path, ".git")); err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	run := func(args ...string) (string, error) {
		return runGit(ctx, ws.Path, args...)
	}
	var cleanupEnvironment func()
	pinnedPreservation := false
	preserveDescendant := false
	if ws.PinnedSourceRef != "" || ws.PinnedCommit != "" {
		if ws.PinnedSourceRef == "" || ws.PinnedCommit == "" ||
			ws.Ref != ws.PinnedSourceRef || !validPinnedCommit(ws.PinnedCommit) {
			return "", false, errors.New("pinned workspace preservation identity is invalid")
		}
		environment, cleanup, err := m.newPinnedGitEnvironment()
		if err != nil {
			return "", false, err
		}
		cleanupEnvironment = cleanup
		defer cleanupEnvironment()
		pinnedPreservation = true
		if pathErr := m.validatePinnedWorkspacePath(ctx, ws, environment); pathErr != nil {
			return "", false, pathErr
		}
		if originErr := verifyPinnedOrigin(ctx, ws, ws.RemoteURL, environment); originErr != nil {
			return "", false, originErr
		}
		if controlErr := verifyPinnedGitControlPlane(ctx, ws.Path, environment); controlErr != nil {
			return "", false, controlErr
		}
		run = func(args ...string) (string, error) {
			return runPinnedGit(ctx, ws.Path, environment, args...)
		}
		pinned, err := resolvePinnedGitCommit(ctx, ws.Path, ws.PinnedCommit, environment)
		if err != nil || pinned != ws.PinnedCommit {
			if err == nil {
				err = fmt.Errorf("resolved to %s, want %s", pinned, ws.PinnedCommit)
			}
			return "", false, fmt.Errorf("resolve pinned preservation commit: %w", err)
		}
		head, err := resolvePinnedGitCommit(ctx, ws.Path, "HEAD", environment)
		if err != nil {
			return "", false, fmt.Errorf("resolve pinned preservation HEAD: %w", err)
		}
		if _, err := runPinnedGit(
			ctx,
			ws.Path,
			environment,
			"merge-base",
			"--is-ancestor",
			ws.PinnedCommit,
			head,
		); err != nil {
			return "", false, fmt.Errorf("preserve pinned workspace ancestry: %w", err)
		}
		preserveDescendant = head != ws.PinnedCommit
	}
	status, statusErr := run("status", "--porcelain=v1", "--untracked-files=normal")
	if statusErr != nil {
		return "", false, statusErr
	}
	dirty := strings.TrimSpace(status) != ""
	if !dirty && !preserveDescendant {
		return "", false, nil
	}
	if sessionKey == "" && ws.LockedBy != nil {
		sessionKey = ws.LockedBy.SessionKey
	}
	branchSegment := safeBranchSegment(sessionKey)
	if pinnedPreservation {
		digest := sha256.Sum256(append(
			[]byte("picoclaw-pinned-preservation-branch-v1\x00"),
			[]byte(sessionKey)...,
		))
		branchSegment = "pinned-" + hex.EncodeToString(digest[:20])
	}
	branchBase := "picoclaw/session/" + branchSegment
	branchBase += "/" + now.Format("20060102-150405")
	if rootErr := m.validateRoot(); rootErr != nil {
		return "", false, rootErr
	}
	hooksPath, hooksErr := os.MkdirTemp(m.rootDir, ".preserve-hooks-")
	if hooksErr != nil {
		return "", false, fmt.Errorf("create preservation hooks directory: %w", hooksErr)
	}
	defer os.RemoveAll(hooksPath)
	if !dirty {
		branch, branchErr := nextPreservationBranch(run, branchBase)
		if branchErr != nil {
			return "", false, branchErr
		}
		if pinnedPreservation {
			if layoutErr := preparePinnedPreservationRefLayout(ws.Path, branch); layoutErr != nil {
				return "", false, layoutErr
			}
		}
		if _, branchErr := run(
			"-c",
			"core.hooksPath="+hooksPath,
			"branch",
			branch,
			"HEAD",
		); branchErr != nil {
			return "", false, branchErr
		}
		return branch, true, nil
	}
	if _, addErr := run("-c", "core.hooksPath="+hooksPath, "add", "-A"); addErr != nil {
		return "", false, addErr
	}
	stagedPaths, stagedErr := run("diff", "--cached", "--name-only", "-z")
	if stagedErr != nil {
		return "", false, stagedErr
	}
	if stagedPaths == "" {
		return "", false, errors.New("dirty workspace has no preservable staged changes")
	}
	branch, branchErr := nextPreservationBranch(run, branchBase)
	if branchErr != nil {
		return "", false, branchErr
	}
	if pinnedPreservation {
		if layoutErr := preparePinnedPreservationRefLayout(ws.Path, branch); layoutErr != nil {
			return "", false, layoutErr
		}
	}
	if _, checkoutErr := run(
		"-c",
		"core.hooksPath="+hooksPath,
		"checkout",
		"-b",
		branch,
	); checkoutErr != nil {
		return "", false, checkoutErr
	}
	message := "Preserve PicoClaw workspace changes"
	if sessionKey != "" && !pinnedPreservation {
		message += "\n\nSession: " + sessionKey
	}
	if _, commitErr := run(
		"-c",
		"core.hooksPath="+hooksPath,
		"-c",
		"commit.gpgSign=false",
		"commit",
		"-m",
		message,
	); commitErr != nil {
		return "", false, commitErr
	}
	remainingStatus, remainingErr := run("status", "--porcelain=v1", "--untracked-files=normal")
	if remainingErr != nil {
		return "", false, remainingErr
	}
	if strings.TrimSpace(remainingStatus) != "" {
		return "", false, errors.New("workspace remains dirty after preservation commit")
	}
	return branch, true, nil
}

func preparePinnedPreservationRefLayout(workspacePath, branch string) error {
	const prefix = "picoclaw/session/"
	if !strings.HasPrefix(branch, prefix) {
		return errors.New("pinned preservation branch is outside its reserved namespace")
	}
	components := strings.Split(branch, "/")
	if len(components) < 4 {
		return errors.New("pinned preservation branch is invalid")
	}
	for _, component := range components {
		if component == "" || component == "." || component == ".." ||
			strings.ContainsAny(component, `/\\`) {
			return errors.New("pinned preservation branch is invalid")
		}
	}
	gitDirectory := filepath.Join(workspacePath, ".git")
	refParent, err := ensurePinnedRealDirectoryComponents(
		gitDirectory,
		append([]string{"refs", "heads"}, components[:len(components)-1]...)...,
	)
	if err != nil {
		return fmt.Errorf("prepare pinned preservation ref: %w", err)
	}
	refPath := filepath.Join(refParent, components[len(components)-1])
	if _, statErr := os.Lstat(refPath); statErr == nil {
		return errors.New("pinned preservation ref destination already exists")
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("inspect pinned preservation ref destination: %w", statErr)
	}

	logParent, err := ensurePinnedRealDirectoryComponents(
		gitDirectory,
		append(
			[]string{"logs", "refs", "heads"},
			components[:len(components)-1]...,
		)...,
	)
	if err != nil {
		return fmt.Errorf("prepare pinned preservation reflog: %w", err)
	}
	logPath := filepath.Join(logParent, components[len(components)-1])
	if _, statErr := os.Lstat(logPath); statErr == nil {
		return errors.New("pinned preservation reflog destination already exists")
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("inspect pinned preservation reflog destination: %w", statErr)
	}
	return nil
}

func ensurePinnedRealDirectoryComponents(base string, components ...string) (string, error) {
	info, err := os.Lstat(base)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("pinned Git metadata root is not a real directory")
	}
	current := base
	for _, component := range components {
		if component == "" || component == "." || component == ".." ||
			strings.ContainsAny(component, `/\\`) {
			return "", errors.New("pinned Git metadata directory component is invalid")
		}
		parent := current
		current = filepath.Join(parent, component)
		info, err = os.Lstat(current)
		if os.IsNotExist(err) {
			if mkdirErr := os.Mkdir(current, 0o700); mkdirErr != nil {
				return "", mkdirErr
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("pinned Git metadata directory %s is not real", component)
		}
		if syncErr := fileutil.SyncDirectory(parent); syncErr != nil {
			return "", syncErr
		}
	}
	return current, nil
}

func nextPreservationBranch(
	run func(args ...string) (string, error),
	base string,
) (string, error) {
	if run == nil {
		return "", errors.New("Git runner is unavailable")
	}
	if _, err := run("check-ref-format", "refs/heads/"+base); err != nil {
		return "", fmt.Errorf("validate preservation branch: %w", err)
	}
	output, err := run("for-each-ref", "--format=%(refname)", "refs/heads")
	if err != nil {
		return "", fmt.Errorf("list preservation branches: %w", err)
	}
	existing := make(map[string]struct{})
	for _, ref := range strings.Split(output, "\n") {
		existing[strings.TrimSpace(ref)] = struct{}{}
	}
	for suffix := 1; ; suffix++ {
		candidate := base
		if suffix > 1 {
			candidate = fmt.Sprintf("%s-%d", base, suffix)
		}
		if _, found := existing["refs/heads/"+candidate]; !found {
			return candidate, nil
		}
	}
}

func (m *Manager) statsLocked(ctx context.Context, st *storeState) (Stats, error) {
	stats := Stats{
		RootDir:                    m.rootDir,
		MaxTotalSizeBytes:          m.opts.MaxTotalSizeBytes,
		IgnoredCleanupDelaySeconds: int64(m.opts.IgnoredCleanupDelay.Seconds()),
		DropDelaySeconds:           int64(m.opts.DropDelay.Seconds()),
	}
	controllerWorkspaces := make(map[string]struct{})
	for _, workspace := range st.Workspaces {
		if controllerPrivateWorkspace(workspace) {
			controllerWorkspaces[workspace.ID] = struct{}{}
		}
	}
	for _, entry := range st.History {
		if _, private := controllerWorkspaces[entry.WorkspaceID]; private ||
			strings.HasPrefix(entry.Action, "development_line_") ||
			strings.HasPrefix(entry.Action, "pinned_") {
			continue
		}
		stats.History = append(stats.History, entry)
	}
	repoStats := map[string]*RepositoryInfo{}

	for _, ws := range sortedWorkspaceRecords(st.Workspaces) {
		if ws == nil || controllerPrivateWorkspace(ws) {
			continue
		}
		info, err := m.workspaceInfo(ctx, ws)
		if err != nil {
			return Stats{}, err
		}
		stats.Workspaces = append(stats.Workspaces, info)
		if info.DroppedAt == nil {
			stats.WorkspaceCount++
			stats.TotalSizeBytes += info.SizeBytes
			stats.IgnoredBytes += info.IgnoredBytes
			if info.LockedBy != nil {
				stats.LockedWorkspaceCount++
			}
		}
		repoInfo := repoStats[ws.RepoID]
		if repoInfo == nil {
			repoInfo = &RepositoryInfo{
				ID:          ws.RepoID,
				RemoteURL:   ws.RemoteURL,
				FirstSeenAt: ws.CreatedAt,
				LastSeenAt:  ws.UpdatedAt,
				LastWorkAt:  ws.LastWorkAt,
			}
			repoStats[ws.RepoID] = repoInfo
		} else {
			if repoInfo.FirstSeenAt.IsZero() ||
				(!ws.CreatedAt.IsZero() && ws.CreatedAt.Before(repoInfo.FirstSeenAt)) {
				repoInfo.FirstSeenAt = ws.CreatedAt
			}
			if ws.UpdatedAt.After(repoInfo.LastSeenAt) {
				repoInfo.LastSeenAt = ws.UpdatedAt
			}
			if ws.LastWorkAt.After(repoInfo.LastWorkAt) {
				repoInfo.LastWorkAt = ws.LastWorkAt
			}
		}
		if info.DroppedAt == nil {
			repoInfo.WorkspaceCount++
			repoInfo.SizeBytes += info.SizeBytes
			repoInfo.IgnoredBytes += info.IgnoredBytes
			if info.LockedBy != nil {
				repoInfo.LockedCount++
			}
		}
	}

	for _, repo := range repoStats {
		stats.Repositories = append(stats.Repositories, *repo)
	}
	sort.Slice(stats.Repositories, func(i, j int) bool {
		return stats.Repositories[i].RemoteURL < stats.Repositories[j].RemoteURL
	})
	stats.RepositoryCount = len(stats.Repositories)
	sort.Slice(stats.History, func(i, j int) bool {
		return stats.History[i].Time.After(stats.History[j].Time)
	})
	return stats, nil
}

func (m *Manager) workspaceInfo(ctx context.Context, ws *WorkspaceRecord) (WorkspaceInfo, error) {
	info := WorkspaceInfo{
		ID:              ws.ID,
		RepoID:          ws.RepoID,
		RemoteURL:       ws.RemoteURL,
		UpstreamURL:     ws.UpstreamURL,
		Ref:             ws.Ref,
		Path:            ws.Path,
		PreservedBranch: ws.PreservedBranch,
		CreatedAt:       ws.CreatedAt,
		UpdatedAt:       ws.UpdatedAt,
		LastWorkAt:      ws.LastWorkAt,
		LastCleanedAt:   ws.LastCleanedAt,
		LockedBy:        cloneLock(ws.LockedBy),
		DroppedAt:       ws.DroppedAt,
	}
	if ws.DroppedAt != nil {
		info.Status = "dropped"
		return info, nil
	}
	if ws.LockedBy != nil {
		info.Status = "locked"
	} else {
		info.Status = "available"
	}
	size, err := dirSize(ws.Path)
	if err != nil && !os.IsNotExist(err) {
		return WorkspaceInfo{}, err
	}
	info.SizeBytes = size
	if ws.PinnedSourceRef != "" || ws.PinnedCommit != "" {
		environment, cleanup, environmentErr := m.newPinnedGitEnvironment()
		if environmentErr != nil {
			return WorkspaceInfo{}, environmentErr
		}
		defer cleanup()
		if verifyErr := m.verifyPinnedWorkspace(
			ctx,
			ws,
			ws.RemoteURL,
			ws.PinnedCommit,
			false,
			environment,
		); verifyErr != nil {
			return WorkspaceInfo{}, verifyErr
		}
		ignored, ignoredErr := ignoredSizePinned(ctx, ws.Path, environment)
		if ignoredErr == nil {
			info.IgnoredBytes = ignored
		}
		status, statusErr := runPinnedGit(
			ctx,
			ws.Path,
			environment,
			"status",
			"--porcelain=v1",
			"--untracked-files=normal",
		)
		info.Dirty = statusErr == nil && strings.TrimSpace(status) != ""
		branch, branchErr := runPinnedGit(
			ctx,
			ws.Path,
			environment,
			"branch",
			"--show-current",
		)
		if branchErr == nil {
			info.CurrentBranch = strings.TrimSpace(branch)
		}
		return info, nil
	}
	ignored, err := ignoredSize(ctx, ws.Path)
	if err == nil {
		info.IgnoredBytes = ignored
	}
	info.Dirty = isDirty(ctx, ws.Path)
	info.CurrentBranch = currentBranch(ctx, ws.Path)
	return info, nil
}

func ignoredSizePinned(
	ctx context.Context,
	repositoryPath string,
	environment []string,
) (int64, error) {
	output, err := runPinnedGit(
		ctx,
		repositoryPath,
		environment,
		"status",
		"--ignored",
		"--porcelain=v1",
		"-z",
		"--untracked-files=all",
	)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, path := range ignoredPathRoots(repositoryPath, output) {
		size, sizeErr := dirSize(path)
		if sizeErr != nil && !os.IsNotExist(sizeErr) {
			return 0, sizeErr
		}
		total += size
	}
	return total, nil
}

func (m *Manager) addHistoryLocked(
	st *storeState,
	now time.Time,
	action, repoID, workspaceID, sessionKey, agentID, detail string,
) {
	entry := HistoryEntry{
		ID: shortID(
			fmt.Sprintf("%s:%s:%s:%s:%d", action, repoID, workspaceID, sessionKey, now.UnixNano()),
		),
		Time:        now,
		Action:      action,
		RepoID:      repoID,
		WorkspaceID: workspaceID,
		SessionKey:  strings.TrimSpace(sessionKey),
		AgentID:     strings.TrimSpace(agentID),
		Detail:      strings.TrimSpace(detail),
	}
	if developmentLineHistoryEntry(st, entry) {
		entry = sanitizeControllerHistoryEntry(entry)
		st.DevelopmentLineHistory = append(st.DevelopmentLineHistory, entry)
		if len(st.DevelopmentLineHistory) > historyLimit {
			st.DevelopmentLineHistory = st.DevelopmentLineHistory[len(st.DevelopmentLineHistory)-historyLimit:]
		}
		return
	}
	st.History = append(st.History, entry)
	if len(st.History) > historyLimit {
		st.History = st.History[len(st.History)-historyLimit:]
	}
}

func partitionDevelopmentLineHistory(st *storeState) {
	if st == nil {
		return
	}
	for index := range st.DevelopmentLineHistory {
		st.DevelopmentLineHistory[index] = sanitizeControllerHistoryEntry(
			st.DevelopmentLineHistory[index],
		)
	}
	if len(st.History) == 0 {
		return
	}
	publicHistory := make([]HistoryEntry, 0, len(st.History))
	for _, entry := range st.History {
		if developmentLineHistoryEntry(st, entry) {
			st.DevelopmentLineHistory = append(
				st.DevelopmentLineHistory,
				sanitizeControllerHistoryEntry(entry),
			)
			continue
		}
		publicHistory = append(publicHistory, entry)
	}
	st.History = publicHistory
	if len(st.DevelopmentLineHistory) > historyLimit {
		st.DevelopmentLineHistory = st.DevelopmentLineHistory[len(st.DevelopmentLineHistory)-historyLimit:]
	}
}

func sanitizeControllerHistoryEntry(entry HistoryEntry) HistoryEntry {
	reservation := entry.SessionKey
	entry.SessionKey = ""
	entry.AgentID = ""
	if reservation != "" {
		entry.Detail = strings.ReplaceAll(
			entry.Detail,
			reservation,
			"[controller-reservation]",
		)
	}
	entry.ID = shortID(fmt.Sprintf(
		"controller-history:%s:%s:%s:%s:%d",
		entry.Action,
		entry.RepoID,
		entry.WorkspaceID,
		entry.Detail,
		entry.Time.UnixNano(),
	))
	return entry
}

func developmentLineHistoryEntry(st *storeState, entry HistoryEntry) bool {
	if strings.HasPrefix(entry.Action, "development_line_") ||
		strings.HasPrefix(entry.Action, "pinned_") {
		return true
	}
	if st == nil || entry.WorkspaceID == "" {
		return false
	}
	workspace := st.Workspaces[entry.WorkspaceID]
	if workspace != nil && (workspace.DevelopmentLineID != "" ||
		workspace.PinnedSourceRef != "" || workspace.PinnedCommit != "") {
		return true
	}
	for _, line := range st.DevelopmentLines {
		if line != nil && line.WorkspaceID == entry.WorkspaceID {
			return true
		}
	}
	return false
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=PicoClaw",
		"GIT_AUTHOR_EMAIL=picoclaw@localhost",
		"GIT_COMMITTER_NAME=PicoClaw",
		"GIT_COMMITTER_EMAIL=picoclaw@localhost",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(output))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s failed: %s", strings.Join(args, " "), msg)
	}
	return string(output), nil
}

func cleanIgnored(ctx context.Context, path string) error {
	_, err := runGit(ctx, path, "clean", "-ffdX")
	return err
}

func ignoredSize(ctx context.Context, repoPath string) (int64, error) {
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		return 0, err
	}
	output, err := runGit(
		ctx,
		repoPath,
		"status",
		"--ignored",
		"--porcelain=v1",
		"-z",
		"--untracked-files=all",
	)
	if err != nil {
		return 0, err
	}
	roots := ignoredPathRoots(repoPath, output)
	var total int64
	for _, path := range roots {
		size, err := dirSize(path)
		if err != nil && !os.IsNotExist(err) {
			return 0, err
		}
		total += size
	}
	return total, nil
}

func ignoredPathRoots(repoPath, status string) []string {
	seen := map[string]struct{}{}
	var roots []string
	for _, entry := range strings.Split(status, "\x00") {
		if !strings.HasPrefix(entry, "!! ") {
			continue
		}
		rel := strings.TrimSpace(strings.TrimPrefix(entry, "!! "))
		if rel == "" {
			continue
		}
		path := filepath.Clean(filepath.Join(repoPath, filepath.FromSlash(rel)))
		skip := false
		for _, existing := range roots {
			if path == existing || isWithin(path, existing) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		filtered := roots[:0]
		for _, existing := range roots {
			if !isWithin(existing, path) {
				filtered = append(filtered, existing)
			}
		}
		roots = filtered
		if _, ok := seen[path]; !ok {
			seen[path] = struct{}{}
			roots = append(roots, path)
		}
	}
	return roots
}

func isDirty(ctx context.Context, path string) bool {
	output, err := runGit(ctx, path, "status", "--porcelain=v1", "--untracked-files=normal")
	return err == nil && strings.TrimSpace(output) != ""
}

func currentBranch(ctx context.Context, path string) string {
	output, err := runGit(ctx, path, "branch", "--show-current")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(output)
}

func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func normalizeRepository(repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", errors.New("repository is required")
	}
	if normalized, ok := normalizeRemoteRepository(repo); ok {
		return normalized, nil
	}
	if strings.Contains(repo, "://") || isSCPStyleRemote(repo) {
		return "", errors.New("repository remote URL is invalid; configure credentials outside the URL")
	}
	if abs, err := filepath.Abs(repo); err == nil {
		return filepath.Clean(abs), nil
	}
	return repo, nil
}

func localRepositoryRemoteOrigin(ctx context.Context, repository string) string {
	if !filepath.IsAbs(repository) {
		return ""
	}
	output, err := runGit(ctx, repository, "remote", "get-url", "--all", "origin")
	if err != nil {
		return ""
	}
	lines := strings.Fields(strings.TrimSpace(output))
	if len(lines) != 1 {
		return ""
	}
	if normalized, ok := normalizeRemoteRepository(lines[0]); ok {
		return normalized
	}
	return ""
}

func normalizeRemoteRepository(repo string) (string, bool) {
	if normalized, ok := normalizeSCPRemote(repo); ok {
		return normalized, true
	}
	if !strings.Contains(repo, "://") {
		return "", false
	}
	parsed, err := url.Parse(repo)
	if err != nil {
		return "", false
	}
	return normalizeURLRemote(parsed)
}

func normalizeSCPRemote(repo string) (string, bool) {
	if !isSCPStyleRemote(repo) {
		return "", false
	}
	parts := strings.SplitN(repo, ":", 2)
	userHost := parts[0]
	remotePath, ok := normalizeRemotePath("", parts[1])
	if !ok {
		return "", false
	}
	user, host, ok := strings.Cut(userHost, "@")
	if !ok || strings.TrimSpace(user) == "" || strings.TrimSpace(host) == "" {
		return "", false
	}
	return formatSCPRemote(user, host, remotePath), true
}

func normalizeURLRemote(repoURL *url.URL) (string, bool) {
	scheme := strings.ToLower(repoURL.Scheme)
	if scheme == "" || repoURL.RawQuery != "" || repoURL.Fragment != "" {
		return "", false
	}
	host := repoURL.Hostname()
	if strings.TrimSpace(host) == "" {
		return "", false
	}
	port := repoURL.Port()
	switch scheme {
	case "http":
		if repoURL.User != nil || (port != "" && port != "80") {
			return "", false
		}
		remotePath, ok := normalizeRemotePath(host, repoURL.Path)
		if !ok {
			return "", false
		}
		return scheme + "://" + strings.ToLower(host) + "/" + remotePath, true
	case "https":
		if repoURL.User != nil || (port != "" && port != "443") {
			return "", false
		}
		remotePath, ok := normalizeRemotePath(host, repoURL.Path)
		if !ok {
			return "", false
		}
		return scheme + "://" + strings.ToLower(host) + "/" + remotePath, true
	case "git":
		if repoURL.User != nil || port != "" {
			return "", false
		}
		remotePath, ok := normalizeRemotePath(host, repoURL.Path)
		if !ok {
			return "", false
		}
		return scheme + "://" + strings.ToLower(host) + "/" + remotePath, true
	case "ssh":
		if port != "" && port != "22" {
			return "", false
		}
		user := "git"
		if repoURL.User != nil && repoURL.User.Username() != "" {
			user = repoURL.User.Username()
		}
		remotePath, ok := normalizeRemotePath(host, repoURL.Path)
		if !ok {
			return "", false
		}
		return formatSCPRemote(user, host, remotePath), true
	default:
		return "", false
	}
}

func normalizeRemotePath(host, rawPath string) (string, bool) {
	remotePath := strings.TrimSpace(rawPath)
	remotePath = strings.Trim(remotePath, "/")
	if remotePath == "" {
		return "", false
	}
	remotePath = strings.TrimPrefix(pathpkg.Clean("/"+remotePath), "/")
	if remotePath == "." || remotePath == "" {
		return "", false
	}
	segments := strings.Split(remotePath, "/")
	if len(segments) < 2 {
		return "", false
	}
	if strings.EqualFold(host, "github.com") && len(segments) != 2 {
		return "", false
	}
	return ensureGitSuffix(remotePath), true
}

func ensureGitSuffix(remotePath string) string {
	if strings.HasSuffix(strings.ToLower(remotePath), ".git") {
		return remotePath[:len(remotePath)-len(".git")] + ".git"
	}
	return remotePath + ".git"
}

func formatSCPRemote(user, host, remotePath string) string {
	return strings.TrimSpace(user) + "@" +
		strings.ToLower(strings.TrimSpace(host)) + ":" + remotePath
}

func isSCPStyleRemote(repo string) bool {
	colon := strings.Index(repo, ":")
	if colon <= 0 {
		return false
	}
	userHost := repo[:colon]
	if !strings.Contains(userHost, "@") {
		return false
	}
	firstSlash := strings.IndexAny(repo, `/\`)
	return firstSlash == -1 || colon < firstSlash
}

func repoID(repo string) string {
	return "gw-" + shortID(strings.ToLower(strings.TrimSpace(repo)))
}

func shortID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}

func safePathName(repo string) string {
	repo = strings.TrimSuffix(repo, ".git")
	base := filepath.Base(repo)
	if base == "." || base == "/" || base == "" {
		base = "repo"
	}
	return safeSegment(base, 40)
}

func safeBranchSegment(value string) string {
	if strings.TrimSpace(value) == "" {
		value = "unknown-session"
	}
	return safeSegment(value, 48)
}

func safeSegment(value string, maxLen int) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "repo"
	}
	if len(out) > maxLen {
		out = strings.Trim(out[:maxLen], "-")
	}
	if out == "" {
		out = "repo"
	}
	return out
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func sortedWorkspaceRecords(records map[string]*WorkspaceRecord) []*WorkspaceRecord {
	out := make([]*WorkspaceRecord, 0, len(records))
	for _, ws := range records {
		out = append(out, ws)
	}
	sort.Slice(out, func(i, j int) bool {
		left := out[i]
		right := out[j]
		if left == nil || right == nil {
			return right != nil
		}
		if !left.LastWorkAt.Equal(right.LastWorkAt) {
			if left.LastWorkAt.IsZero() {
				return false
			}
			if right.LastWorkAt.IsZero() {
				return true
			}
			return left.LastWorkAt.Before(right.LastWorkAt)
		}
		return left.ID < right.ID
	})
	return out
}

func workspaceInfos(ctx context.Context, m *Manager, records []*WorkspaceRecord) []WorkspaceInfo {
	out := make([]WorkspaceInfo, 0, len(records))
	for _, ws := range records {
		info, err := m.workspaceInfo(ctx, ws)
		if err == nil {
			out = append(out, info)
		}
	}
	return out
}

func cloneLock(lock *LockInfo) *LockInfo {
	if lock == nil {
		return nil
	}
	cp := *lock
	return &cp
}

func isWithin(candidate, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && rel != "." && filepath.IsLocal(rel)
}
