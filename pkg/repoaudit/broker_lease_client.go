package repoaudit

import (
	"context"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
)

// BrokerLeaseError returns and clears the most recent asynchronous lease
// renewal or release failure. Lease release callbacks predate error returns, so
// this keeps the compatibility signature while making a failed unlock visible
// to callers and to the next acquisition attempt.
func (s Store) BrokerLeaseError() error { return s.consumeBrokerLeaseError() }

func (s Store) recordBrokerLeaseError(err error) {
	if err == nil || s.brokerState == nil {
		return
	}
	s.brokerState.releaseErrMu.Lock()
	if s.brokerState.releaseErr == nil {
		s.brokerState.releaseErr = err
	}
	s.brokerState.releaseErrMu.Unlock()
}

func (s Store) consumeBrokerLeaseError() error {
	if s.brokerState == nil {
		return nil
	}
	s.brokerState.releaseErrMu.Lock()
	err := s.brokerState.releaseErr
	s.brokerState.releaseErr = nil
	s.brokerState.releaseErrMu.Unlock()
	return err
}

func (s Store) newBrokerLeaseRelease(leaseID string, ttl time.Duration, after func()) func() {
	if ttl <= 0 {
		ttl = defaultReviewBrokerLeaseTTL
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go s.renewBrokerLease(stop, done, leaseID, ttl)
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			<-done
			ctx, cancel := context.WithTimeout(context.Background(), min(ttl/2, 5*time.Second))
			defer cancel()
			var response reviewMutationResponse
			err := s.broker.CallWithOptions(
				ctx, reviewBrokerDomain, reviewBrokerVersion, reviewOperationUnlock,
				reviewLeaseRequest{StoreID: s.StoreID(), LeaseID: leaseID}, &response,
				database.CallOptions{Mutation: true},
			)
			if err == nil && !response.Updated {
				err = database.NewError(database.CodeIntegrity, "repository review lease release response is invalid")
			}
			s.recordBrokerLeaseError(err)
			if after != nil {
				after()
			}
		})
	}
}

func (s Store) renewBrokerLease(stop <-chan struct{}, done chan<- struct{}, leaseID string, ttl time.Duration) {
	defer close(done)
	interval := ttl / 3
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	if interval > 10*time.Second {
		interval = 10 * time.Second
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-stop:
			return
		case <-timer.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), min(interval, 5*time.Second))
		var response reviewLeaseResponse
		err := s.broker.CallWithOptions(
			ctx, reviewBrokerDomain, reviewBrokerVersion, reviewOperationRenewLease,
			reviewLeaseRequest{StoreID: s.StoreID(), LeaseID: leaseID}, &response,
			database.CallOptions{Mutation: true},
		)
		cancel()
		if err == nil && (response.LeaseID != leaseID || response.TTLNanoSeconds <= 0) {
			err = database.NewError(database.CodeIntegrity, "repository review lease renewal response is invalid")
		}
		if err != nil {
			switch database.CodeOf(err) {
			case database.CodeConflict, database.CodeUnauthorized, database.CodeInvalid,
				database.CodeIntegrity, database.CodeUnsupported:
				s.recordBrokerLeaseError(err)
				return
			}
		}
		timer.Reset(interval)
	}
}
