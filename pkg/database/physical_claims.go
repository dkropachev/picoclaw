package database

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
)

// PhysicalStoreClaims exclusively fence every physical generation registered
// by one trusted catalog, including across different PICOCLAW_HOME values.
type PhysicalStoreClaims struct {
	files []*os.File
	once  sync.Once
}

// AcquireCatalogStoreClaims claims every main/WAL/SHM/rollback-journal namespace derived from
// the trusted catalog. It accepts no caller-supplied store path.
func AcquireCatalogStoreClaims(home string, cfg *config.Config) (*PhysicalStoreClaims, error) {
	if !BrokerAuthorityHeld() && !MigrationFenceHeld() && !ProviderTestAuthorityHeld() {
		return nil, NewError(CodeUnauthorized, "physical database claims require owner authority")
	}
	// Project first without touching generation members, claim every lexical
	// namespace, and only then perform physical alias/security validation.
	catalog, err := storecatalog.Project(home, cfg)
	if err != nil {
		return nil, err
	}
	root, err := preparePhysicalClaimRoot()
	if err != nil {
		return nil, err
	}
	claimNames := make(map[string]struct{}, len(catalog.Specs)*4)
	for _, spec := range catalog.Specs {
		for _, member := range []string{
			spec.Path, spec.Path + "-wal", spec.Path + "-shm", spec.Path + "-journal",
		} {
			identity := filepath.Clean(member)
			if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
				identity = strings.ToLower(identity)
			}
			digest := sha256.Sum256([]byte(identity))
			claimNames[hex.EncodeToString(digest[:])] = struct{}{}
		}
	}
	names := make([]string, 0, len(claimNames))
	for name := range claimNames {
		names = append(names, name)
	}
	sort.Strings(names)
	claims := &PhysicalStoreClaims{files: make([]*os.File, 0, len(names))}
	for _, name := range names {
		file, lockErr := acquirePlatformFileLock(filepath.Join(root, name+".lock"), false)
		if errors.Is(lockErr, errFileLockBusy) {
			_ = claims.Close()
			return nil, NewError(CodeConflict, "physical database store is already owned")
		}
		if lockErr != nil {
			_ = claims.Close()
			return nil, fmt.Errorf("claim physical database store: %w", lockErr)
		}
		claims.files = append(claims.files, file)
	}
	if _, err := storecatalog.Build(home, cfg); err != nil {
		_ = claims.Close()
		return nil, err
	}
	return claims, nil
}

// Close releases every physical generation claim.
func (claims *PhysicalStoreClaims) Close() error {
	if claims == nil {
		return nil
	}
	var result error
	claims.once.Do(func() {
		for index := len(claims.files) - 1; index >= 0; index-- {
			result = errors.Join(result, releasePlatformFileLock(claims.files[index]))
		}
		claims.files = nil
	})
	return result
}

func preparePhysicalClaimRoot() (string, error) {
	cacheRoot, cacheErr := os.UserCacheDir()
	if cacheErr != nil || strings.TrimSpace(cacheRoot) == "" {
		return "", NewError(CodeUnavailable, "physical database claim root is unavailable")
	}
	root := filepath.Join(cacheRoot, "picoclaw", "database-store-claims")
	if err := rejectExistingAncestorAlias(root); err != nil {
		return "", err
	}
	if err := prepareOwnerOnlyLeafDirectory(root); err != nil {
		return "", fmt.Errorf("create physical database claim root: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", NewError(CodeIntegrity, "physical database claim root is unsafe")
	}
	if err := validateOwnerOnlyDirectory(root, info); err != nil {
		return "", err
	}
	return root, nil
}
