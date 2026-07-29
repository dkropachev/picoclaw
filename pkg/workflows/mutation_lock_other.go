//go:build !unix && !windows

package workflows

import (
	"path/filepath"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

func lockWorkflowMutationFile(path string) (func(), error) {
	if err := fileutil.MkdirAllDurable(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return func() {}, nil
}
