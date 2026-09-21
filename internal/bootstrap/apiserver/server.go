package apiserver

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/insignificantGuy/fastPay/internal/api/payment"
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
}

func LoadConfig() Config {
	loadDotEnv(".env")
	return Config{
		Port:        envOr("PORT", "8080"),
		DatabaseURL: envOr("DATABASE_URL", "postgres://fastpay:fastpay@localhost:5432/fastpay?sslmode=disable"),
		ProviderA:   mock.Mode(envOr("PROVIDER_A_MODE", string(mock.ModeSuccess))),
		ProviderB:   mock.Mode(envOr("PROVIDER_B_MODE", string(mock.ModeSuccess))),
	}
}

func Run() error {
	cfg := LoadConfig()
	db, err := gorm.Open(postgres.Open(cfg.DatabaseURL), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}

	engine := gin.Default()
	svc := payment.NewService(paymentrepo.NewRepository(db), newRouter(cfg))
	svc.Register(engine.Group("/v1"))

	addr := ":" + cfg.Port
	log.Printf("fastPay listening on %s", addr)
	return engine.Run(addr)
}

func newRouter(cfg Config) *routing.Engine {
	a := mock.New(mock.Config{
		ID:               "provider-a",
		Available:        true,
		Mode:             cfg.ProviderA,
		TimeoutDuration:  50 * time.Millisecond,
		LateWebhookDelay: 10 * time.Millisecond,
	})
	b := mock.New(mock.Config{
		ID:               "provider-b",
		Available:        true,
		Mode:             cfg.ProviderB,
		TimeoutDuration:  50 * time.Millisecond,
		LateWebhookDelay: 10 * time.Millisecond,
	})
	return routing.NewEngine(routing.Config{
		Providers: []routing.WeightedProvider{
			{Provider: a, Weight: 1},
			{Provider: b, Weight: 1},
		},
	})
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
