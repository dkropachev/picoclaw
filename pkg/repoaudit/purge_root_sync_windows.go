//go:build windows

package repoaudit

import (
	"errors"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var repositoryReviewReOpenFile = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")

// Windows directory handles opened for traversal do not carry write access.
// Actual deletion is made durable below through a confined file handle reopened
// with FILE_FLAG_WRITE_THROUGH; missing-entry retries need no directory flush.
func syncRepositoryReviewPurgeRoot(_ *os.Root) error { return nil }

type repositoryReviewFileDispositionInfoEx struct {
	Flags uint32
}

func removeRepositoryReviewPurgeRootEntry(root *os.Root, name string) error {
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	handle, err := reopenRepositoryReviewPurgeFile(windows.Handle(file.Fd()))
	if err != nil {
		return err
	}
	info := repositoryReviewFileDispositionInfoEx{Flags: windows.FILE_DISPOSITION_DELETE |
		windows.FILE_DISPOSITION_POSIX_SEMANTICS |
		windows.FILE_DISPOSITION_FORCE_IMAGE_SECTION_CHECK |
		windows.FILE_DISPOSITION_IGNORE_READONLY_ATTRIBUTE}
	dispositionErr := windows.SetFileInformationByHandle(
		handle,
		windows.FileDispositionInfoEx,
		(*byte)(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	closeErr := windows.CloseHandle(handle)
	return errors.Join(dispositionErr, closeErr)
}

func reopenRepositoryReviewPurgeFile(original windows.Handle) (windows.Handle, error) {
	result, _, callErr := repositoryReviewReOpenFile.Call(
		uintptr(original),
		uintptr(windows.DELETE|windows.GENERIC_WRITE|windows.SYNCHRONIZE),
		uintptr(windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE),
		uintptr(windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS|
			windows.FILE_FLAG_WRITE_THROUGH),
	)
	handle := windows.Handle(result)
	if handle == windows.InvalidHandle {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return windows.InvalidHandle, callErr
		}
		return windows.InvalidHandle, syscall.EINVAL
	}
	return handle, nil
}
