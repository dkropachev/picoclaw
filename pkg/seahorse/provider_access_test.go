package seahorse

func init() {
	// Direct provider tests are compiled only into this package's test binary.
	// Production local engines require broker or migration ownership fencing.
	allowUnfencedSeahorseProviderForTests.Store(true)
}
