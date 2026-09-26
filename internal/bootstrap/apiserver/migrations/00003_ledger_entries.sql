-- +goose Up
CREATE TABLE ledger_entries (
    id UUID PRIMARY KEY,
    payment_id UUID NOT NULL UNIQUE REFERENCES payments (id),
    amount BIGINT NOT NULL,
    status VARCHAR(32) NOT NULL,
    provider_reported_amount BIGINT,
    provider_reported_status VARCHAR(32),
    reconciled_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS ledger_entries;
