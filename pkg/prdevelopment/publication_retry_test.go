package prdevelopment

import (
	"math"
	"testing"
	"time"
)

func TestPublicationRetryDelaySaturatesFromClaimCount(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		claims int
		want   time.Duration
	}{
		{name: "negative", claims: -1, want: time.Second},
		{name: "zero", claims: 0, want: time.Second},
		{name: "first", claims: 1, want: time.Second},
		{name: "second", claims: 2, want: 2 * time.Second},
		{name: "third", claims: 3, want: 4 * time.Second},
		{name: "sixth", claims: 6, want: 32 * time.Second},
		{name: "seventh", claims: 7, want: time.Minute},
		{name: "huge", claims: math.MaxInt, want: time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := PublicationRetryDelay(test.claims); got != test.want {
				t.Fatalf("PublicationRetryDelay(%d) = %v, want %v", test.claims, got, test.want)
			}
		})
	}
}
