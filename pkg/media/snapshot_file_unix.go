//go:build unix

package media

import (
	"errors"
	"io/fs"
	"os"
	"reflect"

	"golang.org/x/sys/unix"
)

func openSnapshotFileNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, ErrSnapshotNotRegular
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func snapshotFileChangeToken(_ *os.File, info fs.FileInfo) (any, error) {
	value := reflect.ValueOf(info.Sys())
	if !value.IsValid() {
		return nil, ErrSnapshotUnavailable
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil, ErrSnapshotUnavailable
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return nil, ErrSnapshotUnavailable
	}
	for _, fieldName := range []string{"Ctim", "Ctimespec", "Ctime"} {
		field := value.FieldByName(fieldName)
		if field.IsValid() && field.CanInterface() {
			return field.Interface(), nil
		}
	}
	return nil, ErrSnapshotUnavailable
}
