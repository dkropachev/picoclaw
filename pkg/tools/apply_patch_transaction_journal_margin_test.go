package tools

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchTransactionJournalMarginPrimitiveGuards(t *testing.T) {
	key, journal := validApplyPatchTransactionJournal(t)
	wantJournalError := func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, errApplyPatchTransactionJournalInvalid) {
			t.Fatalf("validation error = %v", err)
		}
	}

	populateApplyPatchTxnRemovalNames(nil)
	if err := validateApplyPatchTransactionPointer(key, nil); !errors.Is(
		err,
		errApplyPatchTransactionPointerInvalid,
	) {
		t.Fatalf("nil pointer error = %v", err)
	}

	pointer := &applyPatchTransactionPointer{
		Version:       applyPatchTransactionPointerVersion,
		Workspace:     journal.Workspace,
		State:         journal.State,
		TransactionID: journal.TransactionID,
		Phase:         journal.Phase,
		JournalSHA256: strings.Repeat("b", applyPatchTransactionDigestHexBytes),
	}
	for _, test := range []struct {
		name   string
		mutate func(*applyPatchTransactionPointer)
	}{
		{"transaction ID", func(value *applyPatchTransactionPointer) { value.TransactionID = "bad" }},
		{"state names", func(value *applyPatchTransactionPointer) { value.State.ActiveDirectory = "active-other" }},
	} {
		t.Run("pointer "+test.name, func(t *testing.T) {
			candidate := *pointer
			test.mutate(&candidate)
			if err := validateApplyPatchTransactionPointer(key, &candidate); !errors.Is(
				err,
				errApplyPatchTransactionPointerInvalid,
			) {
				t.Fatalf("validation error = %v", err)
			}
		})
	}

	for _, test := range []struct {
		name   string
		mutate func(*applyPatchTransactionWorkspaceBinding, *applyPatchTransactionStateBinding)
	}{
		{"workspace path", func(workspace *applyPatchTransactionWorkspaceBinding, _ *applyPatchTransactionStateBinding) {
			workspace.CanonicalPath = "relative"
		}},
		{"state root", func(_ *applyPatchTransactionWorkspaceBinding, state *applyPatchTransactionStateBinding) {
			state.CanonicalRoot = "relative"
		}},
		{"committed directory", func(_ *applyPatchTransactionWorkspaceBinding, state *applyPatchTransactionStateBinding) {
			state.CommittedDirectory = "bad/name"
		}},
	} {
		t.Run("binding "+test.name, func(t *testing.T) {
			workspace, state := journal.Workspace, journal.State
			test.mutate(&workspace, &state)
			wantJournalError(t, validateApplyPatchTransactionBindings(
				key,
				workspace,
				state,
				errApplyPatchTransactionJournalInvalid,
			))
		})
	}

	badNames := journal.State
	badNames.CommittedDirectory = "committed-other"
	wantJournalError(t, validateApplyPatchTransactionStateNames(
		journal.TransactionID,
		badNames,
		errApplyPatchTransactionJournalInvalid,
	))
	badJournal := cloneApplyPatchTransactionJournal(t, journal)
	badJournal.State.ActiveDirectory = "active-other"
	wantJournalError(t, validateApplyPatchTransactionJournal(key, badJournal))

	validEndpoint := *journal.Operations[0].Source
	for _, test := range []struct {
		name   string
		mutate func(*applyPatchTransactionJournalEndpoint)
	}{
		{"label", func(value *applyPatchTransactionJournalEndpoint) { value.Label = "" }},
		{"path", func(value *applyPatchTransactionJournalEndpoint) { value.CanonicalPath = "relative" }},
		{"identity", func(value *applyPatchTransactionJournalEndpoint) { value.PreflightIdentity.Kind = "directory" }},
		{"links", func(value *applyPatchTransactionJournalEndpoint) {
			value.PreflightIdentity = nil
			value.PreflightLinks = 1
		}},
	} {
		t.Run("endpoint "+test.name, func(t *testing.T) {
			candidate := validEndpoint
			identity := *validEndpoint.PreflightIdentity
			candidate.PreflightIdentity = &identity
			test.mutate(&candidate)
			wantJournalError(t, validateApplyPatchTransactionEndpoint(&candidate))
		})
	}
}

