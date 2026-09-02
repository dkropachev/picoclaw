package workflows

func init() {
	// Package tests exercise broker-local storage directly. Production binaries
	// do not compile this switch, so file-backed access still requires the
	// online broker fence or exclusive migration fence.
	allowUnfencedWorkflowProviderForTests.Store(true)
}
