package media

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// CleanupPolicy controls how the MediaStore treats the underlying file when
// a ref is released or expires.
type CleanupPolicy string

const (
	// CleanupPolicyDeleteOnCleanup means the file is store-managed and may be
	// deleted once the final ref for that path is gone.
	CleanupPolicyDeleteOnCleanup CleanupPolicy = "delete_on_cleanup"
	// CleanupPolicyForgetOnly means the store should only drop ref mappings and
	// must never delete the underlying file.
	CleanupPolicyForgetOnly CleanupPolicy = "forget_only"
)

// MediaMeta holds metadata about a stored media file.
type MediaMeta struct {
	Filename      string
	ContentType   string
	Source        string        // "telegram", "discord", "tool:image-gen", etc.
	CleanupPolicy CleanupPolicy // defaults to CleanupPolicyDeleteOnCleanup
}

// MediaStore manages the lifecycle of media files associated with processing scopes.
type MediaStore interface {
	// Store registers an existing local file under the given scope.
	// Returns a ref identifier (e.g. "media://<id>").
	// Store does not move or copy the file; it only records the mapping.
	// If meta.CleanupPolicy is empty, CleanupPolicyDeleteOnCleanup is assumed.
	Store(localPath string, meta MediaMeta, scope string) (ref string, err error)

	// Resolve returns the local file path for a given ref.
	Resolve(ref string) (localPath string, err error)

	// ResolveWithMeta returns the local file path and metadata for a given ref.
	ResolveWithMeta(ref string) (localPath string, meta MediaMeta, err error)

	// ReleaseAll deletes all files registered under the given scope
	// and removes the mapping entries. File-not-exist errors are ignored.
	ReleaseAll(scope string) error
}

// mediaEntry holds the path and metadata for a stored media file.
type mediaEntry struct {
	path     string
	meta     MediaMeta
	storedAt time.Time
}

type pathRefState struct {
	path           string
	identity       os.FileInfo
	refCount       int
	deleteEligible bool
}

// pendingPathDeletion identifies one final-ref deletion attempt. The token
// prevents an older cleanup pass from deleting a path after it has been
// registered again, even if that newer ref has already been released.
type pendingPathDeletion struct {
	key      string
	path     string
	identity os.FileInfo
	token    uint64
}

// MediaCleanerConfig configures the background TTL cleanup.
type MediaCleanerConfig struct {
	Enabled  bool
	MaxAge   time.Duration
	Interval time.Duration
}

// FileMediaStore is a pure in-memory implementation of MediaStore.
// Files are expected to already exist on disk (e.g. in /tmp/picoclaw_media/).
type FileMediaStore struct {
	mu          sync.RWMutex
	refs        map[string]mediaEntry
	scopeToRefs map[string]map[string]struct{}
	refToScope  map[string]string
	refToPath   map[string]string
	pathStates  map[string]pathRefState

	pendingPathDeletions map[string]pendingPathDeletion
	nextDeletionToken    uint64

	cleanerCfg  MediaCleanerConfig
	stop        chan struct{}
	cleanerMu   sync.Mutex
	cleanerDone chan struct{}
	startOnce   sync.Once
	stopOnce    sync.Once
	nowFunc     func() time.Time // for testing
}

// NewFileMediaStore creates a new FileMediaStore without background cleanup.
func NewFileMediaStore() *FileMediaStore {
	return &FileMediaStore{
		refs:                 make(map[string]mediaEntry),
		scopeToRefs:          make(map[string]map[string]struct{}),
		refToScope:           make(map[string]string),
		refToPath:            make(map[string]string),
		pathStates:           make(map[string]pathRefState),
		pendingPathDeletions: make(map[string]pendingPathDeletion),
		nowFunc:              time.Now,
	}
}

// NewFileMediaStoreWithCleanup creates a FileMediaStore with TTL-based background cleanup.
func NewFileMediaStoreWithCleanup(cfg MediaCleanerConfig) *FileMediaStore {
	return &FileMediaStore{
		refs:                 make(map[string]mediaEntry),
		scopeToRefs:          make(map[string]map[string]struct{}),
		refToScope:           make(map[string]string),
		refToPath:            make(map[string]string),
		pathStates:           make(map[string]pathRefState),
		pendingPathDeletions: make(map[string]pendingPathDeletion),
		cleanerCfg:           cfg,
		stop:                 make(chan struct{}),
		nowFunc:              time.Now,
	}
}