func TestApplyPatchTransactionJournalMarginResidualGuards(t *testing.T) {
	key, forestBase := validApplyPatchTransactionForestJournal(t)
	t.Run("aggregate entry count", func(t *testing.T) {
		journal := cloneApplyPatchTransactionJournal(t, forestBase)
		expandApplyPatchTransactionForestEntries(journal, applyPatchTransactionMaxEntries+1)
		if err := validateApplyPatchTransactionJournal(key, journal); !errors.Is(
			err,
			errApplyPatchTransactionJournalInvalid,
		) {
			t.Fatalf("entry-count validation error = %v", err)
		}
	})

	_, regularBase := validApplyPatchTransactionJournal(t)
	t.Run("overlapping operation paths", func(t *testing.T) {
		journal := cloneApplyPatchTransactionJournal(t, regularBase)
		second := journal.Operations[0]
		second.Index = 1
		identity := *second.Source.PreflightIdentity
		path := filepath.Join(second.Source.CanonicalPath, "nested")
		second.Source = &applyPatchTransactionJournalEndpoint{
			Label:             "nested",
			CanonicalPath:     path,
			PreflightIdentity: &identity,
			PreflightLinks:    1,
		}
		second.Target = &applyPatchTransactionJournalEndpoint{
			Label:             "nested",
			CanonicalPath:     path,
			PreflightIdentity: &identity,
			PreflightLinks:    1,
		}
		journal.Operations = append(journal.Operations, second)
		journal.OperationCount = 2
		if err := validateApplyPatchTransactionOperations(journal); err == nil {
			t.Fatal("overlapping operation paths were accepted")
		}
	})

	t.Run("duplicate backup state name", func(t *testing.T) {
		journal := cloneApplyPatchTransactionJournal(t, regularBase)
		populateApplyPatchTxnRemovalNames(journal)
		secondOp := journal.Operations[0]
		secondOp.Index = 1
		journal.Operations = append(journal.Operations, secondOp)
		journal.OperationCount = 2
		first := journal.Artifacts[0]
		second := first
		second.OperationIndex = 1
		journal.Artifacts = []applyPatchTransactionJournalArtifact{first, second}
		if err := validateApplyPatchTransactionArtifacts(journal); err == nil {
			t.Fatal("duplicate backup state name was accepted")
		}
	})

	t.Run("aggregate backup byte count", func(t *testing.T) {
		journal := cloneApplyPatchTransactionJournal(t, regularBase)
		secondOp := journal.Operations[0]
		secondOp.Index = 1
		journal.Operations = append(journal.Operations, secondOp)
		journal.OperationCount = 2
		expected := journal.Operations[0].Before
		expected.Length = applyPatchTransactionMaxBackupBytes/2 + 1
		journal.Operations[0].Before = expected
		journal.Operations[1].Before = expected
		first := journal.Artifacts[0]
		first.Expected = expected
		first.Backup.Length = expected.Length
		first.Backup.SHA256 = expected.SHA256
		second := first
		second.OperationIndex = 1
		second.StateName = applyPatchTransactionTestPrivateName("backup", "margin-second")
		secondRecord := *first.Backup
		second.Backup = &secondRecord
		journal.Artifacts = []applyPatchTransactionJournalArtifact{first, second}
		if err := validateApplyPatchTransactionArtifacts(journal); err == nil {
			t.Fatal("aggregate backup byte overflow was accepted")
		}
	})

	t.Run("duplicate rooted artifact name", func(t *testing.T) {
		journal := cloneApplyPatchTransactionJournal(t, regularBase)
		populateApplyPatchTxnRemovalNames(journal)
		secondOp := journal.Operations[0]
		secondOp.Index = 1
		journal.Operations = append(journal.Operations, secondOp)
		journal.OperationCount = 2
		first := journal.Artifacts[1]
		second := first
		second.OperationIndex = 1
		second.Rooted = &applyPatchTransactionJournalRootedLocation{}
		*second.Rooted = *first.Rooted
		journal.Artifacts = []applyPatchTransactionJournalArtifact{first, second}
		if err := validateApplyPatchTransactionArtifacts(journal); err == nil {
			t.Fatal("duplicate rooted artifact name was accepted")
		}
	})

	t.Run("uncheckpointed postimage stage", func(t *testing.T) {
		journal := cloneApplyPatchTransactionJournal(t, regularBase)
		populateApplyPatchTxnRemovalNames(journal)
		journal.Phase = applyPatchTransactionPhasePrepared
		journal.Artifacts[0].StateIdentity = &applyPatchTxnIdentity{
			Device: 10,
			File:   41,
			Kind:   "regular",
		}
		journal.Artifacts[0].StateLinks = 1
		if err := validateApplyPatchTransactionArtifacts(journal); err == nil {
			t.Fatal("uncheckpointed postimage stage was accepted")
		}
	})

	t.Run("unlisted forest operation", func(t *testing.T) {
		journal := cloneApplyPatchTransactionJournal(t, forestBase)
		populateApplyPatchTxnRemovalNames(journal)
		second := journal.Operations[0]
		second.Index = 1
		journal.Operations = append(journal.Operations, second)
		journal.OperationCount = 2
		if err := validateApplyPatchTransactionForests(journal); err == nil {
			t.Fatal("unlisted forest operation was accepted")
		}
	})

	for _, test := range []struct {
		name string
		data []byte
	}{
		{"truncated object key", []byte("{")},
		{"truncated object close", []byte(`{"value":1`)},
		{"truncated array close", []byte(`[1`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateApplyPatchTransactionJSON(test.data); err == nil {
				t.Fatal("truncated JSON was accepted")
			}
		})
	}
	if err := decodeApplyPatchTransactionJSON(
		[]byte(`{} {`),
		&map[string]any{},
	); err == nil {
		t.Fatal("malformed trailing JSON was accepted")
	}
}

