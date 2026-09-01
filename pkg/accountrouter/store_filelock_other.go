//go:build !unix && !windows

package accountrouter

func lockAccountRouterFile(_ string) (func(), error) { return func() {}, nil }
