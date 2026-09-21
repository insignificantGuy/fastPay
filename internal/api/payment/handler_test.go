package payment_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/insignificantGuy/fastPay/internal/api/payment"
	"github.com/insignificantGuy/fastPay/internal/models"
	"github.com/insignificantGuy/fastPay/internal/providers/mock"
	paymentrepo "github.com/insignificantGuy/fastPay/internal/repository/payment"
	"github.com/insignificantGuy/fastPay/internal/routing"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type memStore struct {
	byID map[string]*models.Payment
}

func newMemStore() *memStore {
	return &memStore{byID: map[string]*models.Payment{}}
}

func (m *memStore) Create(p *models.Payment) error {
	cp := *p
	m.byID[p.ID] = &cp
	return nil
}

func (m *memStore) Update(p *models.Payment) error {
	cp := *p
	m.byID[p.ID] = &cp
	return nil
}

func setupRouter(store payment.Store, engine *routing.Engine) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	payment.NewService(store, engine).Register(r.Group("/v1"))
	return r
}

func postPayment(t *testing.T, h http.Handler, amount int64) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"amount": amount, "currency": "USD"})
	req := httptest.NewRequest(http.MethodPost, "/v1/payments", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
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
	w := postPayment(t, setupRouter(store, engine), 1000)
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
		{"timeout", timeout, http.StatusGatewayTimeout, models.PaymentStatusFailed},
		{"accepted", accepted, http.StatusAccepted, models.PaymentStatusPending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := routing.NewEngine(routing.Config{
				Providers: []routing.WeightedProvider{{Provider: tc.provider, Weight: 1}},
			})
			w := postPayment(t, setupRouter(newMemStore(), engine), 500)
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

func TestCreateIntegrationPostgres(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.Payment{}); err != nil {
		t.Fatal(err)
	}

	repo := paymentrepo.NewRepository(db)
	success := mock.New(mock.Config{ID: "provider-a", Available: true, Mode: mock.ModeSuccess})
	decline := mock.New(mock.Config{ID: "provider-b", Available: true, Mode: mock.ModeDecline})

	t.Run("success", func(t *testing.T) {
		engine := routing.NewEngine(routing.Config{
			Providers: []routing.WeightedProvider{{Provider: success, Weight: 1}},
		})
		w := postPayment(t, setupRouter(repo, engine), 1000)
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
	})

	t.Run("decline", func(t *testing.T) {
		engine := routing.NewEngine(routing.Config{
			Providers: []routing.WeightedProvider{{Provider: decline, Weight: 1}},
		})
		w := postPayment(t, setupRouter(repo, engine), 2500)
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
