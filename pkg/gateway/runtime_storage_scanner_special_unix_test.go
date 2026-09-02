//go:build integration && unix

package gateway

import "golang.org/x/sys/unix"

func runtimeStorageCreateSpecialCanary(path string) (bool, error) {
	if err := unix.Mkfifo(path, 0o600); err != nil {
		return false, err
	}
	return true, nil
}
