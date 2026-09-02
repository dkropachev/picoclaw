package repoeval

import (
	"context"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
)

type evaluationBrokerClientState struct {
	mu  sync.Mutex
	err error
}

// BrokerLeaseError returns and clears the most recent asynchronous controller
// lease renewal or release failure.
func (s Store) BrokerLeaseError() error { return s.consumeBrokerLeaseError() }

func (s Store) recordBrokerLeaseError(err error) {
	if err == nil || s.brokerState == nil {
		return
	}
	s.brokerState.mu.Lock()
	if s.brokerState.err == nil {
		s.brokerState.err = err
	}
	s.brokerState.mu.Unlock()
}

func (s Store) consumeBrokerLeaseError() error {
	if s.brokerState == nil {
		return nil
	}
	s.brokerState.mu.Lock()
	err := s.brokerState.err
	s.brokerState.err = nil
	s.brokerState.mu.Unlock()
	return err
}

func (s Store) newBrokerLeaseRelease(leaseID string, ttl time.Duration) func() {
	if ttl <= 0 {
		ttl = defaultEvaluationBrokerLeaseTTL
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
			var response evaluationMutationResponse
			err := s.broker.CallWithOptions(
				ctx, evaluationBrokerDomain, evaluationBrokerVersion, evaluationOperationUnlock,
				evaluationLeaseRequest{StoreID: s.StoreID(), LeaseID: leaseID}, &response,
				database.CallOptions{Mutation: true},
			)
			if err == nil && !response.Updated {
				err = database.NewError(database.CodeIntegrity, "evaluation lease release response is invalid")
			}
			s.recordBrokerLeaseError(err)
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
		var response evaluationLeaseResponse
		err := s.broker.CallWithOptions(
			ctx, evaluationBrokerDomain, evaluationBrokerVersion, evaluationOperationRenewLease,
			evaluationLeaseRequest{StoreID: s.StoreID(), LeaseID: leaseID}, &response,
			database.CallOptions{Mutation: true},
		)
		cancel()
		if err == nil && (response.LeaseID != leaseID || response.TTLNanoSeconds <= 0) {
			err = database.NewError(database.CodeIntegrity, "evaluation lease renewal response is invalid")
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
