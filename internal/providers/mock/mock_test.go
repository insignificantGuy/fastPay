package mock_test

import (
	"context"
	"testing"
	"time"

	"github.com/insignificantGuy/fastPay/internal/providers"
	"github.com/insignificantGuy/fastPay/internal/providers/mock"
)

func chargeReq() providers.ChargeRequest {
	return providers.ChargeRequest{PaymentID: "pay_1", Amount: 1000, Currency: "USD"}
}

func TestChargeSuccessIsTerminal(t *testing.T) {
	p := mock.New(mock.Config{ID: "a", Available: true, Mode: mock.ModeSuccess})
	res, err := p.Charge(context.Background(), chargeReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != providers.OutcomeSuccess {
		t.Fatalf("outcome = %s, want success", res.Outcome)
	}
	if res.Retryable {
		t.Fatal("success must not be retryable")
	}
	if res.ProviderTxnID == "" {
		t.Fatal("expected provider txn id")
	}
}

func TestChargeDeclineIsTerminal(t *testing.T) {
	p := mock.New(mock.Config{ID: "a", Available: true, Mode: mock.ModeDecline})
	res, err := p.Charge(context.Background(), chargeReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != providers.OutcomeDecline {
		t.Fatalf("outcome = %s, want decline", res.Outcome)
	}
	if res.Retryable {
		t.Fatal("decline must not be retryable")
	}
}

func TestChargeTimeoutIsRetryable(t *testing.T) {
	p := mock.New(mock.Config{
		ID:              "a",
		Available:       true,
		Mode:            mock.ModeTimeout,
		TimeoutDuration: 20 * time.Millisecond,
	})
	start := time.Now()
	res, err := p.Charge(context.Background(), chargeReq())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != providers.OutcomeTimeout {
		t.Fatalf("outcome = %s, want timeout", res.Outcome)
	}
	if !res.Retryable {
		t.Fatal("timeout must be retryable")
	}
	if elapsed < 20*time.Millisecond {
		t.Fatalf("timeout returned too quickly: %s", elapsed)
	}
}

func TestChargeLateWebhookAcceptedThenCallback(t *testing.T) {
	got := make(chan providers.LateWebhookEvent, 1)
	p := mock.New(mock.Config{
		ID:                 "b",
		Available:          true,
		Mode:               mock.ModeLateWebhook,
		LateWebhookDelay:   15 * time.Millisecond,
		LateWebhookSuccess: true,
		OnLateWebhook: func(e providers.LateWebhookEvent) {
			got <- e
		},
	})
	res, err := p.Charge(context.Background(), chargeReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != providers.OutcomeAccepted {
		t.Fatalf("outcome = %s, want accepted", res.Outcome)
	}
	if res.Retryable {
		t.Fatal("accepted must not be classified as retryable for the sync call")
	}
	select {
	case e := <-got:
		if e.PaymentID != "pay_1" || e.ProviderID != "b" || !e.Success {
			t.Fatalf("unexpected webhook event: %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("late webhook callback never fired")
	}
}

func TestProvidersAreIndependentlyConfigurable(t *testing.T) {
	a := mock.New(mock.Config{ID: "provider-a", Available: true, Mode: mock.ModeSuccess})
	b := mock.New(mock.Config{ID: "provider-b", Available: true, Mode: mock.ModeDecline})
	ra, _ := a.Charge(context.Background(), chargeReq())
	rb, _ := b.Charge(context.Background(), chargeReq())
	if ra.Outcome != providers.OutcomeSuccess || rb.Outcome != providers.OutcomeDecline {
		t.Fatalf("a=%s b=%s", ra.Outcome, rb.Outcome)
	}
}

func TestChargeTimeoutHonorsContextCancel(t *testing.T) {
	p := mock.New(mock.Config{
		ID:              "a",
		Available:       true,
		Mode:            mock.ModeTimeout,
		TimeoutDuration: 2 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := p.Charge(ctx, chargeReq())
	if err == nil {
		t.Fatal("expected context error")
	}
}
