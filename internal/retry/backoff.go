package retry

import (
	"math/rand/v2"
	"time"
)

// Backoff returns exponential delay for a 1-based attempt number, plus jitter in [0, jitter].
func Backoff(attempt int, base, jitter time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
	}
	if jitter <= 0 {
		return d
	}
	return d + time.Duration(rand.Int64N(int64(jitter)+1))
}
