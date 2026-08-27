package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestApplyPatchTransactionJournalRoundTripAuthenticated(t *testing.T) {
	key, journal := validApplyPatchTransactionJournal(t)

	first, err := encodeApplyPatchTransactionJournal(key, journal)
	if err != nil {
		t.Fatalf("encode journal: %v", err)
	}
	second, err := encodeApplyPatchTransactionJournal(key, journal)
	if err != nil {
		t.Fatalf("encode journal again: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("journal encoding is not deterministic")
	}
	if len(first) > applyPatchTransactionJournalMaxBytes {
		t.Fatalf("journal length = %d", len(first))
	}

	decoded, err := decodeApplyPatchTransactionJournal(key, first)
	if err != nil {
		t.Fatalf("decode journal: %v", err)
	}
	if !reflect.DeepEqual(decoded, journal) {
		t.Fatalf("decoded journal mismatch\n got: %#v\nwant: %#v", decoded, journal)
	}

	tampered := append([]byte(nil), first...)
	needle := []byte(journal.Operations[0].Source.Label)
	position := bytes.Index(tampered, needle)
	if position < 0 {
		t.Fatal("encoded source label was not found")
	}
	tampered[position] ^= 1
	if _, err := decodeApplyPatchTransactionJournal(key, tampered); !errors.Is(
		err,
		errApplyPatchTransactionJournalAuthentication,
	) {
		t.Fatalf("tampered journal error = %v", err)
	}

	wrongKey := bytes.Repeat([]byte{0x91}, applyPatchTransactionKeyBytes)
	if _, err := decodeApplyPatchTransactionJournal(wrongKey, first); !errors.Is(
		err,
		errApplyPatchTransactionJournalAuthentication,
	) {
		t.Fatalf("wrong-key journal error = %v", err)
	}
}

func TestApplyPatchTransactionJournalPhases(t *testing.T) {
	tests := []struct {
		name              string
		phase             applyPatchTransactionPhase
		decisionAttempted bool
		checkpoint        bool
		wantError         bool
	}{
		{name: "preparing", phase: applyPatchTransactionPhasePreparing},
		{
			name: "preparing decision invalid", phase: applyPatchTransactionPhasePreparing,
			decisionAttempted: true, wantError: true,
		},
		{name: "prepared", phase: applyPatchTransactionPhasePrepared, checkpoint: true},
		{
			name: "prepared decision attempted", phase: applyPatchTransactionPhasePrepared,
			decisionAttempted: true, checkpoint: true,
		},
		{
			name: "prepared missing checkpoint", phase: applyPatchTransactionPhasePrepared,
			wantError: true,
		},
		{name: "rolling back", phase: applyPatchTransactionPhaseRollingBack, checkpoint: true},
		{
			name: "rolling back after decision", phase: applyPatchTransactionPhaseRollingBack,
			decisionAttempted: true, checkpoint: true,
		},
		{
			name: "committed", phase: applyPatchTransactionPhaseCommitted,
			decisionAttempted: true, checkpoint: true,
		},
		{
			name: "committed without decision", phase: applyPatchTransactionPhaseCommitted,
			checkpoint: true, wantError: true,
		},
		{name: "unknown", phase: "future", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key, journal := validApplyPatchTransactionJournal(t)
			journal.Phase = test.phase
			journal.DecisionAttempted = test.decisionAttempted
			if test.checkpoint {
				checkpointApplyPatchTransactionJournal(journal)
			}
			if test.phase == applyPatchTransactionPhaseCommitted && test.checkpoint {
				checkpointCommittedApplyPatchTransactionSources(journal)
			}
			_, err := encodeApplyPatchTransactionJournal(key, journal)
			if test.wantError {
				if !errors.Is(err, errApplyPatchTransactionJournalInvalid) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("encode phase: %v", err)
			}
		})
	}
}

