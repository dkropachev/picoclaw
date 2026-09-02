package database

import "sync/atomic"

var inheritedRuntimeClient atomic.Pointer[Client]

// RuntimeClient returns the authenticated broker client installed by the
// private gateway-runtime entrypoint. Ordinary processes receive nil.
func RuntimeClient() *Client { return inheritedRuntimeClient.Load() }

// InstallProcessClient installs the authenticated broker authority selected by
// a trusted launcher or CLI entrypoint.
func InstallProcessClient(client *Client) {
	inheritedRuntimeClient.Store(client)
}
