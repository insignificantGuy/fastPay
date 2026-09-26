-- +goose Up
ALTER TABLE payments
    ADD COLUMN idempotency_key VARCHAR(128),
    ADD COLUMN last_outcome VARCHAR(32) NOT NULL DEFAULT '';

UPDATE payments SET idempotency_key = id::text WHERE idempotency_key IS NULL;

ALTER TABLE payments
    ALTER COLUMN idempotency_key SET NOT NULL;

CREATE UNIQUE INDEX idx_payments_idempotency_key ON payments (idempotency_key);

CREATE TABLE payment_attempts (
    id UUID PRIMARY KEY,
    payment_id UUID NOT NULL REFERENCES payments (id),
    attempt_number INT NOT NULL,
    outcome VARCHAR(32) NOT NULL,
    retryable BOOLEAN NOT NULL DEFAULT FALSE,
    provider_txn_id VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_payment_attempts_payment_id ON payment_attempts (payment_id);

-- +goose Down
DROP TABLE IF EXISTS payment_attempts;
DROP INDEX IF EXISTS idx_payments_idempotency_key;
ALTER TABLE payments DROP COLUMN IF EXISTS last_outcome;
ALTER TABLE payments DROP COLUMN IF EXISTS idempotency_key;
