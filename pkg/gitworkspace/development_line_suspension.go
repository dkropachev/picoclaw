package gitworkspace

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"strconv"
	"time"
)

const (
	developmentLineSuspensionCandidate      = "candidate"
	developmentLineSuspensionCommitRecovery = "commit_recovery"
)

// developmentLineSuspensionRecord is controller-private, append-only evidence
// that a mutation reservation was permanently retired while its exact local
// candidate remained attached to the retained line. It stores no bearer,
// checkout path, diff, message, author, review, or publication authority.
type developmentLineSuspensionRecord struct {
	Mode                   string    `json:"mode"`
	IntentID               string    `json:"intent_id"`
	RequestHash            string    `json:"request_hash"`
	WorkspaceID            string    `json:"workspace_id"`
	LineID                 string    `json:"line_id"`
	RepoID                 string    `json:"repo_id"`
	SourceRef              string    `json:"source_ref"`
	SourceCommit           string    `json:"source_commit"`
	Version                int64     `json:"version"`
	MutationEpoch          int64     `json:"mutation_epoch"`
	Tip                    string    `json:"tip"`
	Tree                   string    `json:"tree"`
	RetiredReservationHash string    `json:"retired_reservation_hash"`
	AgentID                string    `json:"agent_id"`
	CandidateTree          string    `json:"candidate_tree"`
	CandidateDigest        string    `json:"candidate_digest"`
	ChangedFileCount       int       `json:"changed_file_count"`
	PreparedCommit         string    `json:"prepared_commit,omitempty"`
	PreparedTree           string    `json:"prepared_tree,omitempty"`
	PreparedCommitApplied  bool      `json:"prepared_commit_applied,omitempty"`
	PreviousRecordHash     string    `json:"previous_record_hash"`
	RecordHash             string    `json:"record_hash"`
	SuspendedAt            time.Time `json:"suspended_at"`
}

func emptyDevelopmentLineSuspensionDigest() string {
	digest := sha256.Sum256([]byte("picoclaw-pinned-development-line-suspensions-empty-v1\x00"))
	return hex.EncodeToString(digest[:])
}

