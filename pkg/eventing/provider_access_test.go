package eventing

func init() {
	// Package tests exercise the broker-local adapter directly. Production
	// binaries do not compile this authorization switch and must hold an online
	// or migration fence before any file-backed provider access.
	allowUnfencedProviderForTests.Store(true)
}
