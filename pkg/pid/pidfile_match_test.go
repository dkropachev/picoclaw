package pid

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRemovePidFileIfMatchFencesPIDAndToken(t *testing.T) {
	const (
		recordedPID   = 424242
		recordedToken = "0123456789abcdef0123456789abcdef"
	)

	tests := []struct {
		name          string
		expectedPID   int
		expectedToken string
		wantRemoved   bool
	}{
		{
			name:          "different PID with matching token is fenced",
			expectedPID:   recordedPID + 1,
			expectedToken: recordedToken,
		},
		{
			name:          "matching PID with different token is fenced",
			expectedPID:   recordedPID,
			expectedToken: "fedcba9876543210fedcba9876543210",
		},
		{
			name:          "token comparison is exact",
			expectedPID:   recordedPID,
			expectedToken: recordedToken + " ",
		},
		{
			name:          "non-positive expected PID is rejected",
			expectedPID:   0,
			expectedToken: recordedToken,
		},
		{
			name:        "empty expected token is rejected",
			expectedPID: recordedPID,
		},
		{
			name:          "exact PID and token remove file",
			expectedPID:   recordedPID,
			expectedToken: recordedToken,
			wantRemoved:   true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, pidFileName)
			raw, err := json.Marshal(PidFileData{PID: recordedPID, Token: recordedToken})
			if err != nil {
				t.Fatalf("Marshal(PidFileData) error = %v", err)
			}
			if err = os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatalf("WriteFile(pid file) error = %v", err)
			}

			removed := RemovePidFileIfMatch(dir, test.expectedPID, test.expectedToken)
			if removed != test.wantRemoved {
				t.Fatalf("RemovePidFileIfMatch() = %v, want %v", removed, test.wantRemoved)
			}

			_, statErr := os.Stat(path)
			if test.wantRemoved {
				if !os.IsNotExist(statErr) {
					t.Fatalf("removed pid file stat error = %v, want not-exist", statErr)
				}
				return
			}
			if statErr != nil {
				t.Fatalf("fenced pid file stat error = %v, want file preserved", statErr)
			}
		})
	}
}

func TestRemovePidFileIfMatchPreservesReplacementGeneration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, pidFileName)
	old := PidFileData{PID: 424242, Token: "old-generation"}
	replacement := PidFileData{PID: old.PID, Token: "replacement-generation"}
	raw, err := json.Marshal(replacement)
	if err != nil {
		t.Fatalf("Marshal(replacement) error = %v", err)
	}
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("WriteFile(replacement) error = %v", err)
	}

	if RemovePidFileIfMatch(dir, old.PID, old.Token) {
		t.Fatal("stale generation removed its same-PID replacement")
	}
	remaining, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(replacement) error = %v", err)
	}
	var got PidFileData
	if err = json.Unmarshal(remaining, &got); err != nil {
		t.Fatalf("Unmarshal(replacement) error = %v", err)
	}
	if got.Token != replacement.Token {
		t.Fatalf("remaining token = %q, want %q", got.Token, replacement.Token)
	}
}
