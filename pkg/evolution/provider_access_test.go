package evolution

func init() {
	// Direct provider exercises are package-test-only. Production construction
	// must resolve an opaque StoreID through the runtime broker or hold the
	// appropriate owner fence inside a broker/offline adapter.
	allowUnfencedEvolutionProviderForTests.Store(true)
}