func TestApplyPatchTransactionJournalMarginArtifactGuards(t *testing.T) {
	_, base := validApplyPatchTransactionJournal(t)
	clone := func(t *testing.T) *applyPatchTransactionJournal {
		t.Helper()
		journal := cloneApplyPatchTransactionJournal(t, base)
		populateApplyPatchTxnRemovalNames(journal)
		return journal
	}
	wantInvalid := func(t *testing.T, journal *applyPatchTransactionJournal) {
		t.Helper()
		if err := validateApplyPatchTransactionArtifacts(journal); !errors.Is(
			err,
			errApplyPatchTransactionJournalInvalid,
		) {
			t.Fatalf("validation error = %v", err)
		}
	}
	identity := applyPatchTxnIdentity{Device: 10, File: 30, Kind: "regular"}

	t.Run("missing source endpoint", func(t *testing.T) {
		journal := clone(t)
		journal.Operations[0].Source = nil
		wantInvalid(t, journal)
	})
	t.Run("restore phase", func(t *testing.T) {
		journal := clone(t)
		journal.Phase = applyPatchTransactionPhasePrepared
		journal.Artifacts[1].Rooted.Identity = &identity
		journal.Artifacts[1].Rooted.Links = 1
		wantInvalid(t, journal)
	})
	t.Run("probe witness phase", func(t *testing.T) {
		journal := clone(t)
		journal.Phase = applyPatchTransactionPhasePrepared
		journal.Artifacts[2].Rooted.Identity = &identity
		journal.Artifacts[2].Rooted.Links = 2
		wantInvalid(t, journal)
	})
	t.Run("witness links", func(t *testing.T) {
		journal := clone(t)
		journal.Artifacts[3].Rooted.Identity = &identity
		journal.Artifacts[3].Rooted.Links = 1
		wantInvalid(t, journal)
	})
	t.Run("duplicate removal", func(t *testing.T) {
		journal := clone(t)
		journal.Artifacts[2].Rooted.RemovalBasename = journal.Artifacts[1].Rooted.RemovalBasename
		wantInvalid(t, journal)
	})

	t.Run("source identity", func(t *testing.T) {
		journal := clone(t)
		other := identity
		other.File++
		journal.Artifacts[3].Rooted.Identity = &other
		journal.Artifacts[3].Rooted.Links = 2
		wantInvalid(t, journal)
	})
	t.Run("source checkpoint pair", func(t *testing.T) {
		journal := clone(t)
		for _, index := range []int{3, 4} {
			checkpoint := identity
			journal.Artifacts[index].Rooted.Identity = &checkpoint
			journal.Artifacts[index].Rooted.Links = 2
		}
		journal.Artifacts[4].Rooted.Links = 3
		if err := validateApplyPatchTransactionArtifactIdentities(0, journal); err == nil {
			t.Fatal("mismatched source checkpoint pair was accepted")
		}
	})
	t.Run("probe checkpoint pair", func(t *testing.T) {
		journal := clone(t)
		left, right := identity, identity
		right.File++
		journal.Artifacts[1].Rooted.Identity = &left
		journal.Artifacts[1].Rooted.Links = 1
		journal.Artifacts[2].Rooted.Identity = &right
		journal.Artifacts[2].Rooted.Links = 1
		if err := validateApplyPatchTransactionArtifactIdentities(0, journal); err == nil {
			t.Fatal("mismatched probe checkpoint pair was accepted")
		}
	})
	t.Run("postimage checkpoint pair", func(t *testing.T) {
		journal := clone(t)
		left, right := identity, identity
		right.File++
		journal.Artifacts[5].Rooted.Identity = &left
		journal.Artifacts[5].Rooted.Links = 2
		journal.Artifacts[6].Rooted.Identity = &right
		journal.Artifacts[6].Rooted.Links = 2
		if err := validateApplyPatchTransactionArtifactIdentities(0, journal); err == nil {
			t.Fatal("mismatched postimage checkpoint pair was accepted")
		}
	})
	t.Run("committed source identity", func(t *testing.T) {
		journal := clone(t)
		journal.Phase = applyPatchTransactionPhaseCommitted
		if err := validateApplyPatchTransactionArtifactIdentities(0, journal); err == nil {
			t.Fatal("missing committed source identity was accepted")
		}
	})
}

