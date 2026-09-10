package database

import (
	"errors"
	"os"
	"testing"
)

func currentTestExecutable(path string) (bool, error) {
	return currentTestExecutableWith(path, supervisorTestExecutableOps{
		testing: testing.Testing, executable: os.Executable, identity: supervisorPathIdentity,
	})
}

type supervisorTestExecutableOps struct {
	testing    func() bool
	executable func() (string, error)
	identity   func(string) (supervisorFileIdentity, error)
}

func currentTestExecutableWith(path string, ops supervisorTestExecutableOps) (bool, error) {
	if !ops.testing() {
		return false, nil
	}
	currentPath, err := ops.executable()
	if err != nil {
		return false, errors.New("resolve current test executable identity")
	}
	current, currentErr := ops.identity(currentPath)
	candidate, candidateErr := ops.identity(path)
	if currentErr != nil || candidateErr != nil || !current.Valid() || !candidate.Valid() {
		return false, errors.Join(
			errors.New("resolve current test executable identity"),
			currentErr,
			candidateErr,
		)
	}
	return current == candidate, nil
}
