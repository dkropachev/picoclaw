package repoeval

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

const repositoryEvaluationLockDirectory = ".locks"

func repositoryEvaluationLockPath(root, name string) (string, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(name) == "" ||
		name != filepath.Base(name) || strings.ContainsAny(name, `/\\`) ||
		strings.ContainsRune(name, '\x00') {
		return "", errors.New("repository evaluation lock path is invalid")
	}
	directory := filepath.Join(root, repositoryEvaluationLockDirectory)
	if err := sqlitestore.EnsurePrivateDir(directory); err != nil {
		return "", err
	}
	return filepath.Join(directory, name), nil
}

func secureRepositoryEvaluationLockFile(path string, file *os.File) error {
	if file == nil {
		return errors.New("repository evaluation lock file is unavailable")
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return errors.Join(errors.New("repository evaluation lock file is unsafe"), err)
	}
	secured, err := fileutil.SecurePrivateFile(path)
	if err != nil || secured == nil || !os.SameFile(opened, secured) {
		return errors.Join(errors.New("repository evaluation lock file changed while opening"), err)
	}
	return fileutil.ValidatePrivateFile(path, secured)
}
