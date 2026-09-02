//go:build !unix && !windows

package repoaudit

import "context"

func (s Store) LockAutomationController() (func(), error) {
	if s.broker != nil {
		return s.brokerAcquireNamedLease(
			context.Background(),
			reviewLeaseAutomationController,
			reviewNamedLeaseRequest{},
		)
	}
	if err := s.localProviderError(); err != nil {
		return nil, err
	}
	return func() {}, nil
}