func TestApplyPatchTransactionJournalOperationKinds(t *testing.T) {
	tests := []struct {
		name    string
		journal func(*testing.T) ([]byte, *applyPatchTransactionJournal)
		mutate  func(*applyPatchTransactionJournal)
	}{
		{name: "add", journal: validApplyPatchTransactionAddJournal},
		{name: "update", journal: validApplyPatchTransactionJournal},
		{
			name: "delete", journal: validApplyPatchTransactionJournal,
			mutate: func(journal *applyPatchTransactionJournal) {
				journal.Operations[0].Kind = "delete"
				journal.Operations[0].Target = nil
				journal.Operations[0].After = applyPatchTransactionJournalFileState{}
				journal.Artifacts = journal.Artifacts[:5]
			},
		},
		{
			name: "move", journal: validApplyPatchTransactionJournal,
			mutate: func(journal *applyPatchTransactionJournal) {
				journal.Operations[0].Kind = "move"
				journal.Operations[0].Target = &applyPatchTransactionJournalEndpoint{
					Label: "moved.txt",
					CanonicalPath: filepath.Join(
						journal.Workspace.CanonicalPath,
						"moved.txt",
					),
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key, journal := test.journal(t)
			if test.mutate != nil {
				test.mutate(journal)
			}
			encoded, err := encodeApplyPatchTransactionJournal(key, journal)
			if err != nil {
				t.Fatalf("encode %s journal: %v", test.name, err)
			}
			if _, err := decodeApplyPatchTransactionJournal(key, encoded); err != nil {
				t.Fatalf("decode %s journal: %v", test.name, err)
			}
		})
	}
}

func TestApplyPatchTransactionJournalStrictJSON(t *testing.T) {
	key, journal := validApplyPatchTransactionJournal(t)
	encoded, err := encodeApplyPatchTransactionJournal(key, journal)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		data func() []byte
	}{
		{name: "empty", data: func() []byte { return nil }},
		{name: "malformed", data: func() []byte { return []byte(`{"payload":`) }},
		{
			name: "trailing value",
			data: func() []byte { return append(append([]byte(nil), encoded...), []byte(`{}`)...) },
		},
		{name: "duplicate envelope key", data: func() []byte {
			return []byte(`{"payload":{},"payload":{},"hmac_sha256":"` + strings.Repeat("0", 64) + `"}`)
		}},
		{name: "unknown envelope key", data: func() []byte {
			return []byte(`{"payload":{},"hmac_sha256":"` + strings.Repeat("0", 64) + `","alien":0}`)
		}},
		{name: "duplicate payload key", data: func() []byte {
			duplicate := bytes.Replace(payload, []byte(`{"version":1`), []byte(`{"version":1,"version":1`), 1)
			return authenticatedApplyPatchTransactionTestEnvelope(key, applyPatchTransactionJournalMACDomain, duplicate)
		}},
		{name: "unknown payload key", data: func() []byte {
			unknown := bytes.Replace(payload, []byte(`{"version":1`), []byte(`{"alien":0,"version":1`), 1)
			return authenticatedApplyPatchTransactionTestEnvelope(key, applyPatchTransactionJournalMACDomain, unknown)
		}},
		{name: "unknown nested key", data: func() []byte {
			unknown := bytes.Replace(
				payload,
				[]byte(`"workspace":{"canonical_path"`),
				[]byte(`"workspace":{"alien":0,"canonical_path"`),
				1,
			)
			return authenticatedApplyPatchTransactionTestEnvelope(key, applyPatchTransactionJournalMACDomain, unknown)
		}},
		{name: "noncanonical payload whitespace", data: func() []byte {
			noncanonical := bytes.Replace(payload, []byte(`{"version":1`), []byte(`{ "version":1`), 1)
			return authenticatedApplyPatchTransactionTestEnvelope(
				key,
				applyPatchTransactionJournalMACDomain,
				noncanonical,
			)
		}},
		{name: "noncanonical envelope whitespace", data: func() []byte {
			return append([]byte{' '}, encoded...)
		}},
		{name: "uppercase MAC", data: func() []byte {
			encodedCopy := append([]byte(nil), encoded...)
			marker := []byte(`"hmac_sha256":"`)
			start := bytes.LastIndex(encodedCopy, marker) + len(marker)
			encodedCopy[start] = 'A'
			return encodedCopy
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeApplyPatchTransactionJournal(key, test.data()); !errors.Is(
				err,
				errApplyPatchTransactionJournalInvalid,
			) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestApplyPatchTransactionJournalVersionPhaseAndBindingRejection(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*applyPatchTransactionJournal)
	}{
		{name: "old version", mutate: func(journal *applyPatchTransactionJournal) { journal.Version = 0 }},
		{name: "new version", mutate: func(journal *applyPatchTransactionJournal) { journal.Version++ }},
		{name: "phase", mutate: func(journal *applyPatchTransactionJournal) { journal.Phase = "future" }},
		{
			name:   "workspace digest",
			mutate: func(journal *applyPatchTransactionJournal) { journal.Workspace.PathSHA256 = strings.Repeat("0", 64) },
		},
		{
			name:   "workspace identity",
			mutate: func(journal *applyPatchTransactionJournal) { journal.Workspace.Identity = applyPatchTxnIdentity{} },
		},
		{
			name:   "root identity kind",
			mutate: func(journal *applyPatchTransactionJournal) { journal.State.RootIdentity.Kind = "regular" },
		},
		{
			name:   "key identity",
			mutate: func(journal *applyPatchTransactionJournal) { journal.State.KeyID = strings.Repeat("0", 64) },
		},
		{name: "state directory", mutate: func(journal *applyPatchTransactionJournal) {
			journal.State.WorkspaceDirectory = journal.Workspace.PathSHA256
		}},
		{
			name:   "active name",
			mutate: func(journal *applyPatchTransactionJournal) { journal.State.ActiveDirectory = "../active" },
		},
		{name: "duplicate cleanup name", mutate: func(journal *applyPatchTransactionJournal) {
			journal.State.CommittedDirectory = journal.State.ActiveDirectory
		}},
		{name: "state in workspace", mutate: func(journal *applyPatchTransactionJournal) {
			journal.State.CanonicalRoot = filepath.Join(journal.Workspace.CanonicalPath, "state")
		}},
		{
			name:   "transaction ID",
			mutate: func(journal *applyPatchTransactionJournal) { journal.TransactionID = strings.Repeat("A", 32) },
		},
		{name: "null operations", mutate: func(journal *applyPatchTransactionJournal) { journal.Operations = nil }},
		{name: "operation count", mutate: func(journal *applyPatchTransactionJournal) { journal.OperationCount++ }},
		{
			name:   "source identity missing",
			mutate: func(journal *applyPatchTransactionJournal) { journal.Operations[0].Source.PreflightIdentity = nil },
		},
		{name: "source link count missing", mutate: func(journal *applyPatchTransactionJournal) {
			journal.Operations[0].Source.PreflightLinks = 0
		}},
		{name: "source identity zero", mutate: func(journal *applyPatchTransactionJournal) {
			journal.Operations[0].Source.PreflightIdentity = &applyPatchTxnIdentity{}
		}},
		{name: "noncanonical endpoint", mutate: func(journal *applyPatchTransactionJournal) {
			journal.Operations[0].Source.CanonicalPath += string(filepath.Separator) + ".."
		}},
		{name: "protected endpoint", mutate: func(journal *applyPatchTransactionJournal) {
			journal.Operations[0].Source.CanonicalPath = filepath.Join(journal.State.CanonicalRoot, "source")
		}},
		{name: "bad digest", mutate: func(journal *applyPatchTransactionJournal) {
			journal.Operations[0].Before.SHA256 = strings.Repeat("A", 64)
		}},
		{
			name:   "bad mode",
			mutate: func(journal *applyPatchTransactionJournal) { journal.Operations[0].Before.Mode = 0o1000 },
		},
		{
			name:   "absent carries digest",
			mutate: func(journal *applyPatchTransactionJournal) { journal.Operations[0].After.Exists = false },
		},
		{name: "duplicate artifact", mutate: func(journal *applyPatchTransactionJournal) {
			journal.Artifacts = append(journal.Artifacts, journal.Artifacts[0])
		}},
		{
			name:   "unknown artifact role",
			mutate: func(journal *applyPatchTransactionJournal) { journal.Artifacts[0].Role = "future" },
		},
		{
			name:   "missing artifact",
			mutate: func(journal *applyPatchTransactionJournal) { journal.Artifacts = journal.Artifacts[1:] },
		},
		{
			name:   "bad private name",
			mutate: func(journal *applyPatchTransactionJournal) { journal.Artifacts[0].StateName = "../backup" },
		},
		{name: "bad anchor identity", mutate: func(journal *applyPatchTransactionJournal) {
			journal.Artifacts[1].Rooted.AnchorIdentity.Kind = "regular"
		}},
		{name: "bad checkpoint identity", mutate: func(journal *applyPatchTransactionJournal) {
			journal.Artifacts[1].Rooted.Identity = &applyPatchTxnIdentity{Device: 1, File: 2, Kind: "directory"}
		}},
		{name: "checkpoint links without identity", mutate: func(journal *applyPatchTransactionJournal) {
			journal.Artifacts[1].Rooted.Links = 1
		}},
		{name: "checkpoint identity without links", mutate: func(journal *applyPatchTransactionJournal) {
			journal.Artifacts[1].Rooted.Identity = &applyPatchTxnIdentity{
				Device: 1, File: 2, Kind: "regular",
			}
		}},
		{name: "witness link count", mutate: func(journal *applyPatchTransactionJournal) {
			journal.Artifacts[2].Rooted.Identity = journal.Operations[0].Source.PreflightIdentity
			journal.Artifacts[2].Rooted.Links = 1
		}},
		{name: "backup links without identity", mutate: func(journal *applyPatchTransactionJournal) {
			journal.Artifacts[0].StateLinks = 1
		}},
		{
			name:   "backup length",
			mutate: func(journal *applyPatchTransactionJournal) { journal.Artifacts[0].Backup.Length++ },
		},
		{
			name:   "backup MAC",
			mutate: func(journal *applyPatchTransactionJournal) { journal.Artifacts[0].Backup.HMACSHA256 = "bad" },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key, journal := validApplyPatchTransactionJournal(t)
			test.mutate(journal)
			payload, err := json.Marshal(journal)
			if err != nil {
				t.Fatal(err)
			}
			encoded := authenticatedApplyPatchTransactionTestEnvelope(
				key,
				applyPatchTransactionJournalMACDomain,
				payload,
			)
			if _, err := decodeApplyPatchTransactionJournal(key, encoded); !errors.Is(
				err,
				errApplyPatchTransactionJournalInvalid,
			) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestApplyPatchTransactionJournalOperationAndLabelBounds(t *testing.T) {
	t.Run("operation inclusive", func(t *testing.T) {
		key, journal := validApplyPatchTransactionAddJournal(t)
		expandApplyPatchTransactionAdds(journal, applyPatchTransactionMaxOperations)
		if _, err := encodeApplyPatchTransactionJournal(key, journal); err != nil {
			t.Fatalf("inclusive operation limit: %v", err)
		}
	})
	t.Run("operation exclusive", func(t *testing.T) {
		key, journal := validApplyPatchTransactionAddJournal(t)
		expandApplyPatchTransactionAdds(journal, applyPatchTransactionMaxOperations+1)
		if err := validateApplyPatchTransactionJournal(key, journal); !errors.Is(
			err,
			errApplyPatchTransactionJournalInvalid,
		) {
			t.Fatalf("exclusive operation limit error = %v", err)
		}
	})
	t.Run("label inclusive", func(t *testing.T) {
		key, journal := validApplyPatchTransactionAddJournal(t)
		journal.Operations[0].Target.Label = strings.Repeat("x", applyPatchCandidateMaxPathBytes)
		if _, err := encodeApplyPatchTransactionJournal(key, journal); err != nil {
			t.Fatalf("inclusive label limit: %v", err)
		}
	})
	t.Run("label exclusive", func(t *testing.T) {
		key, journal := validApplyPatchTransactionAddJournal(t)
		journal.Operations[0].Target.Label = strings.Repeat("x", applyPatchCandidateMaxPathBytes+1)
		if err := validateApplyPatchTransactionJournal(key, journal); !errors.Is(
			err,
			errApplyPatchTransactionJournalInvalid,
		) {
			t.Fatalf("exclusive label limit error = %v", err)
		}
	})
	t.Run("aggregate labels inclusive", func(t *testing.T) {
		key, journal := validApplyPatchTransactionAddJournal(t)
		expandApplyPatchTransactionAdds(journal, 8)
		for index := range journal.Operations {
			journal.Operations[index].Target.Label = strings.Repeat("x", applyPatchCandidateMaxPathBytes)
		}
		if _, err := encodeApplyPatchTransactionJournal(key, journal); err != nil {
			t.Fatalf("inclusive aggregate label limit: %v", err)
		}
	})
	t.Run("aggregate labels exclusive", func(t *testing.T) {
		key, journal := validApplyPatchTransactionAddJournal(t)
		expandApplyPatchTransactionAdds(journal, 9)
		for index := range journal.Operations {
			journal.Operations[index].Target.Label = strings.Repeat("x", applyPatchCandidateMaxPathBytes)
		}
		if err := validateApplyPatchTransactionJournal(key, journal); !errors.Is(
			err,
			errApplyPatchTransactionJournalInvalid,
		) {
			t.Fatalf("exclusive aggregate label limit error = %v", err)
		}
	})
}

func TestApplyPatchTransactionJournalByteAndEntryBounds(t *testing.T) {
	t.Run("input bytes inclusive and exclusive", func(t *testing.T) {
		key, journal := validApplyPatchTransactionAddJournal(t)
		state := journal.Operations[0].After
		state.Length = applyPatchTransactionMaxBackupBytes
		journal.Operations[0].After = state
		for index := range journal.Artifacts {
			journal.Artifacts[index].Expected = state
		}
		if err := validateApplyPatchTransactionJournal(key, journal); err != nil {
			t.Fatalf("inclusive input bytes: %v", err)
		}
		journal.Operations[0].After.Length++
		for index := range journal.Artifacts {
			journal.Artifacts[index].Expected = journal.Operations[0].After
		}
		if err := validateApplyPatchTransactionJournal(key, journal); !errors.Is(
			err,
			errApplyPatchTransactionJournalInvalid,
		) {
			t.Fatalf("exclusive input bytes error = %v", err)
		}
	})
	t.Run("entry inclusive and exclusive", func(t *testing.T) {
		key, journal := validApplyPatchTransactionForestJournal(t)
		expandApplyPatchTransactionForestEntries(
			journal,
			applyPatchTransactionMaxEntries-3,
		)
		if err := validateApplyPatchTransactionJournal(key, journal); err != nil {
			t.Fatalf("inclusive entry count: %v", err)
		}
		expandApplyPatchTransactionForestEntries(
			journal,
			applyPatchTransactionMaxEntries-2,
		)
		if err := validateApplyPatchTransactionJournal(key, journal); !errors.Is(
			err,
			errApplyPatchTransactionJournalInvalid,
		) {
			t.Fatalf("exclusive entry count error = %v", err)
		}
	})
}

func TestApplyPatchTransactionJournalEnvelopeAndJSONBounds(t *testing.T) {
	key, journal := validApplyPatchTransactionJournal(t)

	atLimit := bytes.Repeat([]byte{' '}, applyPatchTransactionJournalMaxBytes)
	if _, err := decodeApplyPatchTransactionJournal(key, atLimit); !errors.Is(
		err,
		errApplyPatchTransactionJournalInvalid,
	) || strings.Contains(err.Error(), "size limit") {
		t.Fatalf("at-limit error = %v", err)
	}
	overLimit := append(atLimit, ' ')
	if _, err := decodeApplyPatchTransactionJournal(key, overLimit); !errors.Is(
		err,
		errApplyPatchTransactionJournalInvalid,
	) || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("over-limit error = %v", err)
	}

	longComponent := strings.Repeat("x", applyPatchTransactionJournalMaxBytes/2)
	journal.Workspace.CanonicalPath = string(filepath.Separator) + "w" + longComponent
	journal.State.CanonicalRoot = string(filepath.Separator) + "s" + longComponent
	refreshApplyPatchTransactionBindings(key, journal)
	for index := range journal.Operations {
		journal.Operations[index].Source.CanonicalPath = filepath.Join(journal.Workspace.CanonicalPath, "source.txt")
		journal.Operations[index].Target.CanonicalPath = journal.Operations[index].Source.CanonicalPath
	}
	for index := range journal.Artifacts {
		if journal.Artifacts[index].Rooted != nil {
			journal.Artifacts[index].Rooted.AnchorCanonicalPath = journal.Workspace.CanonicalPath
		}
	}
	if _, err := encodeApplyPatchTransactionJournal(key, journal); !errors.Is(
		err,
		errApplyPatchTransactionJournalInvalid,
	) || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversize encoded journal error = %v", err)
	}

	for depth := 1; depth <= applyPatchTransactionJSONMaxDepth; depth++ {
		value := bytes.Repeat([]byte{'['}, depth)
		value = append(value, '0')
		value = append(value, bytes.Repeat([]byte{']'}, depth)...)
		err := validateApplyPatchTransactionJSON(value)
		if err != nil {
			t.Fatalf("JSON depth %d: %v", depth, err)
		}
	}
	tooDeep := bytes.Repeat([]byte{'['}, applyPatchTransactionJSONMaxDepth+1)
	tooDeep = append(tooDeep, '0')
	tooDeep = append(tooDeep, bytes.Repeat([]byte{']'}, applyPatchTransactionJSONMaxDepth+1)...)
	if err := validateApplyPatchTransactionJSON(tooDeep); err == nil ||
		!strings.Contains(err.Error(), "nesting") {
		t.Fatalf("too-deep JSON error = %v", err)
	}
}

func TestApplyPatchTransactionJSONCollectionBoundsBeforeTypedDecode(t *testing.T) {
	tests := []struct {
		name      string
		data      string
		wantError bool
	}{
		{
			name: "operations inclusive",
			data: `{"operations":` + applyPatchTransactionTestJSONObjectArray(
				applyPatchTransactionMaxOperations,
			) + `}`,
		},
		{
			name: "operations exclusive",
			data: `{"operations":` + applyPatchTransactionTestJSONObjectArray(
				applyPatchTransactionMaxOperations+1,
			) + `}`,
			wantError: true,
		},
		{
			name: "case-folded operations exclusive",
			data: `{"Operations":` + applyPatchTransactionTestJSONObjectArray(
				applyPatchTransactionMaxOperations+1,
			) + `}`,
			wantError: true,
		},
		{
			name: "operation indexes exclusive",
			data: `{"operation_indexes":` + applyPatchTransactionTestJSONObjectArray(
				applyPatchTransactionMaxOperations+1,
			) + `}`,
			wantError: true,
		},
		{
			name: "declared entries inclusive",
			data: `{"forests":[{"entries":` + applyPatchTransactionTestJSONObjectArray(
				applyPatchTransactionMaxEntries-3,
			) + `}]}`,
		},
		{
			name: "declared entries exclusive",
			data: `{"forests":[{"entries":` + applyPatchTransactionTestJSONObjectArray(
				applyPatchTransactionMaxEntries-2,
			) + `}]}`,
			wantError: true,
		},
		{
			name: "arbitrary array exclusive",
			data: `{"unknown":` + applyPatchTransactionTestJSONObjectArray(
				applyPatchTransactionMaxEntries+1,
			) + `}`,
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateApplyPatchTransactionJSON([]byte(test.data))
			if test.wantError && err == nil {
				t.Fatal("oversize collection was accepted")
			}
			if !test.wantError && err != nil {
				t.Fatalf("inclusive collection: %v", err)
			}
		})
	}
}

func TestApplyPatchTransactionPointerRoundTripAndBounds(t *testing.T) {
	key, journal := validApplyPatchTransactionJournal(t)
	journalBytes, err := encodeApplyPatchTransactionJournal(key, journal)
	if err != nil {
		t.Fatal(err)
	}
	journalDigest := sha256.Sum256(journalBytes)
	pointer := &applyPatchTransactionPointer{
		Version:           applyPatchTransactionPointerVersion,
		Workspace:         journal.Workspace,
		State:             journal.State,
		TransactionID:     journal.TransactionID,
		Phase:             journal.Phase,
		DecisionAttempted: journal.DecisionAttempted,
		JournalSHA256:     hex.EncodeToString(journalDigest[:]),
	}
	first, err := encodeApplyPatchTransactionPointer(key, pointer)
	if err != nil {
		t.Fatalf("encode pointer: %v", err)
	}
	second, err := encodeApplyPatchTransactionPointer(key, pointer)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("pointer deterministic encoding = %v, %v", bytes.Equal(first, second), err)
	}
	decoded, err := decodeApplyPatchTransactionPointer(key, first)
	if err != nil {
		t.Fatalf("decode pointer: %v", err)
	}
	if !reflect.DeepEqual(decoded, pointer) {
		t.Fatalf("decoded pointer = %#v, want %#v", decoded, pointer)
	}

	tampered := append([]byte(nil), first...)
	tampered[bytes.Index(tampered, []byte(pointer.TransactionID))] ^= 1
	if _, err := decodeApplyPatchTransactionPointer(key, tampered); !errors.Is(
		err,
		errApplyPatchTransactionPointerAuthentication,
	) {
		t.Fatalf("tampered pointer error = %v", err)
	}

	atLimit := bytes.Repeat([]byte{' '}, applyPatchTransactionPointerMaxBytes)
	if _, err := decodeApplyPatchTransactionPointer(key, atLimit); !errors.Is(
		err,
		errApplyPatchTransactionPointerInvalid,
	) || strings.Contains(err.Error(), "size limit") {
		t.Fatalf("pointer at-limit error = %v", err)
	}
	if _, err := decodeApplyPatchTransactionPointer(key, append(atLimit, ' ')); !errors.Is(
		err,
		errApplyPatchTransactionPointerInvalid,
	) || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("pointer over-limit error = %v", err)
	}

	pointer.Workspace.CanonicalPath = string(filepath.Separator) + "w" + strings.Repeat("x", 5000)
	pointer.State.CanonicalRoot = string(filepath.Separator) + "s" + strings.Repeat("x", 5000)
	refreshApplyPatchTransactionPointerBindings(key, pointer)
	if _, err := encodeApplyPatchTransactionPointer(key, pointer); !errors.Is(
		err,
		errApplyPatchTransactionPointerInvalid,
	) || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversize encoded pointer error = %v", err)
	}
}

