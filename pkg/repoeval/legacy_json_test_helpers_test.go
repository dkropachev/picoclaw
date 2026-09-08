package repoeval

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
)

func (s Store) requireSafeRoot(allowMissing bool) error {
	info, err := os.Lstat(s.root)
	if os.IsNotExist(err) && allowMissing {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("repository evaluation storage root must be a real directory")
	}
	if !repositoryEvaluationPermissionsSafe(info.Mode()) {
		return errors.New("repository evaluation storage root permissions are too broad")
	}
	return nil
}

func (s Store) ensureSafeRoot() error {
	if err := s.requireSafeRoot(true); err != nil {
		return err
	}
	if err := sqlitestore.EnsurePrivateDir(s.root); err != nil {
		return err
	}
	return s.requireSafeRoot(false)
}

func (s Store) path(id string) string {
	return filepath.Join(s.root, stateNamePrefix+id+stateFileSuffix)
}
