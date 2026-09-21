package retry

import (
	"testing"
	"time"
)

func TestBackoffGrowsExponentially(t *testing.T) {
	if got := Backoff(1, 10*time.Millisecond, 0); got != 10*time.Millisecond {
		t.Fatalf("attempt 1: %s", got)
	}
	if got := Backoff(2, 10*time.Millisecond, 0); got != 20*time.Millisecond {
		t.Fatalf("attempt 2: %s", got)
	}
	if got := Backoff(3, 10*time.Millisecond, 0); got != 40*time.Millisecond {
		t.Fatalf("attempt 3: %s", got)
	}
}

func TestBackoffJitterIsBounded(t *testing.T) {
	base, jitter := 10*time.Millisecond, 5*time.Millisecond
	for i := 0; i < 50; i++ {
		got := Backoff(1, base, jitter)
		if got < base || got > base+jitter {
			t.Fatalf("got %s outside [%s, %s]", got, base, base+jitter)
		}
	}
}