func TestApplyPatchTransactionPointerStrictValidation(t *testing.T) {
	key, journal := validApplyPatchTransactionJournal(t)
	journalBytes, err := encodeApplyPatchTransactionJournal(key, journal)
	if err != nil {
		t.Fatal(err)
	}
	journalDigest := sha256.Sum256(journalBytes)
	base := &applyPatchTransactionPointer{
		Version:       applyPatchTransactionPointerVersion,
		Workspace:     journal.Workspace,
		State:         journal.State,
		TransactionID: journal.TransactionID,
		Phase:         journal.Phase,
		JournalSHA256: hex.EncodeToString(journalDigest[:]),
	}
	tests := []struct {
		name   string
		mutate func(*applyPatchTransactionPointer)
	}{
		{name: "old version", mutate: func(pointer *applyPatchTransactionPointer) {
			pointer.Version = 0
		}},
		{name: "new version", mutate: func(pointer *applyPatchTransactionPointer) {
			pointer.Version++
		}},
		{name: "unknown phase", mutate: func(pointer *applyPatchTransactionPointer) {
			pointer.Phase = "future"
		}},
		{name: "preparing decision", mutate: func(pointer *applyPatchTransactionPointer) {
			pointer.DecisionAttempted = true
		}},
		{name: "bad journal digest", mutate: func(pointer *applyPatchTransactionPointer) {
			pointer.JournalSHA256 = strings.Repeat("A", 64)
		}},
		{name: "binding mismatch", mutate: func(pointer *applyPatchTransactionPointer) {
			pointer.Workspace.PathSHA256 = strings.Repeat("0", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pointer := *base
			test.mutate(&pointer)
			payload, marshalErr := json.Marshal(&pointer)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			encoded := authenticatedApplyPatchTransactionTestEnvelope(
				key,
				applyPatchTransactionPointerMACDomain,
				payload,
			)
			if _, decodeErr := decodeApplyPatchTransactionPointer(key, encoded); !errors.Is(
				decodeErr,
				errApplyPatchTransactionPointerInvalid,
			) {
				t.Fatalf("error = %v", decodeErr)
			}
		})
	}

	pointerPayload, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	wrongDomain := authenticatedApplyPatchTransactionTestEnvelope(
		key,
		applyPatchTransactionJournalMACDomain,
		pointerPayload,
	)
	if _, decodeErr := decodeApplyPatchTransactionPointer(key, wrongDomain); !errors.Is(
		decodeErr,
		errApplyPatchTransactionPointerAuthentication,
	) {
		t.Fatalf("cross-domain pointer error = %v", decodeErr)
	}
}

func TestApplyPatchTransactionBackupAuthenticationAndBounds(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, applyPatchTransactionKeyBytes)
	transactionID := strings.Repeat("a", applyPatchTransactionIDHexBytes)
	name := applyPatchTransactionTestPrivateName("backup", "standalone")
	data := []byte("detached preimage")
	record, err := newApplyPatchTransactionBackupRecord(key, transactionID, name, data)
	if err != nil {
		t.Fatalf("new backup record: %v", err)
	}
	if err := verifyApplyPatchTransactionBackup(key, transactionID, name, record, data); err != nil {
		t.Fatalf("verify backup: %v", err)
	}

	tests := []struct {
		name   string
		key    []byte
		txn    string
		state  string
		record applyPatchTransactionBackupRecord
		data   []byte
	}{
		{
			name:   "wrong key",
			key:    bytes.Repeat([]byte{1}, 32),
			txn:    transactionID,
			state:  name,
			record: record,
			data:   data,
		},
		{name: "wrong transaction", key: key, txn: strings.Repeat("b", 32), state: name, record: record, data: data},
		{
			name: "wrong name", key: key, txn: transactionID,
			state: applyPatchTransactionTestPrivateName("backup", "other"), record: record, data: data,
		},
		{
			name:   "wrong bytes",
			key:    key,
			txn:    transactionID,
			state:  name,
			record: record,
			data:   []byte("detached preimagf"),
		},
		{
			name:   "wrong length",
			key:    key,
			txn:    transactionID,
			state:  name,
			record: func() applyPatchTransactionBackupRecord { changed := record; changed.Length++; return changed }(),
			data:   data,
		},
		{
			name:  "wrong digest",
			key:   key,
			txn:   transactionID,
			state: name,
			record: func() applyPatchTransactionBackupRecord {
				changed := record
				changed.SHA256 = strings.Repeat("0", 64)
				return changed
			}(),
			data: data,
		},
		{
			name:  "wrong MAC",
			key:   key,
			txn:   transactionID,
			state: name,
			record: func() applyPatchTransactionBackupRecord {
				changed := record
				changed.HMACSHA256 = strings.Repeat("0", 64)
				return changed
			}(),
			data: data,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := verifyApplyPatchTransactionBackup(
				test.key,
				test.txn,
				test.state,
				test.record,
				test.data,
			); !errors.Is(err, errApplyPatchTransactionBackupInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	exact := bytes.Repeat([]byte{'x'}, applyPatchTransactionMaxBackupBytes)
	if _, err := newApplyPatchTransactionBackupRecord(key, transactionID, name, exact); err != nil {
		t.Fatalf("inclusive backup size: %v", err)
	}
	if _, err := newApplyPatchTransactionBackupRecord(key, transactionID, name, append(exact, 'x')); !errors.Is(
		err,
		errApplyPatchTransactionBackupInvalid,
	) {
		t.Fatalf("exclusive backup size error = %v", err)
	}
}

func TestApplyPatchTransactionForestManifestAndWitnessValidation(t *testing.T) {
	key, journal := validApplyPatchTransactionForestJournal(t)
	if _, err := encodeApplyPatchTransactionJournal(key, journal); err != nil {
		t.Fatalf("encode preparing forest: %v", err)
	}

	prepared := cloneApplyPatchTransactionJournal(t, journal)
	prepared.Phase = applyPatchTransactionPhasePrepared
	checkpointApplyPatchTransactionJournal(prepared)
	if _, err := encodeApplyPatchTransactionJournal(key, prepared); err != nil {
		t.Fatalf("encode prepared forest: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*applyPatchTransactionJournal)
	}{
		{name: "missing root entry", mutate: func(journal *applyPatchTransactionJournal) {
			journal.Forests[0].Entries = journal.Forests[0].Entries[1:]
		}},
		{
			name:   "unbound operation",
			mutate: func(journal *applyPatchTransactionJournal) { journal.Forests[0].OperationIndexes = []int{1} },
		},
		{
			name:   "bad relative path",
			mutate: func(journal *applyPatchTransactionJournal) { journal.Forests[0].Entries[1].RelativePath = "../z.txt" },
		},
		{name: "path mismatch", mutate: func(journal *applyPatchTransactionJournal) {
			journal.Forests[0].Entries[1].CanonicalPath = filepath.Join(journal.Forests[0].PublicRoot, "other.txt")
		}},
		{name: "missing parent manifest", mutate: func(journal *applyPatchTransactionJournal) {
			entry := journal.Forests[0].Entries[1]
			entry.RelativePath = "nested/z.txt"
			entry.CanonicalPath = filepath.Join(journal.Forests[0].PublicRoot, "nested", "z.txt")
			journal.Operations[0].Target.CanonicalPath = entry.CanonicalPath
			journal.Forests[0].SentinelRelativePath = entry.RelativePath
			journal.Forests[0].Entries[1] = entry
		}},
		{
			name:   "sentinel directory",
			mutate: func(journal *applyPatchTransactionJournal) { journal.Forests[0].SentinelRelativePath = "." },
		},
		{
			name:   "sentinel identity mismatch",
			mutate: func(journal *applyPatchTransactionJournal) { journal.Forests[0].SentinelWitness.Identity.File++ },
		},
		{
			name:   "root identity mismatch",
			mutate: func(journal *applyPatchTransactionJournal) { journal.Forests[0].StageRoot.Identity.File++ },
		},
		{
			name:   "anchor mismatch",
			mutate: func(journal *applyPatchTransactionJournal) { journal.Forests[0].RollbackRoot.AnchorIdentity.File++ },
		},
		{name: "duplicate private name", mutate: func(journal *applyPatchTransactionJournal) {
			journal.Forests[0].RollbackRoot.Basename = journal.Forests[0].StageRoot.Basename
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneApplyPatchTransactionJournal(t, prepared)
			test.mutate(candidate)
			if err := validateApplyPatchTransactionJournal(key, candidate); !errors.Is(
				err,
				errApplyPatchTransactionJournalInvalid,
			) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func authenticatedApplyPatchTransactionTestEnvelope(
	key []byte,
	domain string,
	payload []byte,
) []byte {
	return encodeApplyPatchTransactionEnvelope(
		payload,
		hex.EncodeToString(applyPatchTransactionMAC(key, domain, payload)),
	)
}

func validApplyPatchTransactionJournal(
	t *testing.T,
) ([]byte, *applyPatchTransactionJournal) {
	t.Helper()
	key := bytes.Repeat([]byte{0x42}, applyPatchTransactionKeyBytes)
	_, journal := validApplyPatchTransactionAddJournal(t)
	beforeData := []byte("before\n")
	afterData := []byte("after\n")
	before := applyPatchTransactionTestFileState(beforeData, 0o640)
	after := applyPatchTransactionTestFileState(afterData, 0o640)
	identity := &applyPatchTxnIdentity{Device: 10, File: 30, Kind: "regular"}
	path := filepath.Join(journal.Workspace.CanonicalPath, "source.txt")
	journal.Operations = []applyPatchTransactionJournalOperation{{
		Index: 0,
		Kind:  "update",
		Source: &applyPatchTransactionJournalEndpoint{
			Label: "source.txt", CanonicalPath: path,
			PreflightIdentity: identity, PreflightLinks: 1,
		},
		Target: &applyPatchTransactionJournalEndpoint{
			Label: "source.txt", CanonicalPath: path,
			PreflightIdentity: identity, PreflightLinks: 1,
		},
		Before: before,
		After:  after,
	}}
	backupName := applyPatchTransactionTestPrivateName("backup", "000")
	record, err := newApplyPatchTransactionBackupRecord(
		key,
		journal.TransactionID,
		backupName,
		beforeData,
	)
	if err != nil {
		t.Fatal(err)
	}
	journal.Artifacts = []applyPatchTransactionJournalArtifact{
		{
			OperationIndex: 0,
			Role:           applyPatchTransactionArtifactBackupBlob,
			StateName:      backupName,
			Expected:       before,
			Backup:         &record,
		},
		applyPatchTransactionTestRootedArtifact(
			journal,
			0,
			applyPatchTransactionArtifactSourceRestoreStage,
			".picoclaw-restore-000",
			before,
		),
		applyPatchTransactionTestRootedArtifact(
			journal,
			0,
			applyPatchTransactionArtifactSourceProbeWitness,
			".picoclaw-source-probe-witness-000",
			before,
		),
		applyPatchTransactionTestRootedArtifact(
			journal,
			0,
			applyPatchTransactionArtifactSourceWitness,
			".picoclaw-source-witness-000",
			before,
		),
		applyPatchTransactionTestRootedArtifact(
			journal,
			0,
			applyPatchTransactionArtifactSourceQuarantine,
			".picoclaw-source-quarantine-000",
			before,
		),
		applyPatchTransactionTestRootedArtifact(
			journal,
			0,
			applyPatchTransactionArtifactPostimageStage,
			".picoclaw-stage-000",
			after,
		),
		applyPatchTransactionTestRootedArtifact(
			journal,
			0,
			applyPatchTransactionArtifactPostimageWitness,
			".picoclaw-post-witness-000",
			after,
		),
		applyPatchTransactionTestRootedArtifact(
			journal,
			0,
			applyPatchTransactionArtifactTargetRollbackQuarantine,
			".picoclaw-rollback-000",
			after,
		),
	}
	return key, journal
}

func validApplyPatchTransactionAddJournal(
	t *testing.T,
) ([]byte, *applyPatchTransactionJournal) {
	t.Helper()
	key := bytes.Repeat([]byte{0x42}, applyPatchTransactionKeyBytes)
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	stateRoot := filepath.Join(root, "transaction-state")
	workspaceDigest := sha256.Sum256([]byte(workspace))
	workspaceIdentity := applyPatchTxnIdentity{Device: 10, File: 20, Kind: "directory"}
	physicalDigest, err := applyPatchTxnWorkspaceIdentityDigest(workspaceIdentity)
	if err != nil {
		t.Fatal(err)
	}
	keyDigest := sha256.Sum256(key)
	after := applyPatchTransactionTestFileState([]byte("after\n"), 0o644)
	journal := &applyPatchTransactionJournal{
		Version: applyPatchTransactionJournalVersion,
		Workspace: applyPatchTransactionWorkspaceBinding{
			CanonicalPath: workspace,
			PathSHA256:    hex.EncodeToString(workspaceDigest[:]),
			Identity:      workspaceIdentity,
		},
		State: applyPatchTransactionStateBinding{
			CanonicalRoot:      stateRoot,
			RootIdentity:       applyPatchTxnIdentity{Device: 10, File: 21, Kind: "directory"},
			KeyID:              hex.EncodeToString(keyDigest[:]),
			WorkspaceDirectory: "workspaces/" + physicalDigest,
			ActiveDirectory:    "active-" + strings.Repeat("a", applyPatchTransactionIDHexBytes),
			CommittedDirectory: "committed-" + strings.Repeat("a", applyPatchTransactionIDHexBytes),
		},
		TransactionID:  strings.Repeat("a", applyPatchTransactionIDHexBytes),
		Phase:          applyPatchTransactionPhasePreparing,
		OperationCount: 1,
		Operations: []applyPatchTransactionJournalOperation{
			{
				Index: 0,
				Kind:  "add",
				Target: &applyPatchTransactionJournalEndpoint{
					Label:         "added.txt",
					CanonicalPath: filepath.Join(workspace, "added.txt"),
				},
				Before: applyPatchTransactionJournalFileState{},
				After:  after,
			},
		},
		Artifacts: []applyPatchTransactionJournalArtifact{},
		Forests:   []applyPatchTransactionJournalForest{},
	}
	journal.Artifacts = []applyPatchTransactionJournalArtifact{
		applyPatchTransactionTestRootedArtifact(
			journal,
			0,
			applyPatchTransactionArtifactPostimageStage,
			".picoclaw-stage-000",
			after,
		),
		applyPatchTransactionTestRootedArtifact(
			journal,
			0,
			applyPatchTransactionArtifactPostimageWitness,
			".picoclaw-witness-000",
			after,
		),
		applyPatchTransactionTestRootedArtifact(
			journal,
			0,
			applyPatchTransactionArtifactTargetRollbackQuarantine,
			".picoclaw-rollback-000",
			after,
		),
	}
	return key, journal
}

func validApplyPatchTransactionForestJournal(
	t *testing.T,
) ([]byte, *applyPatchTransactionJournal) {
	t.Helper()
	key, journal := validApplyPatchTransactionAddJournal(t)
	root := filepath.Join(journal.Workspace.CanonicalPath, "new-tree")
	target := filepath.Join(root, "z.txt")
	journal.Operations[0].Target.CanonicalPath = target
	journal.Operations[0].ForestID = strings.Repeat("c", applyPatchTransactionIDHexBytes)
	journal.Artifacts = []applyPatchTransactionJournalArtifact{}
	opIndex := 0
	anchorIdentity := applyPatchTxnIdentity{Device: 10, File: 20, Kind: "directory"}
	journal.Forests = []applyPatchTransactionJournalForest{{
		ID:               strings.Repeat("c", applyPatchTransactionIDHexBytes),
		OperationIndexes: []int{0},
		PublicRoot:       root,
		StageRoot: applyPatchTransactionJournalRootedLocation{
			AnchorCanonicalPath: journal.Workspace.CanonicalPath,
			AnchorIdentity:      anchorIdentity,
			Basename:            applyPatchTransactionTestPrivateName("forest-stage", "000"),
			RemovalBasename:     applyPatchTransactionTestPrivateName("remove", "forest-stage-000"),
		},
		RollbackRoot: applyPatchTransactionJournalRootedLocation{
			AnchorCanonicalPath: journal.Workspace.CanonicalPath,
			AnchorIdentity:      anchorIdentity,
			Basename:            applyPatchTransactionTestPrivateName("forest-rollback", "000"),
			RemovalBasename:     applyPatchTransactionTestPrivateName("remove", "forest-rollback-000"),
		},
		SentinelRelativePath: "z.txt",
		SentinelWitness: applyPatchTransactionJournalRootedLocation{
			AnchorCanonicalPath: journal.Workspace.CanonicalPath,
			AnchorIdentity:      anchorIdentity,
			Basename:            applyPatchTransactionTestPrivateName("forest-witness", "000"),
			RemovalBasename:     applyPatchTransactionTestPrivateName("remove", "forest-witness-000"),
		},
		Entries: []applyPatchTransactionJournalForestEntry{
			{RelativePath: ".", CanonicalPath: root, Kind: "directory", Mode: 0o755},
			{
				RelativePath:    "z.txt",
				CanonicalPath:   target,
				Kind:            "file",
				OperationIndex:  &opIndex,
				Mode:            journal.Operations[0].After.Mode,
				Length:          journal.Operations[0].After.Length,
				SHA256:          journal.Operations[0].After.SHA256,
				RemovalBasename: applyPatchTransactionTestPrivateName("remove", "forest-entry-z"),
			},
		},
	}}
	return key, journal
}

func applyPatchTransactionTestRootedArtifact(
	journal *applyPatchTransactionJournal,
	operationIndex int,
	role applyPatchTransactionArtifactRole,
	name string,
	expected applyPatchTransactionJournalFileState,
) applyPatchTransactionJournalArtifact {
	return applyPatchTransactionJournalArtifact{
		OperationIndex: operationIndex,
		Role:           role,
		Rooted: &applyPatchTransactionJournalRootedLocation{
			AnchorCanonicalPath: journal.Workspace.CanonicalPath,
			AnchorIdentity:      journal.Workspace.Identity,
			Basename: applyPatchTransactionTestPrivateName(
				applyPatchTransactionArtifactNamePrefix(role),
				name,
			),
			RemovalBasename: applyPatchTransactionTestPrivateName(
				"remove",
				string(role)+"-"+name,
			),
		},
		Expected: expected,
	}
}

func applyPatchTransactionTestFileState(
	data []byte,
	mode uint32,
) applyPatchTransactionJournalFileState {
	digest := sha256.Sum256(data)
	return applyPatchTransactionJournalFileState{
		Exists: true,
		Length: uint64(len(data)),
		SHA256: hex.EncodeToString(digest[:]),
		Mode:   mode,
	}
}

func checkpointApplyPatchTransactionJournal(journal *applyPatchTransactionJournal) {
	postimageIdentity := &applyPatchTxnIdentity{Device: 10, File: 40, Kind: "regular"}
	backupIdentity := &applyPatchTxnIdentity{Device: 10, File: 41, Kind: "regular"}
	for index := range journal.Artifacts {
		artifact := &journal.Artifacts[index]
		switch artifact.Role {
		case applyPatchTransactionArtifactBackupBlob:
			artifact.StateIdentity = backupIdentity
			artifact.StateLinks = 1
		case applyPatchTransactionArtifactPostimageStage,
			applyPatchTransactionArtifactPostimageWitness:
			artifact.Rooted.Identity = postimageIdentity
			artifact.Rooted.Links = 2
		}
	}
	for index := range journal.Forests {
		forest := &journal.Forests[index]
		rootIdentity := &applyPatchTxnIdentity{Device: 10, File: 50, Kind: "directory"}
		fileIdentity := &applyPatchTxnIdentity{Device: 10, File: 51, Kind: "regular"}
		forest.StageRoot.Identity = rootIdentity
		forest.SentinelWitness.Identity = fileIdentity
		forest.SentinelWitness.Links = 2
		for entryIndex := range forest.Entries {
			entry := &forest.Entries[entryIndex]
			if entry.Kind == "directory" {
				entry.Identity = rootIdentity
			} else {
				entry.Identity = fileIdentity
				if entry.RelativePath == forest.SentinelRelativePath {
					entry.Links = 2
				} else {
					entry.Links = 1
				}
			}
		}
	}
}

func checkpointCommittedApplyPatchTransactionSources(journal *applyPatchTransactionJournal) {
	for index := range journal.Artifacts {
		artifact := &journal.Artifacts[index]
		if artifact.Role == applyPatchTransactionArtifactSourceWitness ||
			artifact.Role == applyPatchTransactionArtifactSourceQuarantine {
			identity := *journal.Operations[artifact.OperationIndex].Source.PreflightIdentity
			artifact.Rooted.Identity = &identity
			artifact.Rooted.Links = 2
		}
	}
}

func cloneApplyPatchTransactionJournal(
	t *testing.T,
	journal *applyPatchTransactionJournal,
) *applyPatchTransactionJournal {
	t.Helper()
	data, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	var clone applyPatchTransactionJournal
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return &clone
}

func expandApplyPatchTransactionAdds(
	journal *applyPatchTransactionJournal,
	count int,
) {
	templateOp := journal.Operations[0]
	templateArtifacts := append([]applyPatchTransactionJournalArtifact(nil), journal.Artifacts...)
	journal.Operations = make([]applyPatchTransactionJournalOperation, count)
	journal.Artifacts = make([]applyPatchTransactionJournalArtifact, 0, count*len(templateArtifacts))
	journal.OperationCount = count
	for index := 0; index < count; index++ {
		op := templateOp
		op.Index = index
		op.Target = &applyPatchTransactionJournalEndpoint{
			Label:         fmt.Sprintf("f%03d.txt", index),
			CanonicalPath: filepath.Join(journal.Workspace.CanonicalPath, fmt.Sprintf("f%03d.txt", index)),
		}
		journal.Operations[index] = op
		for artifactIndex := range templateArtifacts {
			artifact := templateArtifacts[artifactIndex]
			artifact.OperationIndex = index
			location := *artifact.Rooted
			location.Basename = applyPatchTransactionTestPrivateName(
				applyPatchTransactionArtifactNamePrefix(artifact.Role),
				fmt.Sprintf("%s-%03d", artifact.Role, index),
			)
			location.RemovalBasename = applyPatchTransactionTestPrivateName(
				"remove",
				fmt.Sprintf("%s-remove-%03d", artifact.Role, index),
			)
			artifact.Rooted = &location
			journal.Artifacts = append(journal.Artifacts, artifact)
		}
	}
}

func expandApplyPatchTransactionForestEntries(
	journal *applyPatchTransactionJournal,
	wanted int,
) {
	forest := &journal.Forests[0]
	root := forest.Entries[0]
	file := forest.Entries[len(forest.Entries)-1]
	entries := make([]applyPatchTransactionJournalForestEntry, 0, wanted)
	entries = append(entries, root)
	for index := 0; index < wanted-2; index++ {
		name := fmt.Sprintf("d%04d", index)
		entries = append(entries, applyPatchTransactionJournalForestEntry{
			RelativePath:  name,
			CanonicalPath: filepath.Join(forest.PublicRoot, name),
			Kind:          "directory",
			Mode:          0o755,
			RemovalBasename: applyPatchTransactionTestPrivateName(
				"remove",
				"expanded-"+name,
			),
		})
	}
	entries = append(entries, file)
	forest.Entries = entries
}

func refreshApplyPatchTransactionBindings(
	key []byte,
	journal *applyPatchTransactionJournal,
) {
	workspaceDigest := sha256.Sum256([]byte(journal.Workspace.CanonicalPath))
	journal.Workspace.PathSHA256 = hex.EncodeToString(workspaceDigest[:])
	physicalDigest, _ := applyPatchTxnWorkspaceIdentityDigest(journal.Workspace.Identity)
	journal.State.WorkspaceDirectory = "workspaces/" + physicalDigest
	keyDigest := sha256.Sum256(key)
	journal.State.KeyID = hex.EncodeToString(keyDigest[:])
}

func refreshApplyPatchTransactionPointerBindings(
	key []byte,
	pointer *applyPatchTransactionPointer,
) {
	workspaceDigest := sha256.Sum256([]byte(pointer.Workspace.CanonicalPath))
	pointer.Workspace.PathSHA256 = hex.EncodeToString(workspaceDigest[:])
	physicalDigest, _ := applyPatchTxnWorkspaceIdentityDigest(pointer.Workspace.Identity)
	pointer.State.WorkspaceDirectory = "workspaces/" + physicalDigest
	keyDigest := sha256.Sum256(key)
	pointer.State.KeyID = hex.EncodeToString(keyDigest[:])
}

func applyPatchTransactionTestPrivateName(prefix, seed string) string {
	digest := sha256.Sum256([]byte(prefix + "\x00" + seed))
	return ".picoclaw-apply-patch-" + prefix + "-" + hex.EncodeToString(digest[:16])
}

func applyPatchTransactionTestJSONObjectArray(count int) string {
	values := make([]string, count)
	for index := range values {
		values[index] = `{}`
	}
	return `[` + strings.Join(values, ",") + `]`
}
