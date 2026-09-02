//go:build windows

package database

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	windowsCreateDirectorySecure = windows.CreateDirectory
	windowsCreateFileSecure      = windows.CreateFile
)

func createOwnerOnlyDirectory(path string) error {
	attributes, descriptor, err := windowsOwnerOnlySecurityAttributes(true)
	if err != nil {
		return err
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	err = windowsCreateDirectorySecure(pathPointer, attributes)
	runtime.KeepAlive(descriptor)
	return err
}

func prepareOwnerOnlyLeafDirectory(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := createOwnerOnlyDirectory(path); err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	return validateOwnerOnlyDirectory(path, info)
}

func createOwnerOnlyTempFile(directory, prefix string, _ os.FileMode) (*os.File, error) {
	for range 128 {
		random, err := randomHex(16)
		if err != nil {
			return nil, err
		}
		path := filepath.Join(directory, prefix+random)
		file, err := openWindowsOwnerOnlyFile(
			path,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			windows.CREATE_NEW,
			true,
		)
		if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			continue
		}
		return file, err
	}
	return nil, NewError(CodeConflict, "database owner-only temporary filename space is exhausted")
}

func createOwnerOnlyExclusiveFile(path string, _ os.FileMode) (*os.File, error) {
	return openWindowsOwnerOnlyFile(
		path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.CREATE_NEW,
		true,
	)
}

func openOwnerOnlyExistingFile(path string, _ os.FileMode) (*os.File, error) {
	return openWindowsOwnerOnlyFile(path, windows.GENERIC_READ, windows.OPEN_EXISTING, false)
}

func openOwnerOnlyLockFile(path string) (*os.File, error) {
	return openWindowsOwnerOnlyFile(
		path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.OPEN_ALWAYS,
		true,
	)
}

func openWindowsOwnerOnlyFile(
	path string,
	access uint32,
	disposition uint32,
	secureCreation bool,
) (*os.File, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var attributes *windows.SecurityAttributes
	var descriptor *windows.SECURITY_DESCRIPTOR
	if secureCreation {
		attributes, descriptor, err = windowsOwnerOnlySecurityAttributes(false)
		if err != nil {
			return nil, err
		}
	}
	handle, err := windowsCreateFileSecure(
		pathPointer,
		access,
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
		return nil, errors.New("open Windows owner-only file returned no handle")
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect Windows owner-only file: %w", err)
	}
	if information.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		_ = file.Close()
		return nil, NewError(CodeIntegrity, "database owner-only file boundary is invalid")
	}
	if err := validateWindowsOwnerOnlyHandle(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func windowsOwnerOnlySecurityAttributes(
	directory bool,
) (*windows.SecurityAttributes, *windows.SECURITY_DESCRIPTOR, error) {
	sid, err := windowsCurrentProcessUserSID()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve Windows owner-only security: %w", err)
	}
	sddl, err := windowsOwnerOnlySDDL(sid.String(), directory)
	if err != nil {
		return nil, nil, err
	}
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, nil, fmt.Errorf("build Windows owner-only security: %w", err)
	}
	return &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}, descriptor, nil
}
