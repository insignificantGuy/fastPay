# fastPay

A **Go** payment routing service: route a payment across providers, survive their failures, and prove afterward that every cent is accounted for.

**What this proves:** payment routing and fault tolerance — the highest-leverage project in this portfolio because it mirrors the core problem shape of production payments work (route → retry → reconcile), built against mock providers so there's no proprietary logic in it.

---

## Table of contents

- [Status](#status)
- [Scope](#scope)
- [What's explicitly out of scope](#whats-explicitly-out-of-scope)
- [Architecture](#architecture)
- [Design decisions](#design-decisions)
- [Data model](#data-model)
- [Payment flow](#payment-flow)
- [Idempotency & retry](#idempotency--retry)
- [Reconciliation](#reconciliation)
- [Tech stack](#tech-stack)
- [Getting started](#getting-started)
- [Testing](#testing)
- [Development workflow](#development-workflow)
- [Maintenance checklist](#maintenance-checklist)
- [Roadmap](#roadmap)
- [Related projects](#related-projects)
- [License](#license)

---

## Status

**Phase 1 is runnable.** Work started 2026-09-13. Mock providers, weighted round-robin routing, and `POST /v1/payments` persist a `payments` row and return an outcome-specific HTTP status. Idempotency, retries, webhooks, and reconciliation are still planned.

Every remaining section marked **(planned)** is a design intent, not a claim about working code. As each piece lands, its section drops the "(planned)" tag and gets a short "what this actually does" paragraph plus a link to the relevant package. See [Maintenance checklist](#maintenance-checklist) for the exact rule this follows.

---

## Scope

1. Routing rules engine + mock payment providers
2. Idempotency keys + retry logic + async webhook handling
3. Reconciliation ledger/report + tests
4. Deploy live · design-decisions write-up + diagram · README · portfolio write-up

## What's explicitly out of scope

Naming this up front so it reads as a deliberate boundary, not a gap discovered later:

- **Real payment gateways.** All providers are in-process mocks with injectable failure modes. No PCI-scope code, no real card/bank data, ever.
- **Multi-currency.** Single currency throughout, to keep the routing/reconciliation logic legible.
- **A UI.** This is an API-only service; the "product" is the payment engine, not a dashboard.
- **Horizontal scaling / distributed consensus.** Single-instance service. The reliability story here is about surviving *provider* failures, not *infrastructure* failures — that's Project 3's job.
- **Real-time reconciliation.** Reconciliation runs as a job against accumulated state, not per-transaction. See [Reconciliation](#reconciliation) for why.

---

## Architecture

```text
HTTP (Gin)
  → routes
    → controllers
      → services
        → routing engine      (picks a provider per rule set)
        → payment service     (idempotency keys, retry/backoff)
        → webhook handler     (async, provider → ledger)
        → reconciliation      (ledger vs. provider report)
      → repositories (GORM)
        → Postgres
    → mock providers (simulate success / decline / timeout / late webhook)
```

| Package | Role |
| ------- | ---- |
| `internal/bootstrap/apiserver` | HTTP server, route groups, env wiring |
| `internal/providers` | `Provider` interface callers depend on |
| `internal/providers/mock` | Fake payment providers with injectable failure modes |
| `internal/routing` | Weighted round-robin provider selection |
| `internal/api/payment` | `POST /v1/payments` handler and service |
| `internal/repository/payment` | GORM persistence for `payments` |
| `internal/models` | GORM models (`payments` now; attempts/ledger later) |
| `migrations/` | Goose SQL |

---

## Design decisions

This is the section a reviewer should read first — it's the difference between "followed a tutorial" and "can think like an engineer." Each decision below states the problem, the choice, and what was traded away.

### Why idempotency keys, specifically (planned)

**Problem:** a client can retry a request (network blip, timeout, impatient double-click) and a payment API must not charge twice for one intent.

**Choice:** the client supplies an idempotency key on payment creation. The server looks it up before doing anything else — a hit returns the stored result of the original request verbatim; a miss proceeds as new. The key is stored *before* the provider is called, not after, so a crash mid-request can't create a window where a retry falls through as "new."

**Trade-off:** this pushes correctness responsibility onto the client (it must generate and persist its own key), which is the industry-standard trade — the alternative, server-generated dedup based on payload similarity, is heuristic and fails on legitimate repeat payments (same amount, same payee, different day).

### How double-charging is actually prevented (planned)

Idempotency keys stop *client-side* duplication. Double-charging can also happen *server-side* — e.g., a retry after a provider timeout, where the original charge actually succeeded but the response was lost. The fix: every provider call is itself tagged with the same idempotency key, forwarded to the mock provider. A timeout doesn't mean "assume failure and retry blindly" — it means "ask the provider what happened to this key" before deciding whether to retry or reconcile against an already-successful charge. This is the detail most tutorials skip, and it's the one that actually matters in production.

### Why this retry/backoff strategy (planned)

**Problem:** naive immediate retries amplify load exactly when a provider is already struggling, and can also race with a slow-but-successful original attempt.

**Choice:** retries are bounded (a fixed max attempt count) and back off exponentially with jitter, and only trigger on errors classified as retryable (timeouts, 5xx) — not on explicit declines, which are terminal by definition. After the bound is hit, the payment moves to a `failed_pending_review` state rather than silently failing, so nothing gets lost without a human being able to see it.

### Why reconciliation is a separate job, not real-time (planned)

**Problem:** requiring every payment to be reconciled before responding to the client would make the API's latency dependent on a batch/reporting system, which is backwards.

**Choice:** the API's job is to record what *it* believes happened (the ledger). Reconciliation is a periodic job comparing that ledger against each mock provider's own report of what *it* processed, surfacing drift after the fact. This mirrors how real payment reconciliation works — it's a control, not a blocking step in the critical path.

### Why mock providers instead of a real sandbox

Keeps the project's actual logic (routing, idempotency, retry, reconciliation) fully owned and inspectable, with zero dependency on any real payment network's uptime, sandbox quirks, or terms of service — and zero risk of touching anything resembling real financial data, which matters for a public portfolio repo.

What this actually does: [`internal/providers`](internal/providers) is the interface; [`internal/providers/mock`](internal/providers/mock) ships `provider-a` and `provider-b`, each independently set to success, decline, timeout, or late-webhook. Timeouts are classified retryable; declines and successes are not. Late-webhook mode returns `accepted` immediately and fires an in-process callback after a delay (the HTTP webhook receiver is Phase 2).

### Why weighted round-robin, and why it's deterministic

**Problem:** a hardcoded provider isn't a routing engine, and a random weighted pick is hard to test and hard to explain after the fact.

**Choice:** integer weights expand into a repeating sequence (2:1 → A,A,B,…) advanced by a counter. Unavailable providers are skipped; if none remain, the engine returns a clear error rather than panicking.

**Trade-off:** this is not live load-balancing on success rate. Weights are config, not a feedback loop — that stays a documented future extension.

What this actually does: [`internal/routing`](internal/routing) `Engine.SelectProvider`.

---

## Data model

**`payments`** — one row per payment intent (see [`migrations/00001_create_payments.sql`](migrations/00001_create_payments.sql))
| Column | Type | Notes |
| ------ | ---- | ----- |
| `id` | UUID PK | Generated at create |
| `amount` | BIGINT NOT NULL | Minor units |
| `currency` | VARCHAR(3) NOT NULL | Single currency in practice (`USD`) |
| `status` | VARCHAR(32) NOT NULL | `pending` / `succeeded` / `failed` |
| `provider_id` | VARCHAR(64) NOT NULL DEFAULT '' | Mock provider id after routing |
| `created_at`, `updated_at` | TIMESTAMPTZ | |

`idempotency_key` is not in this table yet — Phase 2. `failed_pending_review` is also Phase 2 (retry bound).

**`payment_attempts`** — one row per provider call (a payment can have several, across retries)
| Column | Notes |
| ------ | ----- |
| `id` | Primary key |
| `payment_id` | FK → `payments` |
| `attempt_number` | 1, 2, 3… |
| `outcome` | success / timeout / declined / error |
| `created_at` | |

**`ledger_entries`** — the reconciliation source of truth
| Column | Notes |
| ------ | ----- |
| `id` | Primary key |
| `payment_id` | FK → `payments` |
| `amount`, `status` | fastPay's own record of the outcome |
| `provider_reported_amount`, `provider_reported_status` | filled in by reconciliation, nullable until then |
| `reconciled_at` | null until matched or flagged |

---

## Payment flow

What this actually does today ([`internal/api/payment`](internal/api/payment)): `POST /v1/payments` inserts a `pending` row, asks the routing engine for a provider, charges it, then updates status. HTTP mapping: success → 201 `succeeded`, decline → 422 `failed`, timeout → 504 `failed` (no retry yet), late-webhook accepted → 202 `pending`. Repeated requests create a second row (idempotency is Phase 2).

The diagram below is still the **target** flow, including idempotency, retries, and webhooks that are not wired yet.

```mermaid
sequenceDiagram
    Client->>API: POST /v1/payments (Idempotency-Key)
    API->>DB: Look up key
    alt key seen before
        DB-->>API: prior result
        API-->>Client: same response, no double charge
    else new key
        API->>Routing: pick provider for this payment
        Routing-->>API: provider
        API->>Provider: charge
        alt provider times out / errors
            API->>API: retry with backoff
        end
        Provider-->>API: accepted (async)
        API-->>Client: 202 pending
        Provider-->>API: webhook (success/fail)
        API->>DB: record ledger entry
    end
```

## Idempotency & retry

- Every write carries a client-supplied idempotency key; a repeat request returns the original result instead of re-executing.
- Provider calls that fail with a retryable error (timeout, 5xx) back off and retry a bounded number of times before the payment is marked failed.
- Webhooks are handled async and out of order: a webhook can arrive before, after, or instead of a synchronous response, and the ledger state must converge either way.

## Reconciliation

- A reconciliation job compares fastPay's ledger against each mock provider's own report of what it processed.
- Output is a report of matches, mismatches (amount/status drift), and orphans (present on one side only) — the thing that would page someone in a real payments system.

---

## Tech stack

| Layer | Technology |
| ----- | ---------- |
| Language | Go |
| HTTP | Gin |
| Database | Postgres |
| ORM | GORM |
| Migrations | Goose |
| Providers | In-process mocks (no real payment network calls) |

---

## Getting started

```bash
# 1. Clone and enter the repo
git clone <repo-url> && cd fastPay

# 2. Start Postgres and copy env
docker compose up -d
cp .env.example .env
# DATABASE_URL=postgres://fastpay:fastpay@localhost:5432/fastpay?sslmode=disable
# PORT=8080
# PROVIDER_A_MODE=success   # success | decline | timeout | late_webhook
# PROVIDER_B_MODE=success

# 3. Run migrations against a fresh database
export DATABASE_URL=postgres://fastpay:fastpay@localhost:5432/fastpay?sslmode=disable
goose -dir migrations postgres "$DATABASE_URL" up

# 4. Run the service
go run ./cmd/apiserver

# 5. Smoke-test
curl -sS -X POST localhost:8080/v1/payments \
  -H "Content-Type: application/json" \
  -d '{"amount": 1000, "currency": "USD"}'
```

---

## Testing

- **Unit tests** — all four mock provider modes, weighted round-robin counts, unavailable-provider error, HTTP mapping for success/decline/timeout/accepted. No network.
- **Integration tests** — `TestCreateIntegrationPostgres` in `internal/api/payment` runs request → routing → mock → DB row for success and decline when `DATABASE_URL` is set (skipped otherwise).
- **Reconciliation tests** (planned) — seed mismatches and assert the report catches them.

Coverage target (Phase 2+): every retryable-vs-terminal classification and every idempotency branch (new key / seen key / key seen mid-flight).

```bash
go test ./...
DATABASE_URL=postgres://fastpay:fastpay@localhost:5432/fastpay?sslmode=disable go test ./internal/api/payment -run TestCreateIntegrationPostgres
```

---

## Development workflow

This project is being built in **single-focus daily chunks**, not week-sized goals — each work session targets one item from the [Roadmap](#roadmap), finishes it enough to be genuinely done (code + test + doc line), and stops rather than spreading thin across several half-finished pieces. If a session's target turns out to be bigger than expected, the fix is to cut the target smaller for next time, not to quietly expand what counts as "done."

When picking up a new roadmap item:

1. Check the box's neighboring context — does it depend on something not yet built?
2. Write the migration first if the item touches the data model (see [Data model](#data-model)).
3. Build the smallest working slice, then its test, before moving to the next slice.
4. Update this README in the same sitting — not "later" — per the [Maintenance checklist](#maintenance-checklist).

---

## Maintenance checklist

Do these every time a roadmap item is completed, not in a batch at the end:

- [ ] Move the item from "Planned" to done in the relevant section above, and remove the `(planned)` tag from that section's heading if it was the last planned piece in it.
- [ ] If the item touched the data model, update the [Data model](#data-model) tables to match the real schema (column types, constraints), not just the conceptual shape.
- [ ] If the item introduced a real design trade-off not already captured in [Design decisions](#design-decisions), add it — that section is the actual portfolio value of this repo, more than the code itself.
- [ ] Update [Getting started](#getting-started) if the run/setup steps changed.
- [ ] Check off the corresponding box in [Roadmap](#roadmap).
- [ ] If this was the last item in the current Phase, write one sentence in the Phase heading about what actually shipped vs. what was originally planned for it.

---

## Roadmap

### Phase 1 — Routing + mocks
Shipped: in-process mocks with four deterministic modes, weighted round-robin routing, and `POST /v1/payments` that persists outcomes. No retries or idempotency yet — timeouts are recorded as failed 504s.
- [x] Mock payment providers (success / decline / timeout / late webhook)
- [x] Routing rules engine (pick a provider per payment)
- [x] Payment create endpoint wired to routing + a mock provider

### Phase 2 — Idempotency, retries, webhooks
- [ ] Idempotency keys on payment creation
- [ ] Retry logic with backoff on retryable provider errors
- [ ] Async webhook endpoint + out-of-order handling

### Phase 3 — Reconciliation
- [ ] Ledger of payments and their final state
- [ ] Reconciliation report (ledger vs. provider report)
- [ ] Tests: routing, idempotency, retry, reconciliation

### Phase 4 — Ship
- [ ] Deploy live
- [ ] Design-decisions write-up + diagram
- [ ] Keep this README current
- [ ] Portfolio write-up

---

## Related projects

Part of a 3-project portfolio built to demonstrate backend engineering breadth:

- **Slotly** — multi-tenant SaaS backend API (auth, RBAC, CRUD, Postgres, Go) — shipped.
- **fastPay** (this repo) — payment routing and fault tolerance — in progress.
- **Event-driven observability pipeline** — Kafka, backpressure, circuit breakers, Prometheus/Grafana, load testing — not yet started.

---

## License

MIT