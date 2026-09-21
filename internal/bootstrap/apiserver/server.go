package apiserver

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/insignificantGuy/fastPay/internal/api/payment"
	"github.com/insignificantGuy/fastPay/internal/providers"
	"github.com/insignificantGuy/fastPay/internal/providers/mock"
	paymentrepo "github.com/insignificantGuy/fastPay/internal/repository/payment"
	"github.com/insignificantGuy/fastPay/internal/routing"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type Config struct {
	Port        string
	DatabaseURL string
	ProviderA   mock.Mode
	ProviderB   mock.Mode
	MaxAttempts int
}

func LoadConfig() Config {
	loadDotEnv(".env")
	return Config{
		Port:        envOr("PORT", "8080"),
		DatabaseURL: envOr("DATABASE_URL", "postgres://fastpay:fastpay@localhost:5432/fastpay?sslmode=disable"),
		ProviderA:   mock.Mode(envOr("PROVIDER_A_MODE", string(mock.ModeSuccess))),
		ProviderB:   mock.Mode(envOr("PROVIDER_B_MODE", string(mock.ModeSuccess))),
		MaxAttempts: 3,
	}
}

func Run() error {
	cfg := LoadConfig()
	db, err := gorm.Open(postgres.Open(cfg.DatabaseURL), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}

	a := mock.New(mock.Config{
		ID: "provider-a", Available: true, Mode: cfg.ProviderA,
		TimeoutDuration: 50 * time.Millisecond, LateWebhookDelay: 20 * time.Millisecond,
		LateWebhookSuccess: true,
	})
	b := mock.New(mock.Config{
		ID: "provider-b", Available: true, Mode: cfg.ProviderB,
		TimeoutDuration: 50 * time.Millisecond, LateWebhookDelay: 20 * time.Millisecond,
		LateWebhookSuccess: true,
	})
	engine := routing.NewEngine(routing.Config{
		Providers: []routing.WeightedProvider{
			{Provider: a, Weight: 1},
			{Provider: b, Weight: 1},
		},
	})
	svc := payment.NewService(paymentrepo.NewRepository(db), engine, payment.Config{
		MaxAttempts: cfg.MaxAttempts,
		Providers:   []providers.Provider{a, b},
	})
	a.SetOnLateWebhook(func(ev providers.LateWebhookEvent) { _ = svc.HandleWebhookEvent(ev) })
	b.SetOnLateWebhook(func(ev providers.LateWebhookEvent) { _ = svc.HandleWebhookEvent(ev) })

	r := gin.Default()
	svc.Register(r.Group("/v1"))

	addr := ":" + cfg.Port
	log.Printf("fastPay listening on %s", addr)
	return r.Run(addr)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func loadDotEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if os.Getenv(key) != "" {
			continue
		}
		_ = os.Setenv(key, strings.TrimSpace(val))
	}
}
