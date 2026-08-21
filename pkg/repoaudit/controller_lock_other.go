//go:build !unix && !windows

package repoaudit

func (s Store) LockAutomationController() (func(), error) {
	return func() {}, nil
}
