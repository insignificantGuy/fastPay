package payment_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/insignificantGuy/fastPay/internal/api/payment"
	"github.com/insignificantGuy/fastPay/internal/models"
	"github.com/insignificantGuy/fastPay/internal/providers"
	"github.com/insignificantGuy/fastPay/internal/providers/mock"
	paymentrepo "github.com/insignificantGuy/fastPay/internal/repository/payment"
	"github.com/insignificantGuy/fastPay/internal/routing"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func init() { _ = context.Background; _ = paymentrepo.LedgerStamp{} }

type memStore struct {
	mu       sync.Mutex
	byID     map[string]*models.Payment
	byKey    map[string]string
	attempts []models.PaymentAttempt
	ledger   map[string]*models.LedgerEntry
}

func newMemStore() *memStore {
	return &memStore{
		byID:   map[string]*models.Payment{},
		byKey:  map[string]string{},
		ledger: map[string]*models.LedgerEntry{},
	}
}

func (m *memStore) Create(p *models.Payment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byKey[p.IdempotencyKey]; ok {
		return paymentrepo.ErrDuplicateIdempotencyKey
	}
	cp := *p
	m.byID[p.ID] = &cp
	m.byKey[p.IdempotencyKey] = p.ID
	return nil
}

func (m *memStore) Update(p *models.Payment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *p
	m.byID[p.ID] = &cp
	return nil
}

func (m *memStore) Get(id string) (*models.Payment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byID[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	cp := *p
	return &cp, nil
}

func (m *memStore) GetByIdempotencyKey(key string) (*models.Payment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byKey[key]
	if !ok {
		return nil, nil
	}
	cp := *m.byID[id]
	return &cp, nil
}

func (m *memStore) CreateAttempt(a *models.PaymentAttempt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attempts = append(m.attempts, *a)
	return nil
}

func (m *memStore) ListAttempts(paymentID string) ([]models.PaymentAttempt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.PaymentAttempt
	for _, a := range m.attempts {
		if a.PaymentID == paymentID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (m *memStore) UpsertLedger(e *models.LedgerEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *e
	m.ledger[e.PaymentID] = &cp
	return nil
}

func (m *memStore) GetLedger(paymentID string) (*models.LedgerEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.ledger[paymentID]
	if !ok {
		return nil, nil
	}
	cp := *e
	return &cp, nil
}

func (m *memStore) ListLedger(ctx context.Context, limit int) ([]models.LedgerEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]models.LedgerEntry, 0, len(m.ledger))
	for _, e := range m.ledger {
		out = append(out, *e)
	}
	sortLedger(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStore) ReconcileStamps(ctx context.Context, stamps []paymentrepo.LedgerStamp) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	for _, s := range stamps {
		if s.PaymentID == "" {
			continue
		}
		e, ok := m.ledger[s.PaymentID]
		if !ok {
			continue
		}
		ps := s.ProviderStatus
		pa := s.ProviderAmount
		e.ProviderReportedStatus = &ps
		e.ProviderReportedAmount = &pa
		e.ReconciledAt = &now
	}
	return nil
}

func (m *memStore) Ping(ctx context.Context) error {
	return nil // in-memory store is always ready
}

func noopSleep(_ context.Context, _ time.Duration) error { return nil }

func setupRouter(store payment.Store, engine *routing.Engine, providersList []providers.Provider) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	svc := payment.NewService(store, engine, payment.Config{
		MaxAttempts: 3,
		BaseBackoff: time.Millisecond,
		Jitter:      time.Millisecond,
		Sleep:       noopSleep,
		Providers:   providersList,
	})
	svc.Register(r.Group("/v1"))
	return r
}

func setupRouterSvc(store payment.Store, engine *routing.Engine, providersList []providers.Provider, max int) (*gin.Engine, *payment.Service) {
	gin.SetMode(gin.TestMode)
	svc := payment.NewService(store, engine, payment.Config{
		MaxAttempts: max,
		BaseBackoff: time.Millisecond,
		Jitter:      time.Millisecond,
		Sleep:       noopSleep,
		Providers:   providersList,
	})
	r := gin.New()
	svc.Register(r.Group("/v1"))
	return r, svc
}

