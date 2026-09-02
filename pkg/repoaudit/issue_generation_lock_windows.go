//go:build windows

package repoaudit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

const issueGenerationSlotRetryInterval = 25 * time.Millisecond

func (s Store) TryLockIssueGenerationAttempt(
	repository, draftID, generationID string,
) (func(), bool, error) {
	if !validBoundedText(repository, maxRepositoryIdentityBytes) ||
		!validBoundedText(draftID, 256) ||
		!validBoundedText(generationID, maxIssueGenerationIDBytes) {
		return nil, false, errors.New("invalid repository review issue generation lock")
	}
	lockPath, err := repositoryReviewLockPath(
		s.root,
		"issue-generation-"+stableID("", repository, draftID, generationID)+".lock",
	)
	if err != nil {
		return nil, false, err
	}
	return tryLockRepositoryReviewIssueFile(lockPath)
}

func (s Store) AcquireIssueGenerationSlot(
	ctx context.Context,
	maximum int,
) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if maximum < 1 || maximum > 32 {
		return nil, errors.New("invalid repository review issue generation slot limit")
	}
	for {
		for slot := 0; slot < maximum; slot++ {
			lockPath, lockPathErr := repositoryReviewLockPath(
				s.root,
				fmt.Sprintf("issue-writer-slot-%02d.lock", slot),
			)
			if lockPathErr != nil {
				return nil, lockPathErr
			}
			release, acquired, err := tryLockRepositoryReviewIssueFile(lockPath)
			if err != nil {
				return nil, err
			}
			if acquired {
				return release, nil
			}
		}
		timer := time.NewTimer(issueGenerationSlotRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func tryLockRepositoryReviewIssueFile(lockPath string) (func(), bool, error) {
	if info, err := os.Lstat(lockPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, false, errors.New("repository review issue lock must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, false, err
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := secureRepositoryReviewLockFile(lockPath, file); err != nil {
		_ = file.Close()
		return nil, false, err
	}
	overlapped := new(windows.Overlapped)
	err = windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, overlapped,
	)
	if err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
		_ = file.Close()
	}, true, nil
}
