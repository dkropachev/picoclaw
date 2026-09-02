//go:build unix

package repoaudit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

const issueGenerationSlotRetryInterval = 25 * time.Millisecond

// TryLockIssueGenerationAttempt acquires non-blocking workspace-wide ownership
// of one durable generation attempt. The operating system releases the lock if
// its process exits, allowing an interrupted attempt to be resumed safely.
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

// AcquireIssueGenerationSlot waits for one of maximum workspace-wide provider
// call slots. Holding the returned release function is the slot lease.
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
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
			info.Mode().Perm()&0o077 != 0 {
			return nil, false, errors.New("repository review issue lock must be a private regular file")
		}
	} else if !os.IsNotExist(err) {
		return nil, false, err
	}
	if err := repositoryReviewMkdirLockDir(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, false, err
	}
	file, err := repositoryReviewOpenLockFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := secureRepositoryReviewLockFile(lockPath, file); err != nil {
		_ = file.Close()
		return nil, false, err
	}
	if err := repositoryReviewFlock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("lock repository review issue generation: %w", err)
	}
	return func() {
		_ = repositoryReviewFlock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, true, nil
}