func postPayment(t *testing.T, h http.Handler, amount int64, key string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"amount": amount, "currency": "USD"})
	req := httptest.NewRequest(http.MethodPost, "/v1/payments", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("checkoutID", key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestCreateUsesRoutingEngineProvider(t *testing.T) {
	a := mock.New(mock.Config{ID: "provider-a", Available: true, Mode: mock.ModeSuccess})
	b := mock.New(mock.Config{ID: "provider-b", Available: true, Mode: mock.ModeDecline})
	engine := routing.NewEngine(routing.Config{
		Providers: []routing.WeightedProvider{
			{Provider: a, Weight: 1},
			{Provider: b, Weight: 0},
		},
	})
	store := newMemStore()
	w := postPayment(t, setupRouter(store, engine, nil), 1000, "key-route")
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["provider_id"] != "provider-a" {
		t.Fatalf("provider_id=%v, want provider-a (weight forced)", resp["provider_id"])
	}
	if resp["status"] != models.PaymentStatusSucceeded {
		t.Fatalf("status=%v", resp["status"])
	}
	id, _ := resp["id"].(string)
	row := store.byID[id]
	if row == nil || row.ProviderID != "provider-a" || row.Status != models.PaymentStatusSucceeded {
		t.Fatalf("store row = %+v", row)
	}
	if store.ledger[id] == nil || store.ledger[id].Status != models.PaymentStatusSucceeded {
		t.Fatalf("ledger=%+v", store.ledger[id])
	}
}

func TestCreateDeclineAndTimeoutDiffer(t *testing.T) {
	decline := mock.New(mock.Config{ID: "d", Available: true, Mode: mock.ModeDecline})
	timeout := mock.New(mock.Config{ID: "t", Available: true, Mode: mock.ModeTimeout, TimeoutDuration: time.Millisecond})
	accepted := mock.New(mock.Config{ID: "l", Available: true, Mode: mock.ModeLateWebhook})

	cases := []struct {
		name       string
		provider   *mock.Provider
		httpStatus int
		payStatus  string
	}{
		{"decline", decline, http.StatusUnprocessableEntity, models.PaymentStatusFailed},
		{"timeout", timeout, http.StatusServiceUnavailable, models.PaymentStatusFailedPendingReview},
		{"accepted", accepted, http.StatusAccepted, models.PaymentStatusPending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := routing.NewEngine(routing.Config{
				Providers: []routing.WeightedProvider{{Provider: tc.provider, Weight: 1}},
			})
			w := postPayment(t, setupRouter(newMemStore(), engine, nil), 500, "key-"+tc.name)
			if w.Code != tc.httpStatus {
				t.Fatalf("http=%d want %d body=%s", w.Code, tc.httpStatus, w.Body.String())
			}
			var resp map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &resp)
			if resp["status"] != tc.payStatus {
				t.Fatalf("status=%v want %s", resp["status"], tc.payStatus)
			}
		})
	}
}

func TestIdempotencyReplayDoesNotChargeTwice(t *testing.T) {
	a := mock.New(mock.Config{ID: "a", Available: true, Mode: mock.ModeSuccess})
	engine := routing.NewEngine(routing.Config{
		Providers: []routing.WeightedProvider{{Provider: a, Weight: 1}},
	})
	h := setupRouter(newMemStore(), engine, []providers.Provider{a})
	w1 := postPayment(t, h, 1000, "same-key")
	w2 := postPayment(t, h, 1000, "same-key")
	if w1.Code != http.StatusCreated || w2.Code != http.StatusCreated {
		t.Fatalf("codes %d %d", w1.Code, w2.Code)
	}
	var r1, r2 map[string]any
	_ = json.Unmarshal(w1.Body.Bytes(), &r1)
	_ = json.Unmarshal(w2.Body.Bytes(), &r2)
	if r1["id"] != r2["id"] {
		t.Fatalf("expected same payment id, got %v %v", r1["id"], r2["id"])
	}
	if a.ChargeCount() != 1 {
		t.Fatalf("charge count=%d want 1", a.ChargeCount())
	}
}

