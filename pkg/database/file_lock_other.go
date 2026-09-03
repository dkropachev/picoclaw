//go:build !unix && !windows

package database

import "os"

func acquirePlatformFileLock(string, bool) (*os.File, error) {
	return nil, NewError(CodeUnsupported, "database storage fencing is unsupported on this platform")
}

func releasePlatformFileLock(*os.File) error { return nil }
