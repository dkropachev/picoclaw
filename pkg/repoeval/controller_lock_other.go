//go:build !unix && !windows

package repoeval

func (s Store) LockController() (func(), error) {
	if s.broker != nil {
		return s.brokerLockController()
	}
	if err := s.localProviderError(); err != nil {
		return nil, err
	}
	return func() {}, nil
}
