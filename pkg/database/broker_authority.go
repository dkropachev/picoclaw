package database

import "sync/atomic"

var brokerProcessAuthority atomic.Bool

// BrokerAuthorityHeld reports whether this process consumed the supervisor's
// one-time authenticated bootstrap proof. An ordinary launcher/runtime online
// fence does not grant provider authority.
func BrokerAuthorityHeld() bool { return brokerProcessAuthority.Load() }

func markBrokerAuthorityHeld() { brokerProcessAuthority.Store(true) }
