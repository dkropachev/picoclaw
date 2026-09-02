package sqlbridge

import (
	"context"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
)

type transactionHeartbeat struct {
	rpc    RPC
	target Target
	id     string
	ttl    time.Duration

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
	errMu    sync.Mutex
	err      error
}

func newTransactionHeartbeat(rpc RPC, target Target, id string, ttl time.Duration) *transactionHeartbeat {
	heartbeat := &transactionHeartbeat{
		rpc: rpc, target: target, id: id, ttl: ttl,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	go heartbeat.run()
	return heartbeat
}

func (heartbeat *transactionHeartbeat) run() {
	defer close(heartbeat.done)
	interval := heartbeat.ttl / 3
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
		case <-heartbeat.stop:
			return
		case <-timer.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), min(interval, 5*time.Second))
		response, err := heartbeat.rpc.Renew(ctx, TransactionRequest{
			Target: heartbeat.target, TransactionID: heartbeat.id,
		})
		cancel()
		if err == nil && (!response.Accepted || response.TTLNanoSeconds <= 0) {
			err = database.NewError(database.CodeIntegrity, "SQL bridge transaction renewal response is invalid")
		}
		if err != nil {
			switch database.CodeOf(err) {
			case database.CodeConflict, database.CodeUnauthorized, database.CodeInvalid,
				database.CodeIntegrity, database.CodeUnsupported:
				heartbeat.errMu.Lock()
				heartbeat.err = err
				heartbeat.errMu.Unlock()
				return
			}
		}
		timer.Reset(interval)
	}
}

func (heartbeat *transactionHeartbeat) stopAndWait() {
	if heartbeat == nil {
		return
	}
	heartbeat.stopOnce.Do(func() { close(heartbeat.stop) })
	<-heartbeat.done
}

func (heartbeat *transactionHeartbeat) failure() error {
	if heartbeat == nil {
		return nil
	}
	heartbeat.errMu.Lock()
	defer heartbeat.errMu.Unlock()
	return heartbeat.err
}
