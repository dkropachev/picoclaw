package databasemigration

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBackupManifestJSONPreflight(t *testing.T) {
	if err := preflightBackupManifestJSON(
		mustBackupArchiveManifest(t, validManifestValidationFixture(t)),
	); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		payload string
	}{
		{"top array", `[]`},
		{"unknown top field", `{"unknown":0}`},
		{"duplicate top field", `{"version":1,"version":2}`},
		{"nested scalar", `{"version":[]}`},
		{"null stores", `{"stores":null}`},
		{"nonobject store", `{"stores":[0]}`},
		{"unknown store field", `{"stores":[{"unknown":0}]}`},
		{"duplicate store field", `{"stores":[{"path":"a","path":"b"}]}`},
		{"nonstr legacy root", `{"stores":[{"legacy_roots":[0]}]}`},
		{"nonobject file", `{"files":[0]}`},
		{"unknown file field", `{"files":[{"unknown":0}]}`},
		{"duplicate file field", `{"files":[{"role":"a","role":"b"}]}`},
		{"nested file scalar", `{"files":[{"size":[]}]}`},
		{"nonstr catalog path", `{"catalog_generations":[0]}`},
		{"trailing data", `{} {}`},
		{"truncated", `{"stores":[`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := preflightBackupManifestJSON([]byte(test.payload)); err == nil {
				t.Fatal("malformed manifest JSON passed preflight")
			}
		})
	}
}

func TestBackupManifestJSONPreflightBoundsArrays(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
	}{
		{"stores", backupManifestJSONArrayPayload("stores", `{}`, backupMaxEntries+1)},
		{"files", backupManifestJSONArrayPayload("files", `{}`, backupMaxFiles+1)},
		{"catalog", backupManifestJSONArrayPayload("catalog_generations", `"x"`, backupMaxEntries+1)},
		{"legacy roots", `{"stores":[{"legacy_roots":[` +
			strings.TrimSuffix(strings.Repeat(`"x",`, backupMaxLegacyRoots+1), ",") + `]}]}`},
		{"legacy kinds", `{"stores":[{"legacy_root_kinds":[` +
			strings.TrimSuffix(strings.Repeat(`"file",`, backupMaxLegacyRoots+1), ",") + `]}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := preflightBackupManifestJSON([]byte(test.payload)); err == nil {
				t.Fatal("oversized manifest array passed preflight")
			}
		})
	}
	decoder := json.NewDecoder(strings.NewReader(`null`))
	if _, err := backupManifestJSONToken(
		decoder, &backupManifestJSONBudget{},
	); err == nil {
		t.Fatal("empty JSON token budget succeeded")
	}
}

func backupManifestJSONArrayPayload(name, value string, count int) string {
	return `{"` + name + `":[` + strings.TrimSuffix(strings.Repeat(value+",", count), ",") + `]}`
}
