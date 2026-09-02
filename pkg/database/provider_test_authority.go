package database

import (
	"sync"
	"sync/atomic"
	"testing"
)

var providerTestAuthoritySuppression atomic.Int64

// ProviderTestAuthorityHeld reports a test-harness-only provider grant.
// testing.Testing is false in every normally built PicoClaw binary, so this
// compatibility path cannot authorize launcher or runtime production code.
func ProviderTestAuthorityHeld() bool {
	return testing.Testing() && providerTestAuthoritySuppression.Load() == 0
}

// SuspendProviderTestAuthority lets focused tests prove the production
// fail-closed path. The returned restore function is idempotent.
func SuspendProviderTestAuthority() func() {
	providerTestAuthoritySuppression.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() { providerTestAuthoritySuppression.Add(-1) })
	}
}
