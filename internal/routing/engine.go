package routing

import (
	"fmt"
	"sync"

	"github.com/insignificantGuy/fastPay/internal/providers"
)

// Payment is the input the engine uses to select a provider.
// Extra fields can be added later without changing the method shape.
type Payment struct {
	Amount   int64
	Currency string
}

// WeightedProvider is rule configuration, not selection logic.
type WeightedProvider struct {
	Provider providers.Provider
	Weight   int
}

// Config lives outside the selection algorithm so weights can change
// without touching engine code.
type Config struct {
	Providers []WeightedProvider
}

// Engine implements deterministic weighted round-robin.
type Engine struct {
	mu    sync.Mutex
	cfg   Config
	index int
}

func NewEngine(cfg Config) *Engine {
	return &Engine{cfg: cfg}
}

// SelectProvider picks the next available provider according to weights.
// Unavailable providers are skipped. If none are available, a clear error
// is returned (never a panic).
func (e *Engine) SelectProvider(payment Payment) (providers.Provider, error) {
	_ = payment

	e.mu.Lock()
	defer e.mu.Unlock()

	sequence := weightedSequence(e.cfg.Providers)
	if len(sequence) == 0 {
		return nil, fmt.Errorf("no providers configured")
	}

	n := len(sequence)
	for i := 0; i < n; i++ {
		p := sequence[e.index%n]
		e.index++
		if p.Available() {
			return p, nil
		}
	}
	return nil, fmt.Errorf("no available providers")
}

func weightedSequence(entries []WeightedProvider) []providers.Provider {
	var seq []providers.Provider
	for _, entry := range entries {
		if entry.Provider == nil || entry.Weight <= 0 {
			continue
		}
		for i := 0; i < entry.Weight; i++ {
			seq = append(seq, entry.Provider)
		}
	}
	return seq
}
