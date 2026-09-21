package payment

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/insignificantGuy/fastPay/internal/models"
	"github.com/insignificantGuy/fastPay/internal/providers"
	"github.com/insignificantGuy/fastPay/internal/reconciliation"
	paymentrepo "github.com/insignificantGuy/fastPay/internal/repository/payment"
	"github.com/insignificantGuy/fastPay/internal/retry"
	"github.com/insignificantGuy/fastPay/internal/routing"
)

type Store interface {
	Create(p *models.Payment) error
	Update(p *models.Payment) error
	Get(id string) (*models.Payment, error)
	GetByIdempotencyKey(key string) (*models.Payment, error)
	CreateAttempt(a *models.PaymentAttempt) error
	ListAttempts(paymentID string) ([]models.PaymentAttempt, error)
	UpsertLedger(e *models.LedgerEntry) error
	GetLedger(paymentID string) (*models.LedgerEntry, error)
	ListLedger() ([]models.LedgerEntry, error)
}

type Router interface {
	SelectProvider(payment routing.Payment) (providers.Provider, error)
}

type Config struct {
	MaxAttempts int
	BaseBackoff time.Duration
	Jitter      time.Duration
	Sleep       func(ctx context.Context, d time.Duration) error
	Providers   []providers.Provider
}

type Service struct {
	store  Store
	router Router
	cfg    Config
}

func NewService(store Store, router Router, cfg Config) *Service {
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 3
	}
	if cfg.BaseBackoff == 0 {
		cfg.BaseBackoff = 20 * time.Millisecond
	}
	if cfg.Jitter == 0 {
		cfg.Jitter = 5 * time.Millisecond
	}
	if cfg.Sleep == nil {
		cfg.Sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	return &Service{store: store, router: router, cfg: cfg}
}

type createRequest struct {
	Amount   int64  `json:"amount" binding:"required,gt=0"`
	Currency string `json:"currency" binding:"required,len=3"`
}

type createResponse struct {
	ID         string `json:"id"`
	Amount     int64  `json:"amount"`
	Currency   string `json:"currency"`
	Status     string `json:"status"`
	ProviderID string `json:"provider_id"`
	Message    string `json:"message,omitempty"`
}

func (s *Service) Create(c *gin.Context) {
	key := c.GetHeader("checkoutID")
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "checkoutID header is required"})
		return
	}

	var req createRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	existing, err := s.store.GetByIdempotencyKey(key)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "idempotency lookup failed"})
		return
	}
	if existing != nil {
		s.replay(c, existing)
		return
	}

	p := &models.Payment{
		ID:             uuid.NewString(),
		IdempotencyKey: key,
		Amount:         req.Amount,
		Currency:       req.Currency,
		Status:         models.PaymentStatusPending,
	}
	if err := s.store.Create(p); err != nil {
		if errors.Is(err, paymentrepo.ErrDuplicateIdempotencyKey) {
			dup, lookupErr := s.store.GetByIdempotencyKey(key)
			if lookupErr == nil && dup != nil {
				s.replay(c, dup)
				return
			}
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to persist payment"})
		return
	}

	provider, err := s.router.SelectProvider(routing.Payment{Amount: req.Amount, Currency: req.Currency})
	if err != nil {
		p.Status = models.PaymentStatusFailed
		p.LastOutcome = "error"
		_ = s.store.Update(p)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error(), "id": p.ID, "status": p.Status})
		return
	}
	p.ProviderID = provider.ID()
	_ = s.store.Update(p)

	result, err := s.chargeWithRetry(c.Request.Context(), p, provider, key)
	if err != nil {
		p.Status = models.PaymentStatusFailedPendingReview
		p.LastOutcome = string(providers.OutcomeTimeout)
		_ = s.store.Update(p)
		_ = s.upsertLedger(p)
		c.JSON(http.StatusGatewayTimeout, toResponse(p, err.Error()))
		return
	}

	httpStatus, status, msg := mapOutcome(result, true)
	if !applyStatus(p, status, result.Outcome) {
		fresh, _ := s.store.Get(p.ID)
		if fresh != nil {
			p = fresh
		}
		s.replay(c, p)
		return
	}
	p.LastOutcome = string(result.Outcome)
	if err := s.store.Update(p); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update payment"})
		return
	}
	_ = s.upsertLedger(p)
	c.JSON(httpStatus, toResponse(p, msg))
}

func (s *Service) chargeWithRetry(ctx context.Context, p *models.Payment, provider providers.Provider, key string) (providers.ChargeResult, error) {
	var last providers.ChargeResult
	for attempt := 1; attempt <= s.cfg.MaxAttempts; attempt++ {
		if attempt > 1 {
			looked, err := provider.Lookup(ctx, key)
			if err == nil && looked.Found && !looked.Result.Retryable {
				last = looked.Result
				_ = s.store.CreateAttempt(&models.PaymentAttempt{
					PaymentID: p.ID, AttemptNumber: attempt,
					Outcome: string(last.Outcome), Retryable: false, ProviderTxnID: last.ProviderTxnID,
				})
				return last, nil
			}
			if err := s.cfg.Sleep(ctx, retry.Backoff(attempt-1, s.cfg.BaseBackoff, s.cfg.Jitter)); err != nil {
				return last, err
			}
		}

		res, err := provider.Charge(ctx, providers.ChargeRequest{
			PaymentID: p.ID, Amount: p.Amount, Currency: p.Currency, IdempotencyKey: key,
		})
		if err != nil {
			last = providers.ChargeResult{Outcome: providers.OutcomeTimeout, Retryable: true, Message: err.Error()}
		} else {
			last = res
		}
		_ = s.store.CreateAttempt(&models.PaymentAttempt{
			PaymentID: p.ID, AttemptNumber: attempt,
			Outcome: string(last.Outcome), Retryable: last.Retryable, ProviderTxnID: last.ProviderTxnID,
		})
		if !last.Retryable {
			return last, nil
		}
	}
	return last, nil
}

