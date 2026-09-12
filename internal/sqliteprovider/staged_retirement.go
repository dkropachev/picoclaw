package sqliteprovider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"sync"
)

const (
	stagedRetirementNameAttempts = 8
	stagedRetirementRandomBytes  = 16
)

// retainedStagedGeneration binds retirement to the exact provider-created
// regular file that occupied path when it was retained. Close abandons the
// capability without changing the filesystem; only Retire may remove it.
type retainedStagedGeneration struct {
	sync.Mutex
	platform *stagedRetirementPlatform
	retired  bool
	closed   bool
}

func retainStagedGeneration(
	ctx context.Context,
	path string,
) (*retainedStagedGeneration, error) {
	if ctx == nil {
		return nil, errors.New("SQLite staged retirement context is unavailable")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if err := validateStagedRetirementPath(path); err != nil {
		return nil, err
	}
	platform, err := retainStagedGenerationPlatform(path)
	if err != nil {
		return nil, err
	}
	if err := context.Cause(ctx); err != nil {
		return nil, errors.Join(err, platform.close())
	}
	return &retainedStagedGeneration{platform: platform}, nil
}

// Check proves that path currently names the exact retained single-link main.
// It does not move, refresh, or replace the retained identity.
func (retained *retainedStagedGeneration) Check(
	ctx context.Context,
	path string,
) error {
	if retained == nil || ctx == nil {
		return errors.New("SQLite staged retention check is unavailable")
	}
	if err := validateStagedRetirementPath(path); err != nil {
		return err
	}
	retained.Lock()
	defer retained.Unlock()
	if retained.closed || retained.retired || retained.platform == nil {
		return errors.New("SQLite staged retention check is closed")
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return retained.platform.check(path)
}

// Retire synchronously removes only the retained physical object from its
// retained parent. It reports success only after the platform implementation
// proves the original and quarantine names absent, proves the retained object
// unlinked/deletion-pending, and completes supported parent durability work.
func (retained *retainedStagedGeneration) Retire(ctx context.Context) error {
	if retained == nil || ctx == nil {
		return errors.New("SQLite staged retirement is unavailable")
	}
	retained.Lock()
	defer retained.Unlock()
	if retained.closed || retained.platform == nil {
		return errors.New("SQLite staged retirement is closed")
	}
	if retained.retired {
		return nil
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := retained.platform.retire(ctx); err != nil {
		return err
	}
	retained.retired = true
	return nil
}

// Close releases the retained handles without removing the stage. It is safe
// to defer immediately after a successful retainStagedGeneration call.
func (retained *retainedStagedGeneration) Close() error {
	if retained == nil {
		return nil
	}
	retained.Lock()
	defer retained.Unlock()
	if retained.closed {
		return nil
	}
	retained.closed = true
	platform := retained.platform
	retained.platform = nil
	if platform == nil {
		return nil
	}
	return platform.close()
}

func validateStagedRetirementPath(path string) error {
	if !validProviderFilesystemPath(path) || !filepath.IsAbs(path) ||
		filepath.Clean(path) != path || strings.HasPrefix(strings.ToLower(path), "file:") ||
		filepath.Base(path) == "." || filepath.Base(path) == string(filepath.Separator) {
		return errors.New("SQLite staged retirement path is invalid")
	}
	if err := validateProviderPathSyntax(path); err != nil {
		return err
	}
	if err := validateProviderAncestors(path); err != nil {
		return err
	}
	return nil
}

func unusedStagedRetirementLeaf(
	available func(string) (bool, error),
) (string, error) {
	if available == nil {
		return "", errors.New("SQLite staged retirement namespace inspection is unavailable")
	}
	for range stagedRetirementNameAttempts {
		random := make([]byte, stagedRetirementRandomBytes)
		if _, err := rand.Read(random); err != nil {
			return "", err
		}
		leaf := ".sqlite-retirement-" + hex.EncodeToString(random)
		unused, err := available(leaf)
		if err != nil {
			return "", err
		}
		if unused {
			return leaf, nil
		}
	}
	return "", errors.New("SQLite staged retirement filename space is exhausted")
}
