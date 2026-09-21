package providers

import "context"

// Outcome is the synchronous result of a provider charge call.
type Outcome string

const (
	OutcomeSuccess  Outcome = "success"
	OutcomeDecline  Outcome = "decline"
	OutcomeTimeout  Outcome = "timeout"
	OutcomeAccepted Outcome = "accepted" // late webhook: sync call accepted, outcome arrives later
)

// ChargeRequest is what callers send to a provider.
type ChargeRequest struct {
	PaymentID string
	Amount    int64
	Currency  string
}

// ChargeResult is classified so Phase 2 retry logic can consume Retryable
// without inspecting provider-specific error strings.
type ChargeResult struct {
	Outcome       Outcome
	Retryable     bool
	ProviderTxnID string
	Message       string
}

// LateWebhookEvent is delivered asynchronously by mocks in late-webhook mode.
type LateWebhookEvent struct {
	PaymentID     string
	ProviderID    string
	ProviderTxnID string
	Success       bool
}

// LateWebhookHandler is invoked in-process after a delay. It is not an HTTP
// endpoint — that belongs to Phase 2.
type LateWebhookHandler func(LateWebhookEvent)

// Provider is the only surface routing and payment code may depend on.
type Provider interface {
	ID() string
	Available() bool
	Charge(ctx context.Context, req ChargeRequest) (ChargeResult, error)
}
