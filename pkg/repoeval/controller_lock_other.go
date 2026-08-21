//go:build !unix && !windows

package repoeval

func (s Store) LockController() (func(), error) { return func() {}, nil }