// Store registers a local file under the given scope. The file must exist.
func (s *FileMediaStore) Store(localPath string, meta MediaMeta, scope string) (string, error) {
	ref := "media://" + uuid.New().String()
	meta.CleanupPolicy = normalizeCleanupPolicy(meta.CleanupPolicy)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Path normalization, identity lookup, and registration must share the same
	// lock used by final-ref deletion. Otherwise cleanup can remove the file
	// after the check and before the ref is registered.
	canonicalPath, err := filepath.Abs(localPath)
	if err != nil {
		return "", fmt.Errorf("media store: %s: %w", localPath, err)
	}
	canonicalPath = filepath.Clean(canonicalPath)
	identity, err := os.Lstat(canonicalPath)
	if err != nil {
		return "", fmt.Errorf("media store: %s: %w", localPath, err)
	}
	pathKey := lifecyclePathKey(canonicalPath)

	pathState := s.pathStates[pathKey]
	if pathState.refCount > 0 {
		// A lifecycle key may only coalesce refs to the object captured by its
		// first live registration. An external replacement must start a later
		// lifecycle after the old refs are gone.
		if pathState.identity == nil || !os.SameFile(pathState.identity, identity) {
			return "", fmt.Errorf("media store: path identity changed: %s", canonicalPath)
		}
		canonicalPath = pathState.path
	}

	// Distinct lexical keys can still identify one directory entry (for
	// example Windows device-prefix, short-name, or case aliases). FileInfo
	// does not expose a portable comparable identity key, so verify collisions
	// with SameFile while holding the lifecycle lock. Do not coalesce distinct
	// paths because they may instead be hard links; conservatively make every
	// such lifecycle non-deleting.
	identityAlias := s.disableDeletionForIdentityAliasesLocked(pathKey, identity)
	if pathState.refCount == 0 {
		pathState.path = canonicalPath
		pathState.identity = identity
		pathState.deleteEligible = meta.CleanupPolicy == CleanupPolicyDeleteOnCleanup &&
			!identityAlias
	}

	// A successful re-registration permanently cancels older pending deletion
	// through either this lexical key or the verified entry identity. Token
	// validation keeps it canceled even if this ref is released before an older
	// cleanup resumes.
	s.cancelPendingPathDeletionsLocked(pathKey, identity)

	s.refs[ref] = mediaEntry{path: canonicalPath, meta: meta, storedAt: s.nowFunc()}
	if s.scopeToRefs[scope] == nil {
		s.scopeToRefs[scope] = make(map[string]struct{})
	}
	s.scopeToRefs[scope][ref] = struct{}{}
	s.refToScope[ref] = scope
	s.refToPath[ref] = canonicalPath

	if pathState.refCount > 0 &&
		(meta.CleanupPolicy == CleanupPolicyForgetOnly || identityAlias) {
		// A borrowed path or ambiguous distinct-key identity makes automatic
		// deletion unsafe for the rest of this live lifecycle.
		pathState.deleteEligible = false
	}
	pathState.refCount++
	s.pathStates[pathKey] = pathState

	return ref, nil
}

// Resolve returns the local path for the given ref.
func (s *FileMediaStore) Resolve(ref string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.refs[ref]
	if !ok {
		return "", fmt.Errorf("media store: unknown ref: %s", ref)
	}
	return entry.path, nil
}

// ResolveWithMeta returns the local path and metadata for the given ref.
func (s *FileMediaStore) ResolveWithMeta(ref string) (string, MediaMeta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.refs[ref]
	if !ok {
		return "", MediaMeta{}, fmt.Errorf("media store: unknown ref: %s", ref)
	}
	return entry.path, entry.meta, nil
}

