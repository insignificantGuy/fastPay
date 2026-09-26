package apiserver

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/pressly/goose/v3"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*
var migrationsFS embed.FS

// autoMigrate runs goose "up" against the database if enabled.
//
// It is enabled by default so a fresh deployment always has the schema. Set
// FASTPAY_AUTO_MIGRATE=false to disable it (e.g. when you want to run
// migrations manually, or when a non-owner connection is all you have).
func autoMigrate(ctx context.Context, dsn string) error {
	if !autoMigrateEnabled() {
		log.Println("auto-migrate disabled (FASTPAY_AUTO_MIGRATE=false); skipping")
		return nil
	}

	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("migrate: open: %w", err)
	}
	defer sqlDB.Close()

	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("migrate: ping: %w", err)
	}

	goose.SetBaseFS(migrationsFS)
	if err := goose.RunContext(ctx, "up", sqlDB, "migrations"); err != nil {
		return fmt.Errorf("migrate: goose up: %w", err)
	}
	return nil
}

func autoMigrateEnabled() bool {
	v := strings.TrimSpace(os.Getenv("FASTPAY_AUTO_MIGRATE"))
	if v == "" {
		return true
	}
	return !(v == "false" || v == "0" || v == "no" || v == "off")
}