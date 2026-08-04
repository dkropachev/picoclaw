//go:build !unix && !windows

package media

import (
	"io/fs"
	"os"
)

func openSnapshotFileNoFollow(path string) (*os.File, error) {
	_ = path
	return nil, ErrSnapshotUnavailable
}

func snapshotFileChangeToken(_ *os.File, _ fs.FileInfo) (any, error) {
	return nil, ErrSnapshotUnavailable
}