func TestApplyPatchTransactionJournalMarginForestGuards(t *testing.T) {
	_, base := validApplyPatchTransactionForestJournal(t)
	clone := func(t *testing.T) *applyPatchTransactionJournal {
		t.Helper()
		journal := cloneApplyPatchTransactionJournal(t, base)
		populateApplyPatchTxnRemovalNames(journal)
		return journal
	}
	wantInvalid := func(t *testing.T, journal *applyPatchTransactionJournal) {
		t.Helper()
		if err := validateApplyPatchTransactionForests(journal); !errors.Is(
			err,
			errApplyPatchTransactionJournalInvalid,
		) {
			t.Fatalf("validation error = %v", err)
		}
	}

	t.Run("overlap", func(t *testing.T) {
		journal := clone(t)
		second := journal.Forests[0]
		second.ID = strings.Repeat("d", applyPatchTransactionIDHexBytes)
		second.PublicRoot = filepath.Join(journal.Forests[0].PublicRoot, "nested")
		journal.Forests = append(journal.Forests, second)
		wantInvalid(t, journal)
	})
	t.Run("protected artifact", func(t *testing.T) {
		journal := clone(t)
		forest := &journal.Forests[0]
		journal.State.CanonicalRoot = filepath.Join(
			forest.StageRoot.AnchorCanonicalPath,
			forest.StageRoot.Basename,
		)
		wantInvalid(t, journal)
	})
	t.Run("duplicate forest name", func(t *testing.T) {
		journal := clone(t)
		second := journal.Forests[0]
		second.ID = strings.Repeat("d", applyPatchTransactionIDHexBytes)
		second.PublicRoot = filepath.Join(filepath.Dir(second.PublicRoot), "other-tree")
		journal.Forests = append(journal.Forests, second)
		wantInvalid(t, journal)
	})
	t.Run("duplicate forest removal", func(t *testing.T) {
		journal := clone(t)
		forest := &journal.Forests[0]
		forest.RollbackRoot.RemovalBasename = forest.StageRoot.RemovalBasename
		wantInvalid(t, journal)
	})
	t.Run("root checkpoint identity", func(t *testing.T) {
		journal := clone(t)
		forest := &journal.Forests[0]
		forest.StageRoot.Identity = &applyPatchTxnIdentity{Device: 10, File: 50, Kind: "directory"}
		forest.Entries[0].Identity = &applyPatchTxnIdentity{Device: 10, File: 51, Kind: "directory"}
		wantInvalid(t, journal)
	})
	t.Run("uncheckpointed forest", func(t *testing.T) {
		journal := clone(t)
		journal.Phase = applyPatchTransactionPhasePrepared
		wantInvalid(t, journal)
	})
	t.Run("uncheckpointed entry", func(t *testing.T) {
		journal := clone(t)
		checkpointApplyPatchTransactionJournal(journal)
		journal.Phase = applyPatchTransactionPhasePrepared
		journal.Forests[0].Entries[1].Identity = nil
		journal.Forests[0].Entries[1].Links = 0
		wantInvalid(t, journal)
	})
	t.Run("operation forest", func(t *testing.T) {
		journal := clone(t)
		journal.Operations[0].ForestID = strings.Repeat("e", applyPatchTransactionIDHexBytes)
		wantInvalid(t, journal)
	})
	t.Run("forest operation kind", func(t *testing.T) {
		journal := clone(t)
		journal.Operations[0].Kind = "update"
		wantInvalid(t, journal)
	})
}