// ReleaseAll removes all files under the given scope and cleans up mappings.
// Phase 1 (under lock): remove entries from maps and schedule final-ref
// deletions. Phase 2 validates each scheduled deletion and removes the file
// while holding the same lock used by Store. This prevents a stale cleanup
// pass from deleting a newly registered path.
func (s *FileMediaStore) ReleaseAll(scope string) error {
	// Phase 1: collect deletion candidates and remove mappings under lock.
	var deletions []pendingPathDeletion

	s.mu.Lock()
	refs, ok := s.scopeToRefs[scope]
	if !ok {
		s.mu.Unlock()
		return nil
	}

	for ref := range refs {
		fallbackPath := ""
		if entry, exists := s.refs[ref]; exists {
			fallbackPath = entry.path
		}
		if deletion, shouldDelete := s.releaseRefLocked(ref, fallbackPath); shouldDelete {
			deletions = append(deletions, deletion)
		}
	}
	delete(s.scopeToRefs, scope)
	s.mu.Unlock()

	// Phase 2: validate and delete each path atomically with registration.
	for _, deletion := range deletions {
		if err := s.removePendingPath(deletion); err != nil && !os.IsNotExist(err) {
			logger.WarnCF("media", "release: failed to remove file", map[string]any{
				"path":  deletion.path,
				"error": err.Error(),
			})
		}
	}

	return nil
}

// CleanExpired removes all entries older than MaxAge.
// Phase 1 (under lock): identify expired entries, remove mappings, and
// schedule final-ref deletions. Phase 2 validates and deletes each path while
// holding the same lock used by Store.
func (s *FileMediaStore) CleanExpired() int {
	if s.cleanerCfg.MaxAge <= 0 {
		return 0
	}

	// Phase 1: collect expired entries under lock
	type expiredEntry struct {
		ref      string
		deletion pendingPathDeletion
		remove   bool
	}

	s.mu.Lock()
	cutoff := s.nowFunc().Add(-s.cleanerCfg.MaxAge)
	var expired []expiredEntry

	for ref, entry := range s.refs {
		if entry.storedAt.Before(cutoff) {
			if scope, ok := s.refToScope[ref]; ok {
				if scopeRefs, ok := s.scopeToRefs[scope]; ok {
					delete(scopeRefs, ref)
					if len(scopeRefs) == 0 {
						delete(s.scopeToRefs, scope)
					}
				}
			}

			expiredItem := expiredEntry{ref: ref}
			if deletion, shouldDelete := s.releaseRefLocked(ref, entry.path); shouldDelete {
				expiredItem.deletion = deletion
				expiredItem.remove = true
			}
			expired = append(expired, expiredItem)
		}
	}
	s.mu.Unlock()

	// Phase 2: validate and delete each path atomically with registration.
	for _, e := range expired {
		if !e.remove {
			continue
		}
		if err := s.removePendingPath(e.deletion); err != nil && !os.IsNotExist(err) {
			logger.WarnCF("media", "cleanup: failed to remove file", map[string]any{
				"path":  e.deletion.path,
				"error": err.Error(),
			})
		}
	}

	return len(expired)
}

func normalizeCleanupPolicy(policy CleanupPolicy) CleanupPolicy {
	switch policy {
	case "", CleanupPolicyDeleteOnCleanup:
		return CleanupPolicyDeleteOnCleanup
	case CleanupPolicyForgetOnly:
		return CleanupPolicyForgetOnly
	default:
		return CleanupPolicyDeleteOnCleanup
	}
}

// lifecyclePathKey returns the stable key used to coordinate one lexical path.
// Store first makes the path absolute; Clean removes dot segments without
// applying a platform-inexact case fold. Filesystem aliases are coordinated
// separately through verified entry identity.
func lifecyclePathKey(path string) string {
	return filepath.Clean(path)
}

func (s *FileMediaStore) disableDeletionForIdentityAliasesLocked(
	pathKey string,
	identity os.FileInfo,
) bool {
	if identity == nil {
		return false
	}
	found := false
	for otherKey, otherState := range s.pathStates {
		if otherKey == pathKey || otherState.refCount <= 0 ||
			otherState.identity == nil ||
			!os.SameFile(otherState.identity, identity) {
			continue
		}
		otherState.deleteEligible = false
		s.pathStates[otherKey] = otherState
		found = true
	}
	return found
}

