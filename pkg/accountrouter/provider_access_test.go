package accountrouter

func init() {
	// Package tests exercise the broker-local adapter directly. Production
	// binaries do not compile this switch and therefore cannot bypass the
	// online or exclusive migration ownership fence.
	allowUnfencedAccountRouterProviderForTests.Store(true)
}
