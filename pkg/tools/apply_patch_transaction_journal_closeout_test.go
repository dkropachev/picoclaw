package tools

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchTransactionJournalCloseoutOperationValidation(t *testing.T) {
	key, base := validApplyPatchTransactionJournal(t)
	mutations := []struct {
		name   string
		mutate func(*applyPatchTransactionJournal)
	}{
		{"wrong operation order", func(j *applyPatchTransactionJournal) { j.Operations[0].Index = 1 }},
		{"before mode", func(j *applyPatchTransactionJournal) { j.Operations[0].Before.Mode = 0o1000 }},
		{"after digest", func(j *applyPatchTransactionJournal) { j.Operations[0].After.SHA256 = "bad" }},
		{"absent state payload", func(j *applyPatchTransactionJournal) {
			j.Operations[0].Before.Exists = false
		}},
		{"before byte overflow", func(j *applyPatchTransactionJournal) {
			j.Operations[0].Before.Length = applyPatchTransactionMaxBackupBytes + 1
		}},
		{"after byte overflow", func(j *applyPatchTransactionJournal) {
			j.Operations[0].After.Length = applyPatchTransactionMaxBackupBytes + 1
		}},
		{"invalid kind", func(j *applyPatchTransactionJournal) { j.Operations[0].Kind = "copy" }},
		{"add has source", func(j *applyPatchTransactionJournal) { j.Operations[0].Kind = "add" }},
		{"delete has target", func(j *applyPatchTransactionJournal) { j.Operations[0].Kind = "delete" }},
		{"update label mismatch", func(j *applyPatchTransactionJournal) {
			j.Operations[0].Target.Label = "other"
		}},
		{"update path mismatch", func(j *applyPatchTransactionJournal) {
			j.Operations[0].Target.CanonicalPath = filepath.Join(j.Workspace.CanonicalPath, "other")
		}},
		{"update identity mismatch", func(j *applyPatchTransactionJournal) {
			j.Operations[0].Target.PreflightIdentity.File++
		}},
		{"move same path", func(j *applyPatchTransactionJournal) {
			j.Operations[0].Kind = "move"
		}},
		{"blank endpoint label", func(j *applyPatchTransactionJournal) {
			j.Operations[0].Source.Label = ""
		}},
		{"endpoint nul label", func(j *applyPatchTransactionJournal) {
			j.Operations[0].Source.Label = "bad\x00label"
		}},
		{"endpoint relative path", func(j *applyPatchTransactionJournal) {
			j.Operations[0].Source.CanonicalPath = "relative"
		}},
		{"endpoint invalid identity", func(j *applyPatchTransactionJournal) {
			j.Operations[0].Source.PreflightIdentity.Kind = "directory"
		}},
		{"endpoint missing links", func(j *applyPatchTransactionJournal) {
			j.Operations[0].Source.PreflightLinks = 0
		}},
		{"endpoint nil identity links", func(j *applyPatchTransactionJournal) {
			j.Operations[0].Source.PreflightIdentity = nil
		}},
		{"protected endpoint", func(j *applyPatchTransactionJournal) {
			j.Operations[0].Source.CanonicalPath = filepath.Join(j.State.CanonicalRoot, "source")
			j.Operations[0].Target.CanonicalPath = j.Operations[0].Source.CanonicalPath
		}},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneApplyPatchTransactionJournal(t, base)
			test.mutate(candidate)
			if err := validateApplyPatchTransactionJournal(key, candidate); err == nil {
				t.Fatal("invalid journal was accepted")
			}
		})
	}

	_, addBase := validApplyPatchTransactionAddJournal(t)
	addMutations := []func(*applyPatchTransactionJournal){
		func(j *applyPatchTransactionJournal) {
			j.Operations[0].Target.PreflightIdentity = &applyPatchTxnIdentity{}
		},
		func(j *applyPatchTransactionJournal) { j.Operations[0].Before.Exists = true },
		func(j *applyPatchTransactionJournal) { j.Operations[0].Target = nil },
	}
	for index, mutate := range addMutations {
		candidate := cloneApplyPatchTransactionJournal(t, addBase)
		mutate(candidate)
		if err := validateApplyPatchTransactionJournal(key, candidate); err == nil {
			t.Fatalf("invalid add %d was accepted", index)
		}
	}
}

