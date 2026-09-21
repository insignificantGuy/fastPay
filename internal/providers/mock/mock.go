package mock

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/insignificantGuy/fastPay/internal/providers"
)

// Mode is a deterministically triggerable charge outcome.
type Mode string

const (
	ModeSuccess     Mode = "success"
	ModeDecline     Mode = "decline"
	ModeTimeout     Mode = "timeout"
	ModeLateWebhook Mode = "late_webhook"
)

type storedCharge struct {
	result providers.ChargeResult
	amount int64
	status string
	key    string
	payID  string
}

// Config is independent per mock instance.
type Config struct {
	ID                 string
	Available          bool
	Mode               Mode
	TimeoutDuration    time.Duration
	LateWebhookDelay   time.Duration
	LateWebhookSuccess bool
	OnLateWebhook      providers.LateWebhookHandler
	TimeoutActuallySucceeded bool
	Gate               <-chan struct{}
	// Sequence, if set, overrides Mode one Charge at a time.
	Sequence []Mode
}

// Provider is an in-process mock. Zero network calls.
type Provider struct {
	mu    sync.Mutex
	cfg   Config
	seq   atomic.Uint64
	byKey map[string]storedCharge
	sleep func(ctx context.Context, d time.Duration) error
}

func New(cfg Config) *Provider {
	if cfg.TimeoutDuration == 0 {
		cfg.TimeoutDuration = 50 * time.Millisecond
	}
	if cfg.LateWebhookDelay == 0 {
		cfg.LateWebhookDelay = 10 * time.Millisecond
	}
	return &Provider{
		cfg:   cfg,
		byKey: map[string]storedCharge{},
		sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
	}
}

func (p *Provider) ID() string { return p.cfg.ID }

func (p *Provider) Available() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg.Available
}

func (p *Provider) SetMode(mode Mode) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.Mode = mode
}

func (p *Provider) SetAvailable(available bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.Available = available
}

func (p *Provider) SetOnLateWebhook(h providers.LateWebhookHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.OnLateWebhook = h
}

func (p *Provider) ChargeCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return int(p.seq.Load())
}

func (p *Provider) Charge(ctx context.Context, req providers.ChargeRequest) (providers.ChargeResult, error) {
	if p.cfg.Gate != nil {
		select {
		case <-ctx.Done():
			return providers.ChargeResult{}, ctx.Err()
		case <-p.cfg.Gate:
		}
	}

	if req.IdempotencyKey != "" {
		p.mu.Lock()
		if existing, ok := p.byKey[req.IdempotencyKey]; ok && existing.status != "timeout" {
			p.mu.Unlock()
			return existing.result, nil
		}
		p.mu.Unlock()
	}

	txnID := fmt.Sprintf("%s-%d", p.cfg.ID, p.seq.Add(1))

	p.mu.Lock()
	mode := p.cfg.Mode
	if len(p.cfg.Sequence) > 0 {
		mode = p.cfg.Sequence[0]
		p.cfg.Sequence = p.cfg.Sequence[1:]
	}
	timeoutSucceeded := p.cfg.TimeoutActuallySucceeded
	handler := p.cfg.OnLateWebhook
	delay := p.cfg.LateWebhookDelay
	lateOK := p.cfg.LateWebhookSuccess
	p.mu.Unlock()

	switch mode {
	case ModeSuccess:
		res := providers.ChargeResult{
			Outcome: providers.OutcomeSuccess, Retryable: false,
			ProviderTxnID: txnID, Message: "approved",
		}
		p.store(req, res, "succeeded")
		return res, nil
	case ModeDecline:
		res := providers.ChargeResult{
			Outcome: providers.OutcomeDecline, Retryable: false,
			ProviderTxnID: txnID, Message: "declined",
		}
		p.store(req, res, "failed")
		return res, nil
	case ModeTimeout:
		if err := p.sleep(ctx, p.cfg.TimeoutDuration); err != nil {
			return providers.ChargeResult{}, err
		}
		res := providers.ChargeResult{
			Outcome: providers.OutcomeTimeout, Retryable: true,
			ProviderTxnID: txnID, Message: "provider timed out",
		}
		if timeoutSucceeded {
			hidden := res
			hidden.Outcome = providers.OutcomeSuccess
			hidden.Retryable = false
			hidden.Message = "approved"
			p.store(req, hidden, "succeeded")
		} else {
			p.store(req, res, "timeout")
		}
		return res, nil
	case ModeLateWebhook:
		event := providers.LateWebhookEvent{
			PaymentID: req.PaymentID, ProviderID: p.cfg.ID,
			ProviderTxnID: txnID, Amount: req.Amount, Success: lateOK,
		}
		res := providers.ChargeResult{
			Outcome: providers.OutcomeAccepted, Retryable: false,
			ProviderTxnID: txnID, Message: "accepted; outcome via webhook",
		}
		status := "pending"
		p.store(req, res, status)
		if handler != nil {
			go func() {
				time.Sleep(delay)
				settled := "failed"
				if event.Success {
					settled = "succeeded"
				}
				p.mu.Lock()
				if req.IdempotencyKey != "" {
					sc := p.byKey[req.IdempotencyKey]
					sc.status = settled
					p.byKey[req.IdempotencyKey] = sc
				}
				p.mu.Unlock()
				handler(event)
			}()
		}
		return res, nil
	default:
		return providers.ChargeResult{}, fmt.Errorf("unknown mock mode %q", mode)
	}
}

func (p *Provider) Lookup(_ context.Context, idempotencyKey string) (providers.LookupResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	sc, ok := p.byKey[idempotencyKey]
	if !ok || sc.status == "timeout" {
		return providers.LookupResult{Found: false}, nil
	}
	return providers.LookupResult{Found: true, Result: sc.result, Amount: sc.amount}, nil
}

func (p *Provider) Report(_ context.Context) ([]providers.ProcessedCharge, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]providers.ProcessedCharge, 0, len(p.byKey))
	for _, sc := range p.byKey {
		if sc.status == "timeout" {
			continue
		}
		out = append(out, providers.ProcessedCharge{
			PaymentID:      sc.payID,
			IdempotencyKey: sc.key,
			Amount:         sc.amount,
			Status:         sc.status,
			ProviderTxnID:  sc.result.ProviderTxnID,
		})
	}
	return out, nil
}

func (p *Provider) store(req providers.ChargeRequest, res providers.ChargeResult, status string) {
	if req.IdempotencyKey == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.byKey[req.IdempotencyKey] = storedCharge{
		result: res, amount: req.Amount, status: status,
		key: req.IdempotencyKey, payID: req.PaymentID,
	}
}
