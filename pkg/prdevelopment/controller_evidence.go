package prdevelopment

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

func controllerContextDigest(contextText string) string {
	return controllerEvidenceDigest(
		"picoclaw-pr-development-controller-context-v1",
		contextText,
	)
}

func controllerModelResultDigest(content, workspaceID string, iterations int) string {
	return controllerEvidenceDigest(
		"picoclaw-pr-development-controller-model-result-v1",
		workspaceID,
		strconv.Itoa(iterations),
		content,
	)
}

func controllerEvidenceDigest(domain string, fields ...string) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte(domain))
	_, _ = digest.Write([]byte{0})
	var length [8]byte
	for _, field := range fields {
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write([]byte(field))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func controllerCommitMessage(attemptOrdinal int) (string, error) {
	if attemptOrdinal < 0 || attemptOrdinal >= 8192 {
		return "", fmt.Errorf("invalid durable repair attempt ordinal")
	}
	message := fmt.Sprintf(
		"Apply PR review repair attempt %d",
		attemptOrdinal+1,
	)
	if message != strings.TrimSpace(message) || len(message) > 512 {
		return "", fmt.Errorf("invalid deterministic repair commit message")
	}
	return message, nil
}
