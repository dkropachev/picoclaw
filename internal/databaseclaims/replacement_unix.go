//go:build unix && !aix

package databaseclaims

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/pkg/database"
)

type unixReplacementHandle struct {
	file     *os.File
	identity fileidentity.Identity
}

func openReplacementHandle(path string) (replacementHandle, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open physical database replacement stage: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	identity, objectType, err := fileidentity.Opened(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect physical database replacement stage: %w", err)
	}
	if objectType != fileidentity.ObjectTypeRegular {
		_ = file.Close()
		return nil, database.NewError(database.CodeIntegrity, "replacement stage handle is not regular")
	}
	handle := &unixReplacementHandle{file: file, identity: identity}
	if !handle.valid() {
		_ = file.Close()
		return nil, database.NewError(database.CodeIntegrity, "replacement stage handle is unsafe")
	}
	return handle, nil
}

func (handle *unixReplacementHandle) matches(identity fileidentity.Identity) bool {
	if handle == nil || !handle.valid() || !identity.Valid() || handle.identity != identity {
		return false
	}
	opened, objectType, err := fileidentity.Opened(handle.file)
	return err == nil && objectType == fileidentity.ObjectTypeRegular && opened == identity
}

func (handle *unixReplacementHandle) valid() bool {
	if handle == nil || handle.file == nil {
		return false
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(handle.file.Fd()), &stat); err != nil {
		return false
	}
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Nlink == 1
}

func (handle *unixReplacementHandle) close() error {
	if handle == nil || handle.file == nil {
		return nil
	}
	file := handle.file
	handle.file = nil
	return file.Close()
}