func TestIdempotencyMidFlightReturnsPending(t *testing.T) {
	gate := make(chan struct{})
	a := mock.New(mock.Config{ID: "a", Available: true, Mode: mock.ModeSuccess, Gate: gate})
	engine := routing.NewEngine(routing.Config{
		Providers: []routing.WeightedProvider{{Provider: a, Weight: 1}},
	})
	h := setupRouter(newMemStore(), engine, nil)

	first := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		first <- postPayment(t, h, 1000, "inflight")
	}()
	time.Sleep(30 * time.Millisecond)
	second := postPayment(t, h, 1000, "inflight")
	close(gate)
	w1 := <-first
	if second.Code != http.StatusAccepted {
		t.Fatalf("mid-flight http=%d body=%s", second.Code, second.Body.String())
	}
	if w1.Code != http.StatusCreated {
		t.Fatalf("first http=%d body=%s", w1.Code, w1.Body.String())
	}
}

func TestRetryStopsOnDecline(t *testing.T) {
	a := mock.New(mock.Config{ID: "a", Available: true, Mode: mock.ModeDecline})
	engine := routing.NewEngine(routing.Config{
		Providers: []routing.WeightedProvider{{Provider: a, Weight: 1}},
	})
	postPayment(t, setupRouter(newMemStore(), engine, nil), 100, "decline-once")
	if a.ChargeCount() != 1 {
		t.Fatalf("declines must not retry, charges=%d", a.ChargeCount())
	}
}

func TestRetryOnTimeoutUntilSuccess(t *testing.T) {
	a := mock.New(mock.Config{
		ID: "a", Available: true, Mode: mock.ModeSuccess, TimeoutDuration: time.Millisecond,
		Sequence: []mock.Mode{mock.ModeTimeout, mock.ModeSuccess},
	})
	engine := routing.NewEngine(routing.Config{
		Providers: []routing.WeightedProvider{{Provider: a, Weight: 1}},
	})
	w := postPayment(t, setupRouter(newMemStore(), engine, nil), 100, "retry-then-ok")
	if w.Code != http.StatusCreated {
		t.Fatalf("http=%d body=%s", w.Code, w.Body.String())
	}
	if a.ChargeCount() < 2 {
		t.Fatalf("expected a retry, charges=%d", a.ChargeCount())
	}
}

func TestLookupPreventsDoubleChargeAfterLostSuccess(t *testing.T) {
	a := mock.New(mock.Config{
		ID: "a", Available: true, Mode: mock.ModeTimeout, TimeoutDuration: time.Millisecond,
		TimeoutActuallySucceeded: true,
	})
	engine := routing.NewEngine(routing.Config{
		Providers: []routing.WeightedProvider{{Provider: a, Weight: 1}},
	})
	w := postPayment(t, setupRouter(newMemStore(), engine, nil), 100, "lost-success")
	if w.Code != http.StatusCreated {
		t.Fatalf("http=%d body=%s", w.Code, w.Body.String())
	}
	if a.ChargeCount() != 1 {
		t.Fatalf("lookup should prevent a second charge, got %d", a.ChargeCount())
	}
}

