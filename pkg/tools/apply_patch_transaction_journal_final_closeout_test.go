package tools

import (
	"context"
	"errors"
	"testing"
)

func TestApplyPatchTransactionJournalFinalHelperGuards(t *testing.T) {
	key := make([]byte, applyPatchTransactionAuthenticationBytes)
	if _, err := encodeApplyPatchTransactionPointer(nil, nil); err == nil {
		t.Fatal("invalid pointer encoding succeeded")
	}
	if _, err := encodeApplyPatchTransactionAuthenticated(
		nil, "domain", map[string]any{}, 1024, errApplyPatchTransactionJournalInvalid,
	); err == nil {
		t.Fatal("invalid authenticated key encoded")
	}
	if _, err := encodeApplyPatchTransactionAuthenticated(
		key,
		"domain",
		map[string]any{"unsupported": make(chan int)},
		1024,
		errApplyPatchTransactionJournalInvalid,
	); err == nil {
		t.Fatal("unsupported authenticated payload encoded")
	}
	if _, err := encodeApplyPatchTransactionAuthenticated(
		key, "domain", map[string]any{"value": "oversize"}, 1,
		errApplyPatchTransactionJournalInvalid,
	); err == nil {
		t.Fatal("oversize authenticated payload encoded")
	}
	if err := decodeApplyPatchTransactionAuthenticated(
		nil, "domain", []byte("{}"), 1024,
		errApplyPatchTransactionJournalInvalid,
		errApplyPatchTransactionJournalAuthentication,
		&map[string]any{},
	); err == nil {
		t.Fatal("invalid authenticated decode key succeeded")
	}
	if err := validateApplyPatchTransactionJournal(nil, nil); err == nil {
		t.Fatal("invalid journal key succeeded")
	}
	if err := validateApplyPatchTransactionJournal(key, nil); err == nil {
		t.Fatal("nil journal succeeded")
	}
	if got := applyPatchTransactionArtifactNamePrefix("unknown"); got != "" {
		t.Fatalf("unknown artifact prefix = %q", got)
	}
	if err := validateApplyPatchTransactionRootedLocation(nil, "regular"); err == nil {
		t.Fatal("nil rooted location succeeded")
	}
	if err := validateApplyPatchTransactionPrivateBasename("valid-☃"); err == nil {
		t.Fatal("non-ASCII private basename succeeded")
	}
	if err := validateApplyPatchTransactionRandomPrivateName("value", ""); err == nil {
		t.Fatal("empty random-name prefix succeeded")
	}
	if containsApplyPatchTransactionIndex([]int{1, 2}, 3) {
		t.Fatal("missing operation index was reported present")
	}
	if err := decodeApplyPatchTransactionJSON([]byte("{} {}"), &map[string]any{}); err == nil {
		t.Fatal("trailing JSON value succeeded")
	}
}

func TestApplyPatchTransactionStageFinalCloseoutGuards(t *testing.T) {
	if got := intentOperationByIndex(&applyPatchTxnForestIntent{}, 7); got != nil {
		t.Fatalf("missing forest operation = %#v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := stageApplyPatchTxnPostimages(
		ctx,
		&applyPatchTxnIntentPlan{forests: []*applyPatchTxnForestIntent{{}}},
		&applyPatchTransactionJournal{Phase: applyPatchTransactionPhasePreparing},
		func(*applyPatchTransactionJournal) error { return nil },
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled forest staging error = %v", err)
	}
}

func TestApplyPatchTransactionJournalFinalRootedLocationMatrix(t *testing.T) {
	valid := applyPatchTransactionJournalRootedLocation{
		AnchorCanonicalPath: t.TempDir(),
		AnchorIdentity: applyPatchTxnIdentity{
			Device: 1,
			File:   2,
			Kind:   "directory",
		},
		Basename:        applyPatchTransactionTestPrivateName("stage", "final-rooted"),
		RemovalBasename: applyPatchTransactionTestPrivateName("remove", "final-rooted"),
	}
	tests := []struct {
		name string
		kind string
		edit func(*applyPatchTransactionJournalRootedLocation)
	}{
		{
			name: "invalid anchor path",
			kind: "regular",
			edit: func(value *applyPatchTransactionJournalRootedLocation) {
				value.AnchorCanonicalPath = "relative"
			},
		},
		{
			name: "invalid anchor identity",
			kind: "regular",
			edit: func(value *applyPatchTransactionJournalRootedLocation) {
				value.AnchorIdentity = applyPatchTxnIdentity{}
			},
		},
		{
			name: "invalid basename",
			kind: "regular",
			edit: func(value *applyPatchTransactionJournalRootedLocation) {
				value.Basename = "bad/name"
			},
		},
		{
			name: "same removal",
			kind: "regular",
			edit: func(value *applyPatchTransactionJournalRootedLocation) {
				value.RemovalBasename = value.Basename
			},
		},
		{
			name: "wrong checkpoint kind",
			kind: "regular",
			edit: func(value *applyPatchTransactionJournalRootedLocation) {
				value.Identity = &applyPatchTxnIdentity{
					Device: 1,
					File:   3,
					Kind:   "directory",
				}
				value.Links = 1
			},
		},
		{
			name: "links without identity",
			kind: "regular",
			edit: func(value *applyPatchTransactionJournalRootedLocation) {
				value.Links = 1
			},
		},
		{
			name: "removal without identity",
			kind: "regular",
			edit: func(value *applyPatchTransactionJournalRootedLocation) {
				value.RemovalAttempted = true
			},
		},
		{
			name: "zero regular links",
			kind: "regular",
			edit: func(value *applyPatchTransactionJournalRootedLocation) {
				value.Identity = &applyPatchTxnIdentity{
					Device: 1,
					File:   3,
					Kind:   "regular",
				}
			},
		},
		{
			name: "directory links",
			kind: "directory",
			edit: func(value *applyPatchTransactionJournalRootedLocation) {
				value.Identity = &applyPatchTxnIdentity{
					Device: 1,
					File:   3,
					Kind:   "directory",
				}
				value.Links = 1
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.edit(&value)
			if err := validateApplyPatchTransactionRootedLocation(&value, test.kind); err == nil ||
				!errors.Is(err, errApplyPatchTransactionJournalInvalid) {
				t.Fatalf("rooted validation error = %v", err)
			}
		})
	}
}
