package repoaudit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

const repositoryReviewLockDirectory = ".locks"

// repositoryReviewLockPath keeps every process-coordination file inside the
// protected store namespace. Lock files are runtime state, not compatibility
// artifacts, so no sibling-path fallback is retained.
func repositoryReviewLockPath(root, name string) (string, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(name) == "" ||
		name != filepath.Base(name) || strings.ContainsAny(name, `/\\`) ||
		strings.ContainsRune(name, '\x00') {
		return "", errors.New("repository review lock path is invalid")
	}
	directory := filepath.Join(root, repositoryReviewLockDirectory)
	if err := sqlitestore.EnsurePrivateDir(directory); err != nil {
		return "", err
	}
	return filepath.Join(directory, name), nil
}

func secureRepositoryReviewLockFile(path string, file *os.File) error {
	if file == nil {
		return errors.New("repository review lock file is unavailable")
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return errors.Join(errors.New("repository review lock file is unsafe"), err)
	}
	secured, err := fileutil.SecurePrivateFile(path)
	if err != nil || secured == nil || !os.SameFile(opened, secured) {
		return errors.Join(errors.New("repository review lock file changed while opening"), err)
	}
	return fileutil.ValidatePrivateFile(path, secured)
}
