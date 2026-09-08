//go:build windows

package sqliteprovider

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func providerOpenFile(path string, flag int, mode os.FileMode) (providerFile, error) {
	if flag != os.O_RDWR && flag != os.O_CREATE|os.O_EXCL|os.O_RDWR || mode.Perm() != 0o600 {
		return nil, errors.New("SQLite provider Windows file flags are invalid")
	}
	_, parent, err := openPinnedProviderWindowsDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || current == nil || current.User.Sid == nil || !current.User.Sid.IsValid() {
		return nil, errors.New("SQLite provider Windows owner is unavailable")
	}
	descriptor, err := windows.SecurityDescriptorFromString(
		"O:" + current.User.Sid.String() + "D:P(A;;GA;;;" + current.User.Sid.String() + ")",
	)
	if err != nil {
		return nil, err
	}
	pointer, err := providerWindowsPathPointer(path)
	if err != nil {
		return nil, err
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	disposition := uint32(windows.OPEN_EXISTING)
	if flag&os.O_CREATE != 0 {
		disposition = windows.CREATE_NEW
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		attributes,
		disposition,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	runtime.KeepAlive(descriptor)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("SQLite provider Windows file handle is unavailable")
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil ||
		information.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		_ = file.Close()
		return nil, errors.Join(errors.New("SQLite provider Windows file boundary is unsafe"), err)
	}
	if err := validateWindowsProviderHandle(handle, current.User.Sid, false); err != nil {
		_ = file.Close()
		return nil, err
	}
	if !pinnedProviderWindowsDirectoryMatches(parent, filepath.Dir(path)) {
		_ = file.Close()
		return nil, errors.New("SQLite provider Windows parent changed while opening file")
	}
	return file, nil
}