func TestWebhookOutOfOrderConverges(t *testing.T) {
	store := newMemStore()
	a := mock.New(mock.Config{ID: "a", Available: true, Mode: mock.ModeLateWebhook, LateWebhookSuccess: true})
	engine := routing.NewEngine(routing.Config{
		Providers: []routing.WeightedProvider{{Provider: a, Weight: 1}},
	})
	h, svc := setupRouterSvc(store, engine, []providers.Provider{a}, 3)
	a.SetOnLateWebhook(func(ev providers.LateWebhookEvent) { _ = svc.HandleWebhookEvent(ev) })

	w := postPayment(t, h, 750, "webhook-key")
	if w.Code != http.StatusAccepted {
		t.Fatalf("http=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	id := resp["id"].(string)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		row, _ := store.Get(id)
		if row != nil && row.Status == models.PaymentStatusSucceeded {
			led, _ := store.GetLedger(id)
			if led == nil || led.Status != models.PaymentStatusSucceeded {
				t.Fatalf("ledger not converged: %+v", led)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("webhook did not converge payment to succeeded")
}

func TestWebhookHTTPEndpoint(t *testing.T) {
	store := newMemStore()
	a := mock.New(mock.Config{ID: "a", Available: true, Mode: mock.ModeLateWebhook})
	engine := routing.NewEngine(routing.Config{
		Providers: []routing.WeightedProvider{{Provider: a, Weight: 1}},
	})
	h := setupRouter(store, engine, nil)
	w := postPayment(t, h, 100, "wh-http")
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	id := resp["id"].(string)

	body, _ := json.Marshal(providers.LateWebhookEvent{PaymentID: id, ProviderID: "a", Success: true, Amount: 100})
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	wr := httptest.NewRecorder()
	h.ServeHTTP(wr, req)
	if wr.Code != http.StatusOK {
		t.Fatalf("webhook http=%d %s", wr.Code, wr.Body.String())
	}
	row, _ := store.Get(id)
	if row.Status != models.PaymentStatusSucceeded {
		t.Fatalf("status=%s", row.Status)
	}
}

func TestReconciliationReport(t *testing.T) {
	store := newMemStore()
	a := mock.New(mock.Config{ID: "a", Available: true, Mode: mock.ModeSuccess})
	engine := routing.NewEngine(routing.Config{
		Providers: []routing.WeightedProvider{{Provider: a, Weight: 1}},
	})
	h := setupRouter(store, engine, []providers.Provider{a})
	w := postPayment(t, h, 1000, "recon-1")
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	// seed a ledger-only orphan and a mismatch (drift the payment's amount to 1)
	_ = store.UpsertLedger(&models.LedgerEntry{PaymentID: "orphan-l", Amount: 1, Status: "succeeded"})
	for _, e := range store.ledger {
		if e.PaymentID != "orphan-l" {
			e.Amount = 1
			break
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/reconciliation", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("http=%d %s", w.Code, w.Body.String())
	}
	var rep struct {
		Matches         []any
		Mismatches      []any
		OrphansLedger   []any `json:"orphans_ledger"`
		OrphansProvider []any `json:"orphans_provider"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Mismatches) < 1 {
		t.Fatalf("expected amount drift mismatch, report=%s", w.Body.String())
	}
	if len(rep.OrphansLedger) < 1 {
		t.Fatalf("expected ledger orphan, report=%s", w.Body.String())
	}

	// Reconciliation must have stamped provider_reported_* and reconciled_at on the
	// drifted row (batched, not one UPDATE per row).
	led, _ := store.GetLedger(created.ID)
	if led == nil || led.ProviderReportedStatus == nil || *led.ProviderReportedStatus != "succeeded" {
		t.Fatalf("drifted row not stamped: %+v", led)
	}
	if led.ReconciledAt == nil {
		t.Fatalf("drifted row not marked reconciled: %+v", led)
	}
	// The ledger-only orphan must NOT be stamped (nothing to compare against).
	orphanLed, _ := store.GetLedger("orphan-l")
	if orphanLed != nil && orphanLed.ReconciledAt != nil {
		t.Fatalf("ledger-only orphan should not be reconciled: %+v", orphanLed)
	}
}

func TestReconciliationIsBoundedAndBatched(t *testing.T) {
	store := newMemStore()
	a := mock.New(mock.Config{ID: "a", Available: true, Mode: mock.ModeSuccess})
	engine := routing.NewEngine(routing.Config{
		Providers: []routing.WeightedProvider{{Provider: a, Weight: 1}},
	})

	// Seed 5 ledger rows, only 2 of which the provider actually processed.
	for i := 1; i <= 5; i++ {
		_ = store.UpsertLedger(&models.LedgerEntry{
			PaymentID: "seed-" + itoa(i), Amount: int64(i * 100), Status: "succeeded",
		})
	}
	// provider-a processed seed-1 and seed-2
	_, _ = a.Charge(context.Background(), providers.ChargeRequest{
		PaymentID: "seed-1", Amount: 100, Currency: "USD", IdempotencyKey: "k1",
	})
	_, _ = a.Charge(context.Background(), providers.ChargeRequest{
		PaymentID: "seed-2", Amount: 200, Currency: "USD", IdempotencyKey: "k2",
	})

	// Reconcile with a cap of 2 — the report must never exceed the cap.
	svc := payment.NewService(store, engine, payment.Config{
		MaxAttempts: 1, Providers: []providers.Provider{a}, ReconcileLimit: 2,
	})
	rep, err := svc.RunReconciliation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := len(rep.Matches) + len(rep.Mismatches) + len(rep.OrphansLedger) + len(rep.OrphansProvider); got > 2 {
		t.Fatalf("report grew beyond the cap: %d items", got)
	}

	// The two stamped rows must carry reconciled_at.
	stamped := 0
	for _, id := range []string{"seed-1", "seed-2"} {
		led, _ := store.GetLedger(id)
		if led != nil && led.ReconciledAt != nil {
			stamped++
		}
	}
	if stamped != 2 {
		t.Fatalf("expected 2 stamped rows, got %d", stamped)
	}
}

func TestReconcileStampsIsNoOpWhenEmpty(t *testing.T) {
	store := newMemStore()
	if err := store.ReconcileStamps(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.ReconcileStamps(context.Background(), []paymentrepo.LedgerStamp{}); err != nil {
		t.Fatal(err)
	}
}

func TestHealthEndpoints(t *testing.T) {
	store := newMemStore()
	a := mock.New(mock.Config{ID: "a", Available: true, Mode: mock.ModeSuccess})
	engine := routing.NewEngine(routing.Config{
		Providers: []routing.WeightedProvider{{Provider: a, Weight: 1}},
	})
	h := setupRouter(store, engine, []providers.Provider{a})

	// Liveness: the process is up, so /healthy is 200 regardless of the DB.
	req := httptest.NewRequest(http.MethodGet, "/v1/healthy", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("healthy http=%d body=%s", w.Code, w.Body.String())
	}
	var live map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &live); err != nil || live["status"] != "ok" {
		t.Fatalf("healthy body=%s", w.Body.String())
	}

	// Readiness: the in-memory store is always reachable, so /ready is 200.
	req = httptest.NewRequest(http.MethodGet, "/v1/ready", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ready http=%d body=%s", w.Code, w.Body.String())
	}
	var ready map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &ready); err != nil || ready["status"] != "ok" {
		t.Fatalf("ready body=%s", w.Body.String())
	}
}

func itoa(n int) string {
	return "0123456789"[n : n+1]
}

// sortLedger orders rows by payment id so a bounded ListLedger is deterministic
// (map iteration order is random). The real repo orders by created_at/id.
func sortLedger(rows []models.LedgerEntry) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j-1].PaymentID > rows[j].PaymentID; j-- {
			rows[j-1], rows[j] = rows[j], rows[j-1]
		}
	}
}

func TestCreateIntegrationPostgres(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.Payment{}, &models.PaymentAttempt{}, &models.LedgerEntry{}); err != nil {
		t.Fatal(err)
	}

	repo := paymentrepo.NewRepository(db)
	success := mock.New(mock.Config{ID: "provider-a", Available: true, Mode: mock.ModeSuccess})
	decline := mock.New(mock.Config{ID: "provider-b", Available: true, Mode: mock.ModeDecline})

	t.Run("success", func(t *testing.T) {
		engine := routing.NewEngine(routing.Config{
			Providers: []routing.WeightedProvider{{Provider: success, Weight: 1}},
		})
		w := postPayment(t, setupRouter(repo, engine, []providers.Provider{success}), 1000, "int-success")
		if w.Code != http.StatusCreated {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			ID         string
			Status     string
			ProviderID string `json:"provider_id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		row, err := repo.Get(resp.ID)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != models.PaymentStatusSucceeded || row.ProviderID != "provider-a" || row.Amount != 1000 {
			t.Fatalf("row=%+v", row)
		}
		led, err := repo.GetLedger(resp.ID)
		if err != nil || led == nil || led.Status != models.PaymentStatusSucceeded {
			t.Fatalf("ledger=%+v err=%v", led, err)
		}
	})

	t.Run("decline", func(t *testing.T) {
		engine := routing.NewEngine(routing.Config{
			Providers: []routing.WeightedProvider{{Provider: decline, Weight: 1}},
		})
		w := postPayment(t, setupRouter(repo, engine, nil), 2500, "int-decline")
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			ID     string
			Status string
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		row, err := repo.Get(resp.ID)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != models.PaymentStatusFailed || row.ProviderID != "provider-b" {
			t.Fatalf("row=%+v", row)
		}
	})
}
