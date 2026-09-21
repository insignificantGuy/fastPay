package mock

import (
	"context"
	"fmt"
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

// Config is independent per mock instance.
type Config struct {
	ID                 string
	Available          bool
	Mode               Mode
	TimeoutDuration    time.Duration
	LateWebhookDelay   time.Duration
	LateWebhookSuccess bool
	OnLateWebhook      providers.LateWebhookHandler
}

// Provider is an in-process mock. Zero network calls.
type Provider struct {
	cfg   Config
	seq   atomic.Uint64
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
		cfg: cfg,
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

func (p *Provider) Available() bool { return p.cfg.Available }

func (p *Provider) SetMode(mode Mode) { p.cfg.Mode = mode }

func (p *Provider) SetAvailable(available bool) { p.cfg.Available = available }

func (p *Provider) Charge(ctx context.Context, req providers.ChargeRequest) (providers.ChargeResult, error) {
	txnID := fmt.Sprintf("%s-%d", p.cfg.ID, p.seq.Add(1))

	switch p.cfg.Mode {
	case ModeSuccess:
		return providers.ChargeResult{
			Outcome:       providers.OutcomeSuccess,
			Retryable:     false,
			ProviderTxnID: txnID,
			Message:       "approved",
		}, nil
	case ModeDecline:
		return providers.ChargeResult{
			Outcome:       providers.OutcomeDecline,
			Retryable:     false,
			ProviderTxnID: txnID,
			Message:       "declined",
		}, nil
	case ModeTimeout:
		if err := p.sleep(ctx, p.cfg.TimeoutDuration); err != nil {
			return providers.ChargeResult{}, err
		}
		return providers.ChargeResult{
			Outcome:       providers.OutcomeTimeout,
			Retryable:     true,
			ProviderTxnID: txnID,
			Message:       "provider timed out",
		}, nil
	case ModeLateWebhook:
		event := providers.LateWebhookEvent{
			PaymentID:     req.PaymentID,
			ProviderID:    p.cfg.ID,
			ProviderTxnID: txnID,
			Success:       p.cfg.LateWebhookSuccess,
		}
		handler := p.cfg.OnLateWebhook
		delay := p.cfg.LateWebhookDelay
		if handler != nil {
			go func() {
				// Detached from the charge context so a late webhook can still
				// fire after the HTTP request returns (Phase 2 receives it).
				time.Sleep(delay)
				handler(event)
			}()
		}
		return providers.ChargeResult{
			Outcome:       providers.OutcomeAccepted,
			Retryable:     false,
			ProviderTxnID: txnID,
			Message:       "accepted; outcome via webhook",
		}, nil
	default:
		return providers.ChargeResult{}, fmt.Errorf("unknown mock mode %q", p.cfg.Mode)
	}
}
