//go:build windows

package media

import (
	"io/fs"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openSnapshotFileNoFollow(path string) (*os.File, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT|
			windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	basicInfo, basicInfoErr := snapshotWindowsBasicInfo(file)
	if basicInfoErr != nil {
		_ = file.Close()
		return nil, basicInfoErr
	}
	if basicInfo.fileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = file.Close()
		return nil, ErrSnapshotNotRegular
	}
	info, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return nil, statErr
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		_ = file.Close()
		return nil, ErrSnapshotNotRegular
	}
	return file, nil
}

type windowsSnapshotBasicInfo struct {
	creationTime   int64
	lastAccessTime int64
	lastWriteTime  int64
	changeTime     int64
	fileAttributes uint32
	_              uint32
}

type windowsSnapshotChangeToken struct {
	changeTime     int64
	fileAttributes uint32
}

func snapshotWindowsBasicInfo(file *os.File) (windowsSnapshotBasicInfo, error) {
	var info windowsSnapshotBasicInfo
	err := windows.GetFileInformationByHandleEx(
		windows.Handle(file.Fd()),
		windows.FileBasicInfo,
		(*byte)(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		return windowsSnapshotBasicInfo{}, ErrSnapshotUnavailable
	}
	return info, nil
}

func snapshotFileChangeToken(file *os.File, _ fs.FileInfo) (any, error) {
	info, err := snapshotWindowsBasicInfo(file)
	if err != nil {
		return nil, err
	}
	return windowsSnapshotChangeToken{
		changeTime:     info.changeTime,
		fileAttributes: info.fileAttributes,
	}, nil
}
