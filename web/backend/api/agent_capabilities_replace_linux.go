//go:build linux

package api

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

func replaceAgentCapabilitiesFileIfUnchanged(
	temporaryPath string,
	targetPath string,
	expected agentDefinitionFile,
	expectedExists bool,
	candidate agentDefinitionFile,
	candidateIdentity fs.FileInfo,
) (bool, error) {
	if !expectedExists {
		err := unix.Renameat2(
			unix.AT_FDCWD,
			temporaryPath,
			unix.AT_FDCWD,
			targetPath,
			unix.RENAME_NOREPLACE,
		)
		switch {
		case err == nil:
			if !agentCapabilitiesTargetMatches(targetPath, candidate, true) ||
				!agentCapabilitiesTargetIdentityMatches(
					targetPath,
					candidateIdentity,
				) {
				logPreservedAgentCapabilitiesFile(
					targetPath,
					"conditional create was superseded before verification",
				)
				return true, errAgentCapabilitiesRevisionMismatch
			}
			return true, fileutil.SyncDirectory(filepath.Dir(targetPath))
		case errors.Is(err, unix.EEXIST):
			return false, errAgentCapabilitiesRevisionMismatch
		case errors.Is(err, unix.ENOSYS), errors.Is(err, unix.EINVAL):
			return linkNewAgentCapabilitiesFile(
				temporaryPath,
				targetPath,
				candidate,
				candidateIdentity,
			)
		default:
			return false, err
		}
	}

	err := unix.Renameat2(
		unix.AT_FDCWD,
		temporaryPath,
		unix.AT_FDCWD,
		targetPath,
		unix.RENAME_EXCHANGE,
	)
	switch {
	case errors.Is(err, unix.ENOENT):
		return false, errAgentCapabilitiesRevisionMismatch
	case errors.Is(err, unix.ENOSYS), errors.Is(err, unix.EINVAL):
		return false, errAgentCapabilitiesAtomicReplaceUnavailable
	case err != nil:
		return false, err
	}

	candidateInstalled := agentCapabilitiesTargetMatches(
		targetPath,
		candidate,
		true,
	) && agentCapabilitiesTargetIdentityMatches(
		targetPath,
		candidateIdentity,
	)
	if candidateInstalled &&
		agentCapabilitiesTargetMatches(temporaryPath, expected, true) {
		if syncErr := fileutil.SyncDirectory(
			filepath.Dir(targetPath),
		); syncErr != nil {
			return recoverAgentCapabilitiesExchangeLinux(
				temporaryPath,
				targetPath,
				candidateIdentity,
				fmt.Errorf(
					"sync replaced agent definition directory: %w",
					syncErr,
				),
			)
		}
		removeAgentCapabilitiesBackupBestEffort(temporaryPath)
		return true, nil
	}

	if !candidateInstalled {
		logPreservedAgentCapabilitiesFile(
			temporaryPath,
			"candidate save was superseded; displaced definition retained",
		)
		return true, errAgentCapabilitiesRevisionMismatch
	}
	return recoverAgentCapabilitiesExchangeLinux(
		temporaryPath,
		targetPath,
		candidateIdentity,
		errAgentCapabilitiesRevisionMismatch,
	)
}

func agentCapabilitiesConditionalCreateSupported() bool {
	return true
}

func rollbackCreatedAgentCapabilitiesFile(
	targetPath string,
	expected agentDefinitionFile,
	expectedIdentity fs.FileInfo,
) error {
	if !agentCapabilitiesTargetMatches(targetPath, expected, true) ||
		!agentCapabilitiesTargetIdentityMatches(targetPath, expectedIdentity) {
		return errAgentCapabilitiesRevisionMismatch
	}
	directory := filepath.Dir(targetPath)
	reservation, err := os.CreateTemp(directory, ".agent-capabilities-rollback-*")
	if err != nil {
		return fmt.Errorf("reserve agent definition rollback path: %w", err)
	}
	quarantinePath := reservation.Name()
	if closeErr := reservation.Close(); closeErr != nil {
		_ = os.Remove(quarantinePath)
		return fmt.Errorf("close agent definition rollback reservation: %w", closeErr)
	}
	if err = fileutil.RemoveDurable(quarantinePath); err != nil {
		return fmt.Errorf("release agent definition rollback path: %w", err)
	}

	err = unix.Renameat2(
		unix.AT_FDCWD,
		targetPath,
		unix.AT_FDCWD,
		quarantinePath,
		unix.RENAME_NOREPLACE,
	)
	if err != nil {
		return fmt.Errorf("quarantine agent definition rollback target: %w", err)
	}
	if err = fileutil.SyncDirectory(directory); err != nil {
		return fmt.Errorf(
			"sync quarantined agent definition directory; preserved at %s: %w",
			quarantinePath,
			err,
		)
	}
	if agentCapabilitiesTargetMatches(quarantinePath, expected, true) &&
		agentCapabilitiesTargetIdentityMatches(
			quarantinePath,
			expectedIdentity,
		) {
		if err = fileutil.RemoveDurable(quarantinePath); err != nil {
			return fmt.Errorf(
				"remove rolled back agent definition; preserved at %s: %w",
				quarantinePath,
				err,
			)
		}
		return nil
	}

	restoreErr := unix.Renameat2(
		unix.AT_FDCWD,
		quarantinePath,
		unix.AT_FDCWD,
		targetPath,
		unix.RENAME_NOREPLACE,
	)
	if restoreErr != nil {
		return fmt.Errorf(
			"restore concurrently edited agent definition; preserved at %s: %w",
			quarantinePath,
			restoreErr,
		)
	}
	if err = fileutil.SyncDirectory(directory); err != nil {
		return fmt.Errorf("sync restored agent definition directory: %w", err)
	}
	return errAgentCapabilitiesRevisionMismatch
}

