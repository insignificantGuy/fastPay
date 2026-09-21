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
	PaymentID      string
	Amount         int64
	Currency       string
	IdempotencyKey string
}

// ChargeResult is classified so retry logic can consume Retryable
// without inspecting provider-specific error strings.
type ChargeResult struct {
	Outcome       Outcome
	Retryable     bool
	ProviderTxnID string
	Message       string
}

// LookupResult is the provider's record for an idempotency key, used after a
// timeout so we do not retry a charge that already succeeded.
type LookupResult struct {
	Found  bool
	Result ChargeResult
	Amount int64
}

// ProcessedCharge is one row in a mock provider's own report (reconciliation).
type ProcessedCharge struct {
	PaymentID      string
	IdempotencyKey string
	Amount         int64
	Status         string
	ProviderTxnID  string
}

// LateWebhookEvent is delivered asynchronously by mocks in late-webhook mode.
type LateWebhookEvent struct {
	PaymentID     string `json:"payment_id"`
	ProviderID    string `json:"provider_id"`
	ProviderTxnID string `json:"provider_txn_id"`
	Amount        int64  `json:"amount"`
	Success       bool   `json:"success"`
}

// LateWebhookHandler is invoked after a delay. The HTTP receiver lives in the API layer.
type LateWebhookHandler func(LateWebhookEvent)

// Provider is the only surface routing and payment code may depend on.
type Provider interface {
	ID() string
	Available() bool
	Charge(ctx context.Context, req ChargeRequest) (ChargeResult, error)
	Lookup(ctx context.Context, idempotencyKey string) (LookupResult, error)
	Report(ctx context.Context) ([]ProcessedCharge, error)
}
