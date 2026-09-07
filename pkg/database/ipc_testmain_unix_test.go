//go:build unix

package database

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMain(testingMain *testing.M) {
	previousUmask := unix.Umask(0o022)
	exitCode := testingMain.Run()
	unix.Umask(previousUmask)
	os.Exit(exitCode)
}