func (s *FileMediaStore) cancelPendingPathDeletionsLocked(
	pathKey string,
	identity os.FileInfo,
) {
	for pendingKey, pending := range s.pendingPathDeletions {
		if pendingKey == pathKey ||
			(identity != nil && pending.identity != nil &&
				os.SameFile(pending.identity, identity)) {
			delete(s.pendingPathDeletions, pendingKey)
		}
	}
}

func (s *FileMediaStore) releaseRefLocked(ref, fallbackPath string) (pendingPathDeletion, bool) {
	path := fallbackPath
	if storedPath, ok := s.refToPath[ref]; ok {
		path = storedPath
		delete(s.refToPath, ref)
	}

	delete(s.refs, ref)
	delete(s.refToScope, ref)

	if path == "" {
		return pendingPathDeletion{}, false
	}

	pathKey := lifecyclePathKey(path)
	pathState, ok := s.pathStates[pathKey]
	if !ok {
		return pendingPathDeletion{}, false
	}
	if pathState.refCount <= 1 {
		delete(s.pathStates, pathKey)
		if !pathState.deleteEligible {
			return pendingPathDeletion{}, false
		}

		s.nextDeletionToken++
		if s.nextDeletionToken == 0 {
			s.nextDeletionToken++
		}
		deletion := pendingPathDeletion{
			key:      pathKey,
			path:     pathState.path,
			identity: pathState.identity,
			token:    s.nextDeletionToken,
		}
		s.pendingPathDeletions[pathKey] = deletion
		return deletion, true
	}

	pathState.refCount--
	s.pathStates[pathKey] = pathState
	return pendingPathDeletion{}, false
}

// removePendingPath deletes a scheduled final-ref path only if no successful
// Store has re-referenced it since the deletion was scheduled. The path check,
// token consumption, and filesystem removal are serialized with Store and
// ReadSnapshot through the store lock.
func (s *FileMediaStore) removePendingPath(deletion pendingPathDeletion) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	pending, ok := s.pendingPathDeletions[deletion.key]
	if !ok || pending.token != deletion.token {
		return nil
	}
	if state, referenced := s.pathStates[deletion.key]; referenced && state.refCount > 0 {
		delete(s.pendingPathDeletions, deletion.key)
		return nil
	}

	delete(s.pendingPathDeletions, deletion.key)
	currentIdentity, err := os.Lstat(deletion.path)
	if err != nil {
		return err
	}
	if deletion.identity == nil || !os.SameFile(deletion.identity, currentIdentity) {
		return nil
	}
	return os.Remove(deletion.path)
}

// Start begins the background cleanup goroutine if cleanup is enabled.
// Safe to call multiple times; only the first call starts the goroutine.
func (s *FileMediaStore) Start() {
	if !s.cleanerCfg.Enabled || s.stop == nil {
		return
	}
	if s.cleanerCfg.Interval <= 0 || s.cleanerCfg.MaxAge <= 0 {
		logger.WarnCF("media", "cleanup: skipped due to invalid config", map[string]any{
			"interval": s.cleanerCfg.Interval.String(),
			"max_age":  s.cleanerCfg.MaxAge.String(),
		})
		return
	}

	s.startOnce.Do(func() {
		logger.InfoCF("media", "cleanup enabled", map[string]any{
			"interval": s.cleanerCfg.Interval.String(),
			"max_age":  s.cleanerCfg.MaxAge.String(),
		})

		done := make(chan struct{})
		s.cleanerMu.Lock()
		s.cleanerDone = done
		s.cleanerMu.Unlock()
		go func() {
			defer close(done)
			ticker := time.NewTicker(s.cleanerCfg.Interval)
			defer ticker.Stop()

			for {
				select {
				case <-ticker.C:
					if n := s.CleanExpired(); n > 0 {
						logger.InfoCF("media", "cleanup: removed expired entries", map[string]any{
							"count": n,
						})
					}
				case <-s.stop:
					return
				}
			}
		}()
	})
}

// Stop terminates the background cleanup goroutine.
// Safe to call multiple times; only the first call closes the channel.
func (s *FileMediaStore) Stop() {
	if s.stop == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stop)
	})
	s.cleanerMu.Lock()
	done := s.cleanerDone
	s.cleanerMu.Unlock()
	if done != nil {
		<-done
	}
}