func TestApplyPatchTransactionJournalCloseoutArtifactValidation(t *testing.T) {
	key, base := validApplyPatchTransactionJournal(t)
	mutations := []struct {
		name   string
		mutate func(*applyPatchTransactionJournal)
	}{
		{"artifact operation negative", func(j *applyPatchTransactionJournal) { j.Artifacts[0].OperationIndex = -1 }},
		{"duplicate artifact", func(j *applyPatchTransactionJournal) {
			j.Artifacts = append(j.Artifacts, j.Artifacts[0])
		}},
		{"unknown role", func(j *applyPatchTransactionJournal) { j.Artifacts[0].Role = "unknown" }},
		{"wrong expected", func(j *applyPatchTransactionJournal) { j.Artifacts[0].Expected.Mode ^= 1 }},
		{"backup rooted", func(j *applyPatchTransactionJournal) {
			j.Artifacts[0].Rooted = j.Artifacts[1].Rooted
		}},
		{"backup missing state", func(j *applyPatchTransactionJournal) { j.Artifacts[0].StateName = "" }},
		{"backup missing record", func(j *applyPatchTransactionJournal) { j.Artifacts[0].Backup = nil }},
		{"backup duplicate state", func(j *applyPatchTransactionJournal) {
			duplicate := j.Artifacts[0]
			duplicate.Role = applyPatchTransactionArtifactSourceRestoreStage
			duplicate.Rooted = nil
			j.Artifacts = append(j.Artifacts, duplicate)
		}},
		{"backup invalid identity", func(j *applyPatchTransactionJournal) {
			j.Artifacts[0].StateIdentity = &applyPatchTxnIdentity{Device: 1, File: 1, Kind: "directory"}
		}},
		{"backup uncheckpointed links", func(j *applyPatchTransactionJournal) { j.Artifacts[0].StateLinks = 1 }},
		{"backup wrong record", func(j *applyPatchTransactionJournal) { j.Artifacts[0].Backup.Length++ }},
		{"rooted has state", func(j *applyPatchTransactionJournal) { j.Artifacts[1].StateName = "bad" }},
		{"rooted missing", func(j *applyPatchTransactionJournal) { j.Artifacts[1].Rooted = nil }},
		{"rooted protected", func(j *applyPatchTransactionJournal) {
			j.Artifacts[1].Rooted.AnchorCanonicalPath = j.State.CanonicalRoot
		}},
		{"duplicate rooted name", func(j *applyPatchTransactionJournal) {
			j.Artifacts[2].Rooted.Basename = j.Artifacts[1].Rooted.Basename
		}},
		{"invalid rooted anchor", func(j *applyPatchTransactionJournal) {
			j.Artifacts[1].Rooted.AnchorCanonicalPath = "relative"
		}},
		{"invalid rooted anchor identity", func(j *applyPatchTransactionJournal) {
			j.Artifacts[1].Rooted.AnchorIdentity.Kind = "regular"
		}},
		{"invalid rooted basename", func(j *applyPatchTransactionJournal) {
			j.Artifacts[1].Rooted.Basename = "bad/name"
		}},
		{"identity wrong kind", func(j *applyPatchTransactionJournal) {
			j.Artifacts[1].Rooted.Identity = &applyPatchTxnIdentity{Device: 1, File: 1, Kind: "directory"}
		}},
		{"links without identity", func(j *applyPatchTransactionJournal) { j.Artifacts[1].Rooted.Links = 1 }},
		{"removal without identity", func(j *applyPatchTransactionJournal) {
			j.Artifacts[1].Rooted.RemovalAttempted = true
		}},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneApplyPatchTransactionJournal(t, base)
			test.mutate(candidate)
			if err := validateApplyPatchTransactionJournal(key, candidate); err == nil {
				t.Fatal("invalid artifact journal was accepted")
			}
		})
	}
}

