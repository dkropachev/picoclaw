package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionRecoveryRestoreCloseoutValidationMatrix(t *testing.T) {
	tests := []struct {
		name      string
		content   func([]byte) []byte
		mode      os.FileMode
		extraLink bool
		mutate    func(*applyPatchTransactionJournalArtifact)
		wantErr   bool
	}{
		{
			name:    "valid partial",
			content: func(backup []byte) []byte { return backup[:len(backup)/2] },
			mode:    0o600,
		},
		{
			name:    "invalid prefix",
			content: func(backup []byte) []byte { return []byte("wrong-prefix") },
			mode:    0o600,
			wantErr: true,
		},
		{
			name:    "invalid completed mode",
			content: func(backup []byte) []byte { return append([]byte(nil), backup...) },
			mode:    0o640,
			wantErr: true,
		},
		{
			name:      "untracked hardlink",
			content:   func(backup []byte) []byte { return append([]byte(nil), backup...) },
			mode:      0o751,
			extraLink: true,
			wantErr:   true,
		},
		{
			name:    "wrong checkpoint identity",
			content: func(backup []byte) []byte { return append([]byte(nil), backup...) },
			mode:    0o751,
			mutate: func(restore *applyPatchTransactionJournalArtifact) {
				restore.Rooted.Identity.File++
			},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, tx, operation, restore := prepareApplyPatchTxnBackupFallbackStage(t)
			defer tx.closeHandles()
			backup, backupErr := tx.store.readBackup(tx.key[:], tx.journal, operation.index)
			if backupErr != nil {
				t.Fatal(backupErr)
			}
			file, identity, createErr := applyPatchTxnCreateRegular(
				operation.source.anchor,
				restore.Rooted.Basename,
				0o600,
			)
			if createErr != nil {
				t.Fatal(createErr)
			}
			restore.Rooted.Identity = copyApplyPatchTxnIdentity(identity)
			restore.Rooted.Links = 1
			content := test.content(backup)
			if _, err := file.Write(content); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Chmod(test.mode); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Sync(); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if test.extraLink {
				if err := os.Link(
					filepath.Join(operation.source.anchor.canonical, restore.Rooted.Basename),
					filepath.Join(operation.source.anchor.canonical, "extra-link"),
				); err != nil {
					t.Fatal(err)
				}
			}
			if test.mutate != nil {
				test.mutate(restore)
			}
			aliases, err := collectApplyPatchTxnVirtualRegularAliases(tx.intent, tx.journal)
			if err != nil {
				t.Fatal(err)
			}
			expectedLinks := uint64(len(aliases[identity]))
			err = validateApplyPatchTxnVirtualRestoreStage(
				tx,
				operation.source.anchor,
				restore,
				expectedLinks,
			)
			if test.wantErr && err == nil {
				t.Fatal("invalid restore stage validated")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("valid restore stage = %v", err)
			}
		})
	}
}

func TestApplyPatchTransactionRecoveryRestoreCloseoutAliasKinds(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	anchor, openErr := openApplyPatchTxnAnchor(directory)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer anchor.Close()
	intent := &applyPatchTxnIntentPlan{
		operations: []*applyPatchTxnIntent{{
			source:  &applyPatchTxnEndpoint{anchor: anchor, basename: "missing"},
			planned: plannedApplyPatchOp{sourcePath: filepath.Join(directory, "missing")},
		}},
	}
	journal := &applyPatchTransactionJournal{}
	aliases, aliasErr := collectApplyPatchTxnVirtualRegularAliases(intent, journal)
	if aliasErr != nil || len(aliases) != 0 {
		t.Fatalf("absent alias set = %#v, %v", aliases, aliasErr)
	}
	if err := os.Symlink("missing", filepath.Join(directory, "link")); err != nil {
		t.Fatal(err)
	}
	intent.operations[0].source.basename = "link"
	aliases, aliasErr = collectApplyPatchTxnVirtualRegularAliases(intent, journal)
	if aliasErr != nil || len(aliases) != 0 {
		t.Fatalf("nonregular alias set = %#v, %v", aliases, aliasErr)
	}
}
