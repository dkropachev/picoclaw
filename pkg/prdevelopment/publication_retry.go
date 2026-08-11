package prdevelopment

import "time"

const (
	prDevelopmentRetryBase    = time.Second
	prDevelopmentRetryMaximum = time.Minute
)

// PublicationRetryDelay returns the deterministic delay for a publication's
// current claim count. It deliberately has no jitter or retry limit, and
// saturates quickly so corrupt or long-lived counters cannot overflow.
func PublicationRetryDelay(claims int) time.Duration {
	if claims < 1 {
		claims = 1
	}
	delay := prDevelopmentRetryBase
	for claim := 1; claim < claims && delay < prDevelopmentRetryMaximum; claim++ {
		delay *= 2
		if delay > prDevelopmentRetryMaximum {
			delay = prDevelopmentRetryMaximum
		}
	}
	return delay
}
