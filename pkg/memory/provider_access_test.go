package memory

func init() {
	// Package tests exercise the broker-local adapter directly. Production
	// binaries do not compile this authorization switch and must use the broker
	// or hold the online/offline database-owner fence.
	allowUnfencedSessionsProviderForTests.Store(true)
}