func (s *Service) HandleWebhook(c *gin.Context) {
	var body providers.LateWebhookEvent
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.HandleWebhookEvent(body); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	p, _ := s.store.Get(body.PaymentID)
	c.JSON(http.StatusOK, toResponse(p, "webhook applied"))
}

func (s *Service) HandleWebhookEvent(ev providers.LateWebhookEvent) error {
	p, err := s.store.Get(ev.PaymentID)
	if err != nil || p == nil {
		return errors.New("payment not found")
	}
	next := models.PaymentStatusFailed
	outcome := providers.OutcomeDecline
	if ev.Success {
		next = models.PaymentStatusSucceeded
		outcome = providers.OutcomeSuccess
	}
	if applyStatus(p, next, outcome) {
		p.LastOutcome = string(outcome)
		if p.ProviderID == "" {
			p.ProviderID = ev.ProviderID
		}
		_ = s.store.Update(p)
		_ = s.upsertLedger(p)
	}
	return nil
}

func (s *Service) Reconcile(c *gin.Context) {
	rep, err := s.RunReconciliation(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rep)
}

func (s *Service) RunReconciliation(ctx context.Context) (reconciliation.Report, error) {
	ledgers, err := s.store.ListLedger()
	if err != nil {
		return reconciliation.Report{}, err
	}
	rows := make([]reconciliation.LedgerRow, 0, len(ledgers))
	for _, e := range ledgers {
		rows = append(rows, reconciliation.LedgerRow{PaymentID: e.PaymentID, Amount: e.Amount, Status: e.Status})
	}
	var processed []providers.ProcessedCharge
	for _, p := range s.cfg.Providers {
		rep, err := p.Report(ctx)
		if err != nil {
			return reconciliation.Report{}, err
		}
		processed = append(processed, rep...)
	}
	report := reconciliation.Compare(rows, processed)

	now := time.Now().UTC()
	apply := func(items []reconciliation.Item, matched bool) {
		for _, item := range items {
			entry, _ := s.store.GetLedger(item.PaymentID)
			if entry == nil {
				continue
			}
			if item.ProviderStatus != "" {
				st := item.ProviderStatus
				amt := item.ProviderAmount
				entry.ProviderReportedStatus = &st
				entry.ProviderReportedAmount = &amt
			}
			if matched || item.Reason == "amount or status drift" {
				t := now
				entry.ReconciledAt = &t
			}
			_ = s.store.UpsertLedger(entry)
		}
	}
	apply(report.Matches, true)
	apply(report.Mismatches, false)
	return report, nil
}

func (s *Service) replay(c *gin.Context, p *models.Payment) {
	status, msg := replayHTTP(p)
	c.JSON(status, toResponse(p, msg))
}

func (s *Service) upsertLedger(p *models.Payment) error {
	return s.store.UpsertLedger(&models.LedgerEntry{
		PaymentID: p.ID,
		Amount:    p.Amount,
		Status:    p.Status,
	})
}

func (s *Service) Register(rg *gin.RouterGroup) {
	rg.POST("/payments", s.Create)
	rg.POST("/webhooks", s.HandleWebhook)
	rg.GET("/reconciliation", s.Reconcile)
}

func toResponse(p *models.Payment, msg string) createResponse {
	if p == nil {
		return createResponse{Message: msg}
	}
	return createResponse{
		ID: p.ID, Amount: p.Amount, Currency: p.Currency,
		Status: p.Status, ProviderID: p.ProviderID, Message: msg,
	}
}

func mapOutcome(result providers.ChargeResult, retriesExhausted bool) (int, string, string) {
	switch result.Outcome {
	case providers.OutcomeSuccess:
		return http.StatusCreated, models.PaymentStatusSucceeded, result.Message
	case providers.OutcomeDecline:
		return http.StatusUnprocessableEntity, models.PaymentStatusFailed, result.Message
	case providers.OutcomeAccepted:
		return http.StatusAccepted, models.PaymentStatusPending, result.Message
	case providers.OutcomeTimeout:
		if retriesExhausted {
			return http.StatusServiceUnavailable, models.PaymentStatusFailedPendingReview, result.Message
		}
		return http.StatusGatewayTimeout, models.PaymentStatusFailed, result.Message
	default:
		return http.StatusInternalServerError, models.PaymentStatusFailed, result.Message
	}
}

func replayHTTP(p *models.Payment) (int, string) {
	switch p.Status {
	case models.PaymentStatusSucceeded:
		return http.StatusCreated, "idempotent replay"
	case models.PaymentStatusFailed:
		return http.StatusUnprocessableEntity, "idempotent replay"
	case models.PaymentStatusFailedPendingReview:
		return http.StatusServiceUnavailable, "idempotent replay"
	default:
		return http.StatusAccepted, "idempotent replay"
	}
}

// applyStatus never regresses a terminal decline/success back to pending.
func applyStatus(p *models.Payment, next string, _ providers.Outcome) bool {
	cur := p.Status
	if cur == models.PaymentStatusSucceeded || cur == models.PaymentStatusFailed {
		return false
	}
	p.Status = next
	return true
}