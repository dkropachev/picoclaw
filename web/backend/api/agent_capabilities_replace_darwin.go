//go:build darwin

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
		err := unix.RenamexNp(temporaryPath, targetPath, unix.RENAME_EXCL)
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
		default:
			return false, err
		}
	}

	if err := unix.RenamexNp(
		temporaryPath,
		targetPath,
		unix.RENAME_SWAP,
	); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return false, errAgentCapabilitiesRevisionMismatch
		}
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
			return recoverAgentCapabilitiesExchangeDarwin(
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
	return recoverAgentCapabilitiesExchangeDarwin(
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
	if err = unix.RenamexNp(
		targetPath,
		quarantinePath,
		unix.RENAME_EXCL,
	); err != nil {
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
	if err = unix.RenamexNp(
		quarantinePath,
		targetPath,
		unix.RENAME_EXCL,
	); err != nil {
		return fmt.Errorf(
			"restore concurrently edited agent definition; preserved at %s: %w",
			quarantinePath,
			err,
		)
	}
	if err = fileutil.SyncDirectory(directory); err != nil {
		return fmt.Errorf("sync restored agent definition directory: %w", err)
	}
	return errAgentCapabilitiesRevisionMismatch
}

func recoverAgentCapabilitiesExchangeDarwin(
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
		if err = unix.RenamexNp(
			temporaryPath,
			targetPath,
			unix.RENAME_SWAP,
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
