package prdevelopment

import (
	"regexp"
	"testing"
	"time"
)

func TestControllerAttemptIdentitiesAreStableAndSeparated(t *testing.T) {
	t.Parallel()
	attemptID := "pdr_0123456789abcdef0123456789abcdef"
	first, err := newControllerAttemptIdentities(attemptID)
	if err != nil {
		t.Fatalf("newControllerAttemptIdentities() error = %v", err)
	}
	second, err := newControllerAttemptIdentities(attemptID)
	if err != nil || first != second {
		t.Fatalf("reconstructed identities = %#v, %v; want %#v", second, err, first)
	}
	operationPattern := regexp.MustCompile(`^pdop_[0-9a-f]{32}$`)
	effectPatterns := map[string]*regexp.Regexp{
		first.CommitIntent: regexp.MustCompile(`^pdcmt_[0-9a-f]{32}$`),
		first.ParkIntent:   regexp.MustCompile(`^pdlnpark_[0-9a-f]{32}$`),
	}
	operations := []string{
		first.AdoptOperation,
		first.ResumeOperation,
		first.CommitOperation,
		first.ParkOperation,
	}
	seen := make(map[string]struct{}, len(operations)+len(effectPatterns))
	for _, identity := range operations {
		if !operationPattern.MatchString(identity) {
			t.Fatalf("operation identity %q is malformed", identity)
		}
		if _, duplicate := seen[identity]; duplicate {
			t.Fatalf("operation identity %q was reused", identity)
		}
		seen[identity] = struct{}{}
	}
	for identity, pattern := range effectPatterns {
		if !pattern.MatchString(identity) {
			t.Fatalf("effect identity %q is malformed", identity)
		}
		if _, duplicate := seen[identity]; duplicate {
			t.Fatalf("effect identity %q was reused", identity)
		}
		seen[identity] = struct{}{}
	}
	if first.CIAttestation == first.CIOwner || first.CIAttestation == "" || first.CIOwner == "" {
		t.Fatalf("local CI identities = %#v", first)
	}

	other, err := newControllerAttemptIdentities(
		"pdr_fedcba9876543210fedcba9876543210",
	)
	if err != nil || other == first || other.CommitIntent == first.CommitIntent ||
		other.ParkIntent == first.ParkIntent {
		t.Fatalf("other attempt identities = %#v, %v; first = %#v", other, err, first)
	}
}

func TestControllerAttemptIdentitiesRejectMalformedAttempt(t *testing.T) {
	t.Parallel()
	for _, attemptID := range []string{
		"",
		" pdr_0123456789abcdef0123456789abcdef",
		"pdr_0123456789ABCDEF0123456789ABCDEF",
		"pdr_0123456789abcdef",
		"other_0123456789abcdef0123456789abcdef",
	} {
		if identities, err := newControllerAttemptIdentities(attemptID); err == nil {
			t.Fatalf("newControllerAttemptIdentities(%q) = %#v, nil", attemptID, identities)
		}
	}
}

func TestControllerCommitTimeIsDurableWholeSecondUTC(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 8, 9, 15, 4, 5, 987654321, time.FixedZone("west", -4*60*60))
	got, err := controllerCommitTime(created)
	if err != nil {
		t.Fatalf("controllerCommitTime() error = %v", err)
	}
	want := time.Date(2026, 8, 9, 19, 4, 5, 0, time.UTC)
	if !got.Equal(want) || got.Location() != time.UTC || got.Nanosecond() != 0 {
		t.Fatalf("controllerCommitTime() = %s, want %s", got, want)
	}
	if _, err = controllerCommitTime(time.Time{}); err == nil {
		t.Fatal("controllerCommitTime(zero) error = nil")
	}
}
