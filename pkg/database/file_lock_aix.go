//go:build aix

package database

import "os"

func acquirePlatformFileLock(string, bool) (*os.File, error) {
	return nil, NewError(CodeUnsupported, "database storage locks are unsupported on AIX")
}

func releasePlatformFileLock(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
