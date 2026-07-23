package gitworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

const (
	stateVersion      = 1
	historyLimit      = 1000
	lockRetryInterval = 50 * time.Millisecond
	inventoryLockDir  = "inventory.lock"
)

type Options struct {
	RootDir             string
	MaxTotalSizeBytes   int64
	IgnoredCleanupDelay time.Duration
	DropDelay           time.Duration
	Now                 func() time.Time
}

type Manager struct {
	rootDir string
	opts    Options
	now     func() time.Time
	mu      sync.Mutex
}

type AcquireRequest struct {
	Repository string
	Ref        string
	SessionKey string
	AgentID    string
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
	ID              string     `json:"id"`
	RepoID          string     `json:"repo_id"`
	RemoteURL       string     `json:"remote_url"`
	Ref             string     `json:"ref,omitempty"`
	Path            string     `json:"path"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	LastWorkAt      time.Time  `json:"last_work_at,omitempty"`
	LastCleanedAt   time.Time  `json:"last_cleaned_at,omitempty"`
	PreservedBranch string     `json:"preserved_branch,omitempty"`
	LockedBy        *LockInfo  `json:"locked_by,omitempty"`
	DroppedAt       *time.Time `json:"dropped_at,omitempty"`
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
	Version      int                          `json:"version"`
	Repositories map[string]*RepositoryRecord `json:"repositories"`
	Workspaces   map[string]*WorkspaceRecord  `json:"workspaces"`
	History      []HistoryEntry               `json:"history,omitempty"`
}

func NewManager(opts Options) (*Manager, error) {
	root := strings.TrimSpace(opts.RootDir)
	if root == "" {
		return nil, errors.New("git workspace root is required")
	}
	root = filepath.Clean(root)
	if err := os.MkdirAll(filepath.Join(root, "checkouts"), 0o755); err != nil {
		return nil, fmt.Errorf("create git workspace root: %w", err)
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Manager{
		rootDir: root,
		opts:    opts,
		now:     now,
	}, nil
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
	}
	repoRec.LastSeenAt = now

	if ws := m.findSessionWorkspaceLocked(st, repoID, sessionKey); ws != nil {
		ws.LockedBy.HeartbeatAt = now
		ws.UpdatedAt = now
		m.addHistoryLocked(st, now, "heartbeat", repoID, ws.ID, sessionKey, req.AgentID, "")
		if saveErr := m.saveLocked(st); saveErr != nil {
			return WorkspaceInfo{}, saveErr
		}
		return m.workspaceInfo(ctx, ws)
	}

	ws := m.findReusableWorkspaceLocked(st, repoID, strings.TrimSpace(req.Ref))
	if ws == nil {
		ws, err = m.createWorkspaceLocked(ctx, st, repoRec, repo, strings.TrimSpace(req.Ref), now)
		if err != nil {
			return WorkspaceInfo{}, err
		}
	}

	ws.LockedBy = &LockInfo{
		SessionKey:  sessionKey,
		AgentID:     strings.TrimSpace(req.AgentID),
		LockedAt:    now,
		HeartbeatAt: now,
	}
	ws.DroppedAt = nil
	ws.UpdatedAt = now
	repoRec.LastWorkAt = now
	m.addHistoryLocked(st, now, "allocated", repoID, ws.ID, sessionKey, req.AgentID, ws.Path)

	if err := m.saveLocked(st); err != nil {
		return WorkspaceInfo{}, err
	}
	return m.workspaceInfo(ctx, ws)
}

func (m *Manager) ReleaseSession(ctx context.Context, req ReleaseRequest) ([]WorkspaceInfo, error) {
	if m == nil {
		return nil, errors.New("git workspace manager is not configured")
	}
	sessionKey := strings.TrimSpace(req.SessionKey)
	if sessionKey == "" {
		return nil, errors.New("session key is required")
	}

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
	now := m.now().UTC()
	var released []*WorkspaceRecord
	for _, ws := range st.Workspaces {
		if ws == nil || ws.LockedBy == nil || ws.LockedBy.SessionKey != sessionKey {
			continue
		}
		branch, changed, err := m.preserveWorkspaceLocked(ctx, ws, sessionKey, now)
		if err != nil {
			m.addHistoryLocked(
				st,
				now,
				"preserve_failed",
				ws.RepoID,
				ws.ID,
				sessionKey,
				req.AgentID,
				err.Error(),
			)
			_ = m.saveLocked(st)
			return nil, err
		}
		if changed {
			ws.PreservedBranch = branch
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
		m.addHistoryLocked(st, now, "released", ws.RepoID, ws.ID, sessionKey, req.AgentID, detail)
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
	if ws == nil || ws.DroppedAt != nil {
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
	if ws == nil || ws.DroppedAt != nil {
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
		if ws == nil || ws.DroppedAt != nil || ws.LockedBy != nil || ws.LastWorkAt.IsZero() {
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
		if ws == nil || ws.DroppedAt != nil || ws.LockedBy != nil || ws.LastWorkAt.IsZero() {
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
			if ws == nil || ws.DroppedAt != nil || ws.LockedBy != nil {
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

func (m *Manager) lockInventory(ctx context.Context) (func(), error) {
	lockPath := filepath.Join(m.rootDir, inventoryLockDir)
	for {
		if err := os.Mkdir(lockPath, 0o700); err == nil {
			return func() {
				_ = os.Remove(lockPath)
			}, nil
		} else if !os.IsExist(err) {
			return nil, fmt.Errorf("lock git workspace inventory: %w", err)
		}
		timer := time.NewTimer(lockRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("lock git workspace inventory: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func (m *Manager) loadLocked() (*storeState, error) {
	st := &storeState{
		Version:      stateVersion,
		Repositories: map[string]*RepositoryRecord{},
		Workspaces:   map[string]*WorkspaceRecord{},
	}
	data, err := os.ReadFile(m.statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return nil, fmt.Errorf("read git workspace inventory: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return st, nil
	}
	if err := json.Unmarshal(data, st); err != nil {
		return nil, fmt.Errorf("parse git workspace inventory: %w", err)
	}
	if st.Repositories == nil {
		st.Repositories = map[string]*RepositoryRecord{}
	}
	if st.Workspaces == nil {
		st.Workspaces = map[string]*WorkspaceRecord{}
	}
	if st.Version == 0 {
		st.Version = stateVersion
	}
	return st, nil
}

func (m *Manager) saveLocked(st *storeState) error {
	if st == nil {
		return nil
	}
	st.Version = stateVersion
	if len(st.History) > historyLimit {
		st.History = st.History[len(st.History)-historyLimit:]
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode git workspace inventory: %w", err)
	}
	if err := os.MkdirAll(m.rootDir, 0o755); err != nil {
		return fmt.Errorf("create git workspace root: %w", err)
	}
	return fileutil.WriteFileAtomic(m.statePath(), data, 0o600)
}

func (m *Manager) findSessionWorkspaceLocked(
	st *storeState,
	repoID, sessionKey string,
) *WorkspaceRecord {
	for _, ws := range st.Workspaces {
		if ws == nil || ws.RepoID != repoID || ws.DroppedAt != nil || ws.LockedBy == nil {
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
		if ref == "" || ws.Ref == "" || ws.Ref == ref {
			return ws
		}
	}
	return nil
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
	ws := &WorkspaceRecord{
		ID:        id,
		RepoID:    repo.ID,
		RemoteURL: remoteURL,
		Ref:       ref,
		Path:      path,
		CreatedAt: now,
		UpdatedAt: now,
	}
	st.Workspaces[id] = ws
	repo.WorkspaceIDs = appendUnique(repo.WorkspaceIDs, id)
	m.addHistoryLocked(st, now, "cloned", repo.ID, id, "", "", path)
	return ws, nil
}

func (m *Manager) dropWorkspaceLocked(
	ctx context.Context,
	st *storeState,
	ws *WorkspaceRecord,
	action string,
) error {
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
	status, err := runGit(ctx, ws.Path, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(status) == "" {
		return "", false, nil
	}
	if sessionKey == "" && ws.LockedBy != nil {
		sessionKey = ws.LockedBy.SessionKey
	}
	branch := "picoclaw/session/" + safeBranchSegment(sessionKey)
	branch += "/" + now.Format("20060102-150405")
	if _, err := runGit(ctx, ws.Path, "checkout", "-B", branch); err != nil {
		return "", false, err
	}
	if _, err := runGit(ctx, ws.Path, "add", "-A"); err != nil {
		return "", false, err
	}
	if _, err := runGit(ctx, ws.Path, "diff", "--cached", "--quiet"); err == nil {
		return branch, false, nil
	}
	message := "Preserve PicoClaw workspace changes"
	if sessionKey != "" {
		message += "\n\nSession: " + sessionKey
	}
	if _, err := runGit(ctx, ws.Path, "commit", "-m", message); err != nil {
		return "", false, err
	}
	return branch, true, nil
}

func (m *Manager) statsLocked(ctx context.Context, st *storeState) (Stats, error) {
	stats := Stats{
		RootDir:                    m.rootDir,
		MaxTotalSizeBytes:          m.opts.MaxTotalSizeBytes,
		IgnoredCleanupDelaySeconds: int64(m.opts.IgnoredCleanupDelay.Seconds()),
		DropDelaySeconds:           int64(m.opts.DropDelay.Seconds()),
		History:                    append([]HistoryEntry(nil), st.History...),
	}
	repoStats := map[string]*RepositoryInfo{}
	for id, repo := range st.Repositories {
		if repo == nil {
			continue
		}
		repoStats[id] = &RepositoryInfo{
			ID:          repo.ID,
			RemoteURL:   repo.RemoteURL,
			FirstSeenAt: repo.FirstSeenAt,
			LastSeenAt:  repo.LastSeenAt,
			LastWorkAt:  repo.LastWorkAt,
		}
	}

	for _, ws := range sortedWorkspaceRecords(st.Workspaces) {
		if ws == nil {
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
			repoInfo = &RepositoryInfo{ID: ws.RepoID, RemoteURL: ws.RemoteURL}
			repoStats[ws.RepoID] = repoInfo
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
	ignored, err := ignoredSize(ctx, ws.Path)
	if err == nil {
		info.IgnoredBytes = ignored
	}
	info.Dirty = isDirty(ctx, ws.Path)
	info.CurrentBranch = currentBranch(ctx, ws.Path)
	return info, nil
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
	st.History = append(st.History, entry)
	if len(st.History) > historyLimit {
		st.History = st.History[len(st.History)-historyLimit:]
	}
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
		return repo, nil
	}
	if abs, err := filepath.Abs(repo); err == nil {
		return filepath.Clean(abs), nil
	}
	return repo, nil
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
		return formatSCPRemote("git", host, remotePath), true
	case "https":
		if repoURL.User != nil || (port != "" && port != "443") {
			return "", false
		}
		remotePath, ok := normalizeRemotePath(host, repoURL.Path)
		if !ok {
			return "", false
		}
		return formatSCPRemote("git", host, remotePath), true
	case "git":
		if repoURL.User != nil || port != "" {
			return "", false
		}
		remotePath, ok := normalizeRemotePath(host, repoURL.Path)
		if !ok {
			return "", false
		}
		return formatSCPRemote("git", host, remotePath), true
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
