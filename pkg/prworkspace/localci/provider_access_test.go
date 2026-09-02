package localci

func init() {
	// Direct cache-provider tests are compiled only into this package's test
	// binary. Production callers must use the broker; broker/offline adapters
	// must own their corresponding storage fence.
	allowUnfencedLocalCIProviderForTests.Store(true)
}