func TestApplyPatchTransactionJournalCloseoutForestValidation(t *testing.T) {
	key, base := validApplyPatchTransactionForestJournal(t)
	mutations := []struct {
		name   string
		mutate func(*applyPatchTransactionJournal)
	}{
		{"forest invalid id", func(j *applyPatchTransactionJournal) { j.Forests[0].ID = "bad" }},
		{"forest duplicate", func(j *applyPatchTransactionJournal) { j.Forests = append(j.Forests, j.Forests[0]) }},
		{"forest relative root", func(j *applyPatchTransactionJournal) { j.Forests[0].PublicRoot = "relative" }},
		{"forest protected", func(j *applyPatchTransactionJournal) {
			j.Forests[0].PublicRoot = filepath.Join(j.State.CanonicalRoot, "tree")
		}},
		{"forest nil operations", func(j *applyPatchTransactionJournal) { j.Forests[0].OperationIndexes = nil }},
		{"forest nil entries", func(j *applyPatchTransactionJournal) { j.Forests[0].Entries = nil }},
		{"forest anchor mismatch", func(j *applyPatchTransactionJournal) {
			j.Forests[0].StageRoot.AnchorCanonicalPath = filepath.Dir(j.Forests[0].PublicRoot) + "-other"
		}},
		{"forest identity mismatch", func(j *applyPatchTransactionJournal) {
			j.Forests[0].StageRoot.AnchorIdentity.File++
		}},
		{"forest random name", func(j *applyPatchTransactionJournal) { j.Forests[0].StageRoot.Basename = "bad" }},
		{"forest operation order", func(j *applyPatchTransactionJournal) { j.Forests[0].OperationIndexes[0] = -1 }},
		{"forest operation binding", func(j *applyPatchTransactionJournal) { j.Operations[0].ForestID = "other" }},
		{"forest root entry", func(j *applyPatchTransactionJournal) { j.Forests[0].Entries[0].RelativePath = "root" }},
		{"forest relative path", func(j *applyPatchTransactionJournal) {
			j.Forests[0].Entries[1].RelativePath = "../bad"
		}},
		{"forest path binding", func(j *applyPatchTransactionJournal) {
			j.Forests[0].Entries[1].CanonicalPath = filepath.Join(j.Forests[0].PublicRoot, "other")
		}},
		{"forest entry mode", func(j *applyPatchTransactionJournal) { j.Forests[0].Entries[1].Mode = 0o1000 }},
		{"forest root payload", func(j *applyPatchTransactionJournal) { j.Forests[0].Entries[0].Length = 1 }},
		{"forest directory identity", func(j *applyPatchTransactionJournal) {
			j.Forests[0].Entries[0].Identity = &applyPatchTxnIdentity{
				Device: 1, File: 1, Kind: "regular",
			}
		}},
		{"forest file missing op", func(j *applyPatchTransactionJournal) {
			j.Forests[0].Entries[1].OperationIndex = nil
		}},
		{"forest file digest", func(j *applyPatchTransactionJournal) { j.Forests[0].Entries[1].SHA256 = "bad" }},
		{"forest file state", func(j *applyPatchTransactionJournal) { j.Forests[0].Entries[1].Length++ }},
		{"forest file identity", func(j *applyPatchTransactionJournal) {
			j.Forests[0].Entries[1].Identity = &applyPatchTxnIdentity{
				Device: 1, File: 1, Kind: "directory",
			}
		}},
		{"forest file links no identity", func(j *applyPatchTransactionJournal) {
			j.Forests[0].Entries[1].Links = 1
		}},
		{"forest entry kind", func(j *applyPatchTransactionJournal) {
			j.Forests[0].Entries[1].Kind = "symlink"
		}},
		{"forest sentinel missing", func(j *applyPatchTransactionJournal) {
			j.Forests[0].SentinelRelativePath = "missing"
		}},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneApplyPatchTransactionJournal(t, base)
			test.mutate(candidate)
			if err := validateApplyPatchTransactionJournal(key, candidate); err == nil {
				t.Fatal("invalid forest journal was accepted")
			}
		})
	}
}