func linkNewAgentCapabilitiesFile(
	temporaryPath string,
	targetPath string,
	candidate agentDefinitionFile,
	candidateIdentity fs.FileInfo,
) (bool, error) {
	if err := os.Link(temporaryPath, targetPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, errAgentCapabilitiesRevisionMismatch
		}
		return false, err
	}
	if !agentCapabilitiesTargetMatches(targetPath, candidate, true) ||
		!agentCapabilitiesTargetIdentityMatches(
			targetPath,
			candidateIdentity,
		) {
		logPreservedAgentCapabilitiesFile(
			targetPath,
			"conditional link create was superseded before verification",
		)
		return false, errAgentCapabilitiesRevisionMismatch
	}
	if err := fileutil.SyncDirectory(filepath.Dir(targetPath)); err != nil {
		return true, err
	}
	removeAgentCapabilitiesBackupBestEffort(temporaryPath)
	return true, nil
}

func recoverAgentCapabilitiesExchangeLinux(
	temporaryPath string,
	targetPath string,
	candidateIdentity fs.FileInfo,
	cause error,
) (bool, error) {
	for attempt := 0; attempt < agentCapabilitiesExchangeRecoveryLimit; attempt++ {
		targetIdentity, identityErr := os.Lstat(targetPath)
		if identityErr != nil {
			return true, fmt.Errorf(
				"inspect agent definition identity before conflict recovery; displaced file preserved at %s: %w",
				temporaryPath,
				identityErr,
			)
		}
		if attempt == 0 && !os.SameFile(targetIdentity, candidateIdentity) {
			logPreservedAgentCapabilitiesFile(
				temporaryPath,
				"candidate was superseded before conflict recovery",
			)
			return true, cause
		}
		targetBefore, exists, err := readAgentDefinitionFile(targetPath)
		if err != nil || !exists {
			logPreservedAgentCapabilitiesFile(
				temporaryPath,
				"target became unsafe during conflict recovery",
			)
			return true, fmt.Errorf(
				"inspect agent definition before conflict recovery; displaced file preserved at %s: %w",
				temporaryPath,
				errors.Join(errAgentCapabilitiesRevisionMismatch, err),
			)
		}
		agentCapabilitiesBeforeConflictRecoverySwap()
		if err = unix.Renameat2(
			unix.AT_FDCWD,
			temporaryPath,
			unix.AT_FDCWD,
			targetPath,
			unix.RENAME_EXCHANGE,
		); err != nil {
			return true, fmt.Errorf(
				"restore concurrently edited agent definition; displaced file preserved at %s: %w",
				temporaryPath,
				err,
			)
		}
		stableIdentity, identityErr := os.Lstat(temporaryPath)
		stable := identityErr == nil &&
			os.SameFile(stableIdentity, targetIdentity) &&
			agentCapabilitiesTargetMatches(temporaryPath, targetBefore, true)
		if stable {
			if err = fileutil.SyncDirectory(filepath.Dir(targetPath)); err != nil {
				return true, fmt.Errorf(
					"sync restored agent definition directory; displaced file preserved at %s: %w",
					temporaryPath,
					err,
				)
			}
			removeAgentCapabilitiesBackupBestEffort(temporaryPath)
			return true, cause
		}
	}

	logPreservedAgentCapabilitiesFile(
		temporaryPath,
		"conflict recovery retry limit reached",
	)
	return true, fmt.Errorf(
		"agent definition changed continuously during conflict recovery; latest displaced entry preserved at %s",
		temporaryPath,
	)
}