func developmentLineSuspensionRecordDigest(record developmentLineSuspensionRecord) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("picoclaw-pinned-development-line-suspension-record-v1\x00"))
	for _, value := range []string{
		record.Mode,
		record.IntentID,
		record.RequestHash,
		record.WorkspaceID,
		record.LineID,
		record.RepoID,
		record.SourceRef,
		record.SourceCommit,
		strconv.FormatInt(record.Version, 10),
		strconv.FormatInt(record.MutationEpoch, 10),
		record.Tip,
		record.Tree,
		record.RetiredReservationHash,
		record.AgentID,
		record.CandidateTree,
		record.CandidateDigest,
		strconv.Itoa(record.ChangedFileCount),
		record.PreparedCommit,
		record.PreparedTree,
		strconv.FormatBool(record.PreparedCommitApplied),
		record.PreviousRecordHash,
		record.SuspendedAt.UTC().Format(time.RFC3339Nano),
	} {
		writeDevelopmentLineSuspensionHashField(digest, value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func writeDevelopmentLineSuspensionHashField(digest hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(value))
}

func hasDevelopmentLineSuspensionEvidence(state *storeState) bool {
	if state == nil {
		return false
	}
	for _, line := range state.DevelopmentLines {
		if line != nil && (line.State == developmentLineSuspended ||
			line.SuspensionCount != 0 || line.SuspensionTailHash != "" ||
			len(line.Suspensions) != 0) {
			return true
		}
	}
	return false
}

func initializeDevelopmentLineSuspensionAnchors(state *storeState) {
	if state == nil {
		return
	}
	for _, line := range state.DevelopmentLines {
		if line == nil {
			continue
		}
		line.SuspensionCount = 0
		line.SuspensionTailHash = emptyDevelopmentLineSuspensionDigest()
		line.Suspensions = nil
	}
}

func developmentLineSuspensionRetiredAt(
	line *developmentLineRecord,
	reservationHash string,
	version, mutationEpoch int64,
	agentID string,
) bool {
	if line == nil || reservationHash == "" {
		return false
	}
	for _, suspension := range line.Suspensions {
		if suspension.RetiredReservationHash == reservationHash &&
			suspension.Version == version && suspension.MutationEpoch == mutationEpoch &&
			suspension.AgentID == agentID {
			return true
		}
	}
	return false
}

func validateDevelopmentLineSuspensions(
	line *developmentLineRecord,
	workspace *WorkspaceRecord,
	activeReservations, reservationOwners map[string]string,
) error {
	if line == nil || workspace == nil {
		return errors.New("git workspace development line suspension owner is missing")
	}
	if line.SuspensionCount < 0 ||
		line.SuspensionCount > maxDevelopmentLineReservations ||
		line.SuspensionCount != len(line.Suspensions) {
		return errors.New("git workspace development line suspension anchor is invalid")
	}
	if line.SuspensionCount == 0 {
		if line.SuspensionTailHash != emptyDevelopmentLineSuspensionDigest() ||
			line.State == developmentLineSuspended {
			return errors.New("git workspace development line empty suspension anchor is invalid")
		}
		return nil
	}
	if line.SuspensionTailHash != line.Suspensions[len(line.Suspensions)-1].RecordHash {
		return errors.New("git workspace development line suspension tail is invalid")
	}

	intents := make(map[string]struct{}, len(line.Suspensions))
	requests := make(map[string]struct{}, len(line.Suspensions))
	recordHashes := make(map[string]struct{}, len(line.Suspensions))
	previousHash := emptyDevelopmentLineSuspensionDigest()
	var previous *developmentLineSuspensionRecord
	for index := range line.Suspensions {
		record := &line.Suspensions[index]
		prepared := record.PreparedCommit != "" || record.PreparedTree != ""
		validMode := record.Mode == developmentLineSuspensionCandidate ||
			record.Mode == developmentLineSuspensionCommitRecovery
		if !validMode ||
			(record.Mode == developmentLineSuspensionCandidate && prepared) ||
			(record.Mode == developmentLineSuspensionCommitRecovery && !prepared) ||
			(!prepared && record.PreparedCommitApplied) ||
			!validPinnedOperationIdentity(record.IntentID, maxDevelopmentLineIdentityBytes) ||
			!validLowerHex(record.RequestHash, sha256.Size*2) ||
			record.WorkspaceID != line.WorkspaceID || record.WorkspaceID != workspace.ID ||
			record.LineID != line.ID || record.RepoID != line.RepoID ||
			record.SourceRef != line.SourceRef || record.SourceCommit != line.SourceCommit ||
			record.Version < 0 || record.Version >= maxDevelopmentLineReservations ||
			record.Version > line.Version || record.MutationEpoch != record.Version+1 ||
			!validPinnedCommit(record.Tip) || !validPinnedCommit(record.Tree) ||
			len(record.Tip) != len(line.SourceCommit) ||
			len(record.Tree) != len(line.SourceCommit) ||
			!validLowerHex(record.RetiredReservationHash, sha256.Size*2) ||
			!validPinnedOperationIdentity(record.AgentID, 256) ||
			!validPinnedCommit(record.CandidateTree) ||
			len(record.CandidateTree) != len(line.SourceCommit) ||
			!validLowerHex(record.CandidateDigest, sha256.Size*2) ||
			record.ChangedFileCount < 0 ||
			record.ChangedFileCount > maxPinnedCandidateChangedFiles ||
			(prepared && (!validPinnedCommit(record.PreparedCommit) ||
				len(record.PreparedCommit) != len(line.SourceCommit) ||
				!validPinnedCommit(record.PreparedTree) ||
				len(record.PreparedTree) != len(line.SourceCommit) ||
				record.PreparedCommit == record.Tip ||
				record.PreparedTree == record.Tree)) ||
			record.PreviousRecordHash != previousHash ||
			!validLowerHex(record.RecordHash, sha256.Size*2) ||
			record.RecordHash != developmentLineSuspensionRecordDigest(*record) ||
			record.SuspendedAt.IsZero() || record.SuspendedAt.Before(line.CreatedAt) ||
			record.SuspendedAt.After(line.UpdatedAt) {
			return errors.New("git workspace development line suspension record is invalid")
		}
		if record.Version == line.Version &&
			(record.Tip != line.Tip || record.Tree != line.Tree) {
			return errors.New("git workspace development line suspension fence changed")
		}
		if line.State == developmentLineParked && record.Version >= line.Version {
			return errors.New("parked git workspace development line has a current suspension")
		}
		if previous != nil && (record.Version < previous.Version ||
			record.SuspendedAt.Before(previous.SuspendedAt) ||
			(record.Version == previous.Version &&
				(record.Tip != previous.Tip || record.Tree != previous.Tree))) {
			return errors.New("git workspace development line suspension history is not causal")
		}
		if _, duplicate := intents[record.IntentID]; duplicate {
			return errors.New("git workspace development line suspension intent was reused")
		}
		if _, duplicate := requests[record.RequestHash]; duplicate {
			return errors.New("git workspace development line suspension request was reused")
		}
		if _, duplicate := recordHashes[record.RecordHash]; duplicate {
			return errors.New("git workspace development line suspension record was reused")
		}
		if owner, duplicate := reservationOwners[record.RetiredReservationHash]; duplicate {
			if owner == line.ID {
				return errors.New("git workspace development line reservation was reused")
			}
			return errors.New("one mutation reservation belongs to multiple development lines")
		}
		if _, active := activeReservations[record.RetiredReservationHash]; active {
			return errors.New("suspended development line reservation still owns a pinned workspace")
		}
		intents[record.IntentID] = struct{}{}
		requests[record.RequestHash] = struct{}{}
		recordHashes[record.RecordHash] = struct{}{}
		reservationOwners[record.RetiredReservationHash] = line.ID
		previousHash = record.RecordHash
		previous = record
	}

	if line.State == developmentLineSuspended {
		tail := line.Suspensions[len(line.Suspensions)-1]
		if tail.Version != line.Version || tail.MutationEpoch != line.MutationEpoch ||
			tail.Tip != line.Tip || tail.Tree != line.Tree ||
			!tail.SuspendedAt.Equal(line.UpdatedAt) {
			return errors.New("suspended git workspace development line fence is invalid")
		}
	}
	return nil
}
