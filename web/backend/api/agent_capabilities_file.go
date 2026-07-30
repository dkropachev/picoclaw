package api

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	picoagent "github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	agentDefinitionMaxBytes                = picoagent.AgentDefinitionMaxBytes
	agentCapabilitiesExchangeRecoveryLimit = 64
)

var (
	errAgentDefinitionNotRegular                 = picoagent.ErrAgentDefinitionNotRegular
	errAgentDefinitionTooLarge                   = picoagent.ErrAgentDefinitionTooLarge
	errAgentCapabilitiesAtomicReplaceUnavailable = errors.New(
		"atomic agent capability replacement unavailable",
	)

	// agentCapabilitiesBeforeConditionalReplace is a test seam for edits that
	// race the final compare-and-swap. Production leaves it as a no-op.
	agentCapabilitiesBeforeConditionalReplace = func() {}
	// agentCapabilitiesBeforeConflictRecoverySwap is a test seam for an edit
	// that races conflict recovery after its latest target snapshot.
	agentCapabilitiesBeforeConflictRecoverySwap = func() {}
)

type agentDefinitionFile = picoagent.AgentDefinitionFile

type agentCapabilitiesWriteResult struct {
	candidateIdentity fs.FileInfo
}

type agentCapabilitiesVisibleCommitError struct {
	err error
}

func (err *agentCapabilitiesVisibleCommitError) Error() string {
	return err.err.Error()
}

func (err *agentCapabilitiesVisibleCommitError) Unwrap() error {
	return err.err
}

func readAgentDefinitionFile(path string) (agentDefinitionFile, bool, error) {
	return picoagent.ReadAgentDefinitionFile(path)
}

func agentDefinitionIssueCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errAgentDefinitionTooLarge):
		return "agent_definition_too_large"
	case errors.Is(err, errAgentDefinitionNotRegular):
		return "agent_definition_not_regular"
	default:
		return "agent_definition_unavailable"
	}
}

func ensureAgentDefinitionTargetSafe(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errAgentDefinitionNotRegular
	}
	return nil
}

func writeAgentCapabilitiesFileIfUnchanged(
	path string,
	data []byte,
	perm fs.FileMode,
	expected agentDefinitionFile,
	expectedExists bool,
) (agentCapabilitiesWriteResult, error) {
	result := agentCapabilitiesWriteResult{}
	directory := filepath.Dir(path)
	if err := fileutil.MkdirAllDurable(directory, 0o755); err != nil {
		return result, fmt.Errorf("create agent definition directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".agent-capabilities-*")
	if err != nil {
		return result, fmt.Errorf(
			"create agent definition temporary file: %w",
			err,
		)
	}
	temporaryPath := temporary.Name()
	cleanup := true
	defer func() {
		_ = temporary.Close()
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err = temporary.Write(data); err != nil {
		return result, fmt.Errorf(
			"write agent definition temporary file: %w",
			err,
		)
	}
	if err = temporary.Chmod(perm); err != nil {
		return result, fmt.Errorf(
			"set agent definition permissions: %w",
			err,
		)
	}
	if err = temporary.Sync(); err != nil {
		return result, fmt.Errorf(
			"sync agent definition temporary file: %w",
			err,
		)
	}
	candidateIdentity, err := temporary.Stat()
	if err != nil {
		return result, fmt.Errorf(
			"inspect agent definition temporary file: %w",
			err,
		)
	}
	result.candidateIdentity = candidateIdentity

	agentCapabilitiesBeforeConditionalReplace()
	candidate := agentDefinitionFile{
		Data: append([]byte(nil), data...),
		Mode: perm.Perm(),
	}
	if !agentCapabilitiesTargetMatches(temporaryPath, candidate, true) ||
		!agentCapabilitiesTargetIdentityMatches(
			temporaryPath,
			candidateIdentity,
		) {
		return result, errors.New(
			"candidate agent definition changed before commit",
		)
	}
	doNotCleanupTemporary, err := replaceAgentCapabilitiesFileIfUnchanged(
		temporaryPath,
		path,
		expected,
		expectedExists,
		candidate,
		candidateIdentity,
	)
	if doNotCleanupTemporary {
		cleanup = false
	}
	if err != nil &&
		!errors.Is(err, errAgentCapabilitiesRevisionMismatch) &&
		!expectedExists &&
		doNotCleanupTemporary {
		return result, &agentCapabilitiesVisibleCommitError{err: err}
	}
	return result, err
}

func agentCapabilitiesTargetMatches(
	path string,
	expected agentDefinitionFile,
	expectedExists bool,
) bool {
	current, exists, err := readAgentDefinitionFile(path)
	return err == nil &&
		exists == expectedExists &&
		(!exists || bytes.Equal(current.Data, expected.Data) &&
			current.Mode.Perm() == expected.Mode.Perm())
}

func agentCapabilitiesSourceMatches(
	path string,
	expected agentDefinitionFile,
	expectedExists bool,
) bool {
	return agentCapabilitiesTargetMatches(path, expected, expectedExists)
}

func agentCapabilitiesTargetIdentityMatches(
	path string,
	expected fs.FileInfo,
) bool {
	if expected == nil {
		return false
	}
	current, err := os.Lstat(path)
	return err == nil && current.Mode().IsRegular() &&
		os.SameFile(current, expected)
}

func removeAgentCapabilitiesBackupBestEffort(path string) {
	if err := fileutil.RemoveDurable(path); err != nil {
		logger.WarnCF(
			"agent-capabilities",
			"failed to remove displaced agent definition after commit",
			map[string]any{
				"path":  path,
				"error": err.Error(),
			},
		)
	}
}

func logPreservedAgentCapabilitiesFile(path string, reason string) {
	logger.WarnCF(
		"agent-capabilities",
		reason,
		map[string]any{"path": path},
	)
}