func TestApplyPatchTransactionJournalMarginForestEntryGuards(t *testing.T) {
	_, base := validApplyPatchTransactionForestJournal(t)
	clone := func(t *testing.T) *applyPatchTransactionJournal {
		t.Helper()
		journal := cloneApplyPatchTransactionJournal(t, base)
		populateApplyPatchTxnRemovalNames(journal)
		return journal
	}
	wantInvalid := func(t *testing.T, journal *applyPatchTransactionJournal) {
		t.Helper()
		forest := &journal.Forests[0]
		if err := validateApplyPatchTransactionForestEntries(journal, forest); !errors.Is(
			err,
			errApplyPatchTransactionJournalInvalid,
		) {
			t.Fatalf("validation error = %v", err)
		}
	}

	for _, test := range []struct {
		name   string
		mutate func(*applyPatchTransactionJournal)
	}{
		{"order", func(journal *applyPatchTransactionJournal) {
			journal.Forests[0].Entries[1].RelativePath = "."
		}},
		{"parent manifest", func(journal *applyPatchTransactionJournal) {
			entry := &journal.Forests[0].Entries[1]
			entry.RelativePath = "missing/z.txt"
			entry.CanonicalPath = filepath.Join(journal.Forests[0].PublicRoot, filepath.FromSlash(entry.RelativePath))
		}},
		{"removal name", func(journal *applyPatchTransactionJournal) {
			journal.Forests[0].Entries[1].RemovalBasename = "bad"
		}},
		{"root removal", func(journal *applyPatchTransactionJournal) {
			journal.Forests[0].Entries[0].RemovalBasename = applyPatchTransactionTestPrivateName("remove", "root")
		}},
		{"removal without identity", func(journal *applyPatchTransactionJournal) {
			journal.Forests[0].Entries[1].RemovalAttempted = true
		}},
		{"entry path", func(journal *applyPatchTransactionJournal) {
			journal.Forests[0].Entries[1].CanonicalPath = "relative"
		}},
		{"file operation", func(journal *applyPatchTransactionJournal) {
			missing := 1
			journal.Forests[0].Entries[1].OperationIndex = &missing
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			journal := clone(t)
			test.mutate(journal)
			wantInvalid(t, journal)
		})
	}

	t.Run("duplicate child and removal", func(t *testing.T) {
		journal := clone(t)
		forest := &journal.Forests[0]
		collidingName := applyPatchTransactionTestPrivateName("remove", "child")
		directory := applyPatchTransactionJournalForestEntry{
			RelativePath:    collidingName,
			CanonicalPath:   filepath.Join(forest.PublicRoot, collidingName),
			Kind:            "directory",
			Mode:            0o755,
			RemovalBasename: applyPatchTransactionTestPrivateName("remove", "directory"),
		}
		forest.Entries = append([]applyPatchTransactionJournalForestEntry{
			forest.Entries[0], directory,
		}, forest.Entries[1])
		forest.Entries[2].RemovalBasename = collidingName
		wantInvalid(t, journal)
	})
}