func TestApplyPatchTransactionJournalCloseoutCodecAndLimits(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, applyPatchTransactionKeyBytes)
	transactionID := strings.Repeat("a", applyPatchTransactionIDHexBytes)
	backupName := applyPatchTransactionTestPrivateName("backup", "closeout")
	data := []byte("payload")
	record, err := newApplyPatchTransactionBackupRecord(key, transactionID, backupName, data)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		key    []byte
		id     string
		nameV  string
		record applyPatchTransactionBackupRecord
		data   []byte
	}{
		{"bad key", nil, transactionID, backupName, record, data},
		{"bad id", key, "bad", backupName, record, data},
		{"bad name", key, transactionID, "bad", record, data},
		{"bad record", key, transactionID, backupName, applyPatchTransactionBackupRecord{}, data},
		{"length", key, transactionID, backupName, record, append(data, 0)},
		{"digest", key, transactionID, backupName, func() applyPatchTransactionBackupRecord { r := record; r.SHA256 = strings.Repeat("0", 64); return r }(), data},
		{"mac", key, transactionID, backupName, func() applyPatchTransactionBackupRecord {
			r := record
			r.HMACSHA256 = strings.Repeat("0", 64)
			return r
		}(), data},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := verifyApplyPatchTransactionBackup(
				test.key,
				test.id,
				test.nameV,
				test.record,
				test.data,
			); err == nil {
				t.Fatal("invalid backup verified")
			}
		})
	}
	if _, err := newApplyPatchTransactionBackupRecord(nil, transactionID, backupName, data); err == nil {
		t.Fatal("backup with invalid key was created")
	}
	if _, err := newApplyPatchTransactionBackupRecord(key, "bad", backupName, data); err == nil {
		t.Fatal("backup with invalid ID was created")
	}
	if _, err := newApplyPatchTransactionBackupRecord(key, transactionID, "bad", data); err == nil {
		t.Fatal("backup with invalid name was created")
	}
	if _, err := newApplyPatchTransactionBackupRecord(
		key, transactionID, backupName, make([]byte, applyPatchTransactionMaxBackupBytes+1),
	); err == nil {
		t.Fatal("oversize backup was created")
	}

	for _, raw := range [][]byte{
		nil,
		[]byte(`{"x":1} trailing`),
		[]byte(`{"x":1,"x":2}`),
		[]byte(strings.Repeat("[", applyPatchTransactionJSONMaxDepth+1) + "0" + strings.Repeat("]", applyPatchTransactionJSONMaxDepth+1)),
	} {
		if err := validateApplyPatchTransactionJSON(raw); err == nil {
			t.Fatalf("invalid JSON accepted: %q", raw)
		}
	}
	limits := &applyPatchTransactionJSONLimits{}
	if err := (*applyPatchTransactionJSONLimits)(nil).addArrayElement("x", 1); err == nil {
		t.Fatal("nil limits accepted element")
	}
	if err := limits.addArrayElement("operations", applyPatchTransactionMaxOperations+1); err == nil {
		t.Fatal("operation limit was not enforced")
	}
	if err := limits.addArrayElement("forests", applyPatchTransactionMaxOperations+1); err == nil {
		t.Fatal("forest limit was not enforced")
	}
	if err := limits.addArrayElement("other", applyPatchTransactionMaxEntries+1); err == nil {
		t.Fatal("generic array limit was not enforced")
	}
	limits.declaredEntries = applyPatchTransactionMaxEntries
	if err := limits.addArrayElement("artifacts", 1); err == nil {
		t.Fatal("declared-entry limit was not enforced")
	}

	var destination map[string]any
	if err := decodeApplyPatchTransactionJSON([]byte(`{"unknown":1}`), &struct{}{}); err == nil {
		t.Fatal("unknown field decoded")
	}
	if err := decodeApplyPatchTransactionJSON([]byte(`{} {}`), &destination); err == nil {
		t.Fatal("trailing JSON decoded")
	}
	if _, err := encodeApplyPatchTransactionAuthenticated(
		key, "domain", make(chan int), 100, errors.New("invalid"),
	); err == nil {
		t.Fatal("unsupported payload encoded")
	}
	if _, err := encodeApplyPatchTransactionAuthenticated(
		key, "domain", strings.Repeat("x", 100), 10, errors.New("invalid"),
	); err == nil {
		t.Fatal("oversize envelope encoded")
	}
	if err := decodeApplyPatchTransactionAuthenticated(
		key, "domain", []byte(`{}`), 100, errors.New("invalid"), errors.New("auth"), &destination,
	); err == nil {
		t.Fatal("malformed envelope decoded")
	}
}
