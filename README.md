# fastPay

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white)](go.mod)
[![Build](https://img.shields.io/badge/build-passing-brightgreen)](https://github.com/insignificantGuy/fastPay/actions)
[![Tests](https://img.shields.io/badge/tests-passing-brightgreen)](https://github.com/insignificantGuy/fastPay/actions)
[![License](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
[![Live](https://img.shields.io/badge/live-tezzpay.up.railway.app-439fe0)](https://tezzpay.up.railway.app)

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
- [Health endpoints](#health-endpoints)
- [Tech stack](#tech-stack)
- [Getting started](#getting-started)
- [Testing](#testing)
- [Development workflow](#development-workflow)
- [Maintenance checklist](#maintenance-checklist)
- [Roadmap](#roadmap)
- [Related projects](#related-projects)
- [Portfolio write-up](#portfolio-write-up)
- [License](#license)

---



## Status

**Phase 4 – Ship.** fastPay is fully built, tested, and deployed. Every command in [Getting started](#getting-started) has been run and verified against a live deployment.

- **Live at [https://tezzpay.up.railway.app](https://tezzpay.up.railway.app)** — a real URL that returns a real response when hit.
- **Auto-migrations** — `goose` migrations are embedded in the binary and run automatically on every deploy (toggle: `FASTPAY_AUTO_MIGRATE=false`).
- **Health probes** — `/v1/healthy` (liveness) and `/v1/ready` (readiness, pings Postgres).
- **Scaling fixes** — bounded ledger fetch, batched reconciliation stamps, connection pool config, graceful webhook fan-out.
- **Portfolio write-up** added ([see below](#portfolio-write-up)).



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


| Package                        | Role                                                 |
| ------------------------------ | ---------------------------------------------------- |
| `internal/bootstrap/apiserver` | HTTP server, route groups, env wiring                |
| `internal/providers`           | `Provider` interface (`Charge`, `Lookup`, `Report`)  |
| `internal/providers/mock`      | Fake payment providers with injectable failure modes |
| `internal/routing`             | Weighted round-robin provider selection              |
| `internal/retry`               | Exponential backoff with jitter                      |
| `internal/api/payment`         | Payments, webhooks, reconciliation HTTP              |
| `internal/repository/payment`  | GORM persistence                                     |
| `internal/reconciliation`      | Ledger vs. provider-report matching                  |
| `internal/models`              | GORM models: payments, attempts, ledger entries      |
| `migrations/`                  | Goose SQL                                            |


---



## Design decisions

This is the section a reviewer should read first — it's the difference between "followed a tutorial" and "can think like an engineer." Each decision below states the problem, the choice, and what was traded away.

### Why idempotency keys, specifically

**Problem:** a client can retry a request (network blip, timeout, impatient double-click) and a payment API must not charge twice for one intent.

**Choice:** the client supplies a `checkoutID` header on payment creation (the checkout's idempotency key). The server looks it up before doing anything else — a hit returns the stored result of the original request; a miss inserts the row *before* the provider is called, so a crash mid-request can't create a window where a retry falls through as "new." A unique constraint is the last line of defense if two requests race.

**Trade-off:** this pushes correctness responsibility onto the client (it must generate and persist its own key), which is the industry-standard trade — the alternative, server-generated dedup based on payload similarity, is heuristic and fails on legitimate repeat payments (same amount, same payee, different day).

What this actually does: `[internal/api/payment](internal/api/payment)`. New key charges once; seen key replays HTTP status and body; mid-flight (still `pending`) returns `202` without a second charge.

### How double-charging is actually prevented

Idempotency keys stop *client-side* duplication. Double-charging can also happen *server-side* — e.g., a retry after a provider timeout, where the original charge actually succeeded but the response was lost. Every provider call is tagged with the same idempotency key. A timeout does not mean "assume failure and retry blindly" — the service calls `Provider.Lookup` before the next attempt. If the mock already captured the payment, we adopt that result and do not `Charge` again.

What this actually does: mocks implement `Lookup` / idempotent `Charge`. `TimeoutActuallySucceeded` is the injectable lost-response case.

### Why this retry/backoff strategy

**Problem:** naive immediate retries amplify load exactly when a provider is already struggling, and can also race with a slow-but-successful original attempt.

**Choice:** retries are bounded (default 3 attempts) and back off exponentially with jitter (`[internal/retry](internal/retry)`), and only trigger on errors classified as retryable (timeouts) — not on explicit declines. After the bound is hit, the payment moves to `failed_pending_review` rather than silently failing.

**Trade-off:** a declined payment is never retried even if a human would want a second provider. Failover-on-decline is a documented future extension, not this service.

### Why reconciliation is a separate job, not real-time

**Problem:** requiring every payment to be reconciled before responding to the client would make the API's latency dependent on a batch/reporting system, which is backwards.

**Choice:** the API records what *it* believes happened (the ledger). `GET /v1/reconciliation` compares that ledger against each mock provider's `Report()`, surfacing matches, amount/status drift, and orphans.

What this actually does: `[internal/reconciliation](internal/reconciliation)` is a pure compare; the payment service loads ledger rows and provider reports, then stamps `provider_reported_*` / `reconciled_at` on matches and mismatches.

### Why mock providers instead of a real sandbox

Keeps the project's actual logic (routing, idempotency, retry, reconciliation) fully owned and inspectable, with zero dependency on any real payment network's uptime, sandbox quirks, or terms of service — and zero risk of touching anything resembling real financial data, which matters for a public portfolio repo.

What this actually does: `[internal/providers/mock](internal/providers/mock)` ships independently configurable `provider-a` / `provider-b`. Modes: success, decline, timeout, late-webhook. Late-webhook returns `accepted` immediately and delivers an in-process event that the HTTP webhook handler also accepts.

### Why weighted round-robin, and why it's deterministic

**Problem:** a hardcoded provider isn't a routing engine, and a random weighted pick is hard to test and hard to explain after the fact.

**Choice:** integer weights expand into a repeating sequence (2:1 → A,A,B,…) advanced by a counter. Unavailable providers are skipped; if none remain, the engine returns a clear error rather than panicking.

**Trade-off:** this is not live load-balancing on success rate. Weights are config, not a feedback loop.

What this actually does: `[internal/routing](internal/routing)` `Engine.SelectProvider`.

---



## Data model

`payments` — one row per payment intent (`[migrations/00001_create_payments.sql](migrations/00001_create_payments.sql)`, `[migrations/00002_idempotency_and_attempts.sql](migrations/00002_idempotency_and_attempts.sql)`)


| Column                     | Type                            | Notes                                                        |
| -------------------------- | ------------------------------- | ------------------------------------------------------------ |
| `id`                       | UUID PK                         | Generated at create                                          |
| `idempotency_key`          | VARCHAR(128) UNIQUE NOT NULL    | Value of the `checkoutID` header                             |
| `amount`                   | BIGINT NOT NULL                 | Minor units                                                  |
| `currency`                 | VARCHAR(3) NOT NULL             | Single currency in practice (`USD`)                          |
| `status`                   | VARCHAR(32) NOT NULL            | `pending` / `succeeded` / `failed` / `failed_pending_review` |
| `provider_id`              | VARCHAR(64) NOT NULL DEFAULT '' | Selected mock provider                                       |
| `last_outcome`             | VARCHAR(32) NOT NULL DEFAULT '' | Last provider outcome (`success`, `timeout`, …)              |
| `created_at`, `updated_at` | TIMESTAMPTZ                     |                                                              |


`payment_attempts` — one row per provider call (`[migrations/00002_idempotency_and_attempts.sql](migrations/00002_idempotency_and_attempts.sql)`)


| Column            | Type                 | Notes                                  |
| ----------------- | -------------------- | -------------------------------------- |
| `id`              | UUID PK              |                                        |
| `payment_id`      | UUID FK → `payments` |                                        |
| `attempt_number`  | INT NOT NULL         | 1, 2, 3…                               |
| `outcome`         | VARCHAR(32) NOT NULL | success / timeout / decline / accepted |
| `retryable`       | BOOLEAN              |                                        |
| `provider_txn_id` | VARCHAR(64)          |                                        |
| `created_at`      | TIMESTAMPTZ          |                                        |


`ledger_entries` — reconciliation source of truth (`[migrations/00003_ledger_entries.sql](migrations/00003_ledger_entries.sql)`)


| Column                                                 | Type                        | Notes                                |
| ------------------------------------------------------ | --------------------------- | ------------------------------------ |
| `id`                                                   | UUID PK                     |                                      |
| `payment_id`                                           | UUID UNIQUE FK → `payments` |                                      |
| `amount`, `status`                                     | BIGINT / VARCHAR(32)        | fastPay's record of the outcome      |
| `provider_reported_amount`, `provider_reported_status` | nullable                    | filled by reconciliation             |
| `reconciled_at`                                        | TIMESTAMPTZ nullable        | set when matched or flagged as drift |
| `created_at`, `updated_at`                             | TIMESTAMPTZ                 |                                      |


---



## Payment flow

What this actually does (`[internal/api/payment](internal/api/payment)`):

1. Require `checkoutID`. Replay if the key already exists.
2. Insert `pending` payment (key stored first).
3. `SelectProvider` → `Charge` with the same key; on retryable timeout, `Lookup` then backoff and retry.
4. Map the outcome to HTTP + payment status; upsert a ledger row.
5. Late webhooks (`POST /v1/webhooks`) can arrive before, after, or instead of a useful sync result; terminal `succeeded` / declined `failed` are not overwritten by a later `pending`.


| Outcome                     | HTTP             | Body status             |
| --------------------------- | ---------------- | ----------------------- |
| success                     | 201              | `succeeded`             |
| decline                     | 422              | `failed`                |
| accepted (awaiting webhook) | 202              | `pending`               |
| retries exhausted (timeout) | 503              | `failed_pending_review` |
| idempotent replay           | same as original | same row                |


```mermaid
sequenceDiagram
    Client->>API: POST /v1/payments (checkoutID)
    API->>DB: Look up key
    alt key seen before
        DB-->>API: prior result
        API-->>Client: same response, no double charge
    else new key
        API->>DB: insert pending payment
        API->>Routing: pick provider
        Routing-->>API: provider
        API->>Provider: Charge keyed by idempotency key
        alt provider times out
            API->>Provider: Lookup key
            alt already captured
                Provider-->>API: success
            else still unknown
                API->>API: backoff and retry Charge
            end
        end
        Provider-->>API: success_decline_or_accepted
        API-->>Client: 201_422_202_or_503
        Provider-->>API: POST /v1/webhooks
        API->>DB: converge payment and ledger
    end
```





## Idempotency & retry

- Every write carries a client-supplied `checkoutID` header; a repeat request returns the original result instead of re-executing. Mid-flight repeats return `202 pending`.
- Provider calls that fail with a retryable timeout back off (exponential + jitter) up to `MaxAttempts` (default 3). Declines are terminal.
- After the bound, status is `failed_pending_review` so the row is still visible for a human / later webhook.
- Webhooks are handled async and out of order: a webhook can arrive before, after, or instead of a synchronous capture result, and ledger state converges either way.



## Reconciliation

- `GET /v1/reconciliation` compares fastPay's ledger against each mock provider's `Report()` of what it processed.
- Output is matches, mismatches (amount/status drift), and orphans (present on one side only).
- Matched and drifted rows get `provider_reported_*` and `reconciled_at` stamped on the ledger.

---


## Health endpoints

Two standard probes are exposed under the `/v1` prefix so load balancers and deployment platforms can safely route traffic only to healthy instances.

| Endpoint    | Type        | Behaviour                                                    |
| ----------- | ----------- | ------------------------------------------------------------ |
| `GET /v1/healthy` | Liveness    | Returns `200 OK` if the process is running — no dependency checks. Use for restart-on-failure. |
| `GET /v1/ready`  | Readiness   | Returns `200 OK` only if the PostgreSQL database is reachable. Use for traffic routing / rolling deploys. |

Example responses:

```bash
# Liveness (always OK if the process is alive)
curl -sS https://<host>/v1/healthy
# -> 200 {"status":"ok"}

# Readiness (OK only when DB is reachable)
curl -sS https://<host>/v1/ready
# -> 200 {"status":"ok"}
# -> 503 {"status":"not ready","error":"..."} when DB is down
```

In a deployment manifest, configure:

```yaml
livenessProbe:
  httpGet: { path: /v1/healthy, port: 8080 }
  initialDelaySeconds: 5
  periodSeconds: 10

readinessProbe:
  httpGet: { path: /v1/ready, port: 8080 }
  initialDelaySeconds: 2
  periodSeconds: 5
  failureThreshold: 3
```

---


## Tech stack


| Layer      | Technology                                       |
| ---------- | ------------------------------------------------ |
| Language   | Go                                               |
| HTTP       | Gin                                              |
| Database   | Postgres                                         |
| ORM        | GORM                                             |
| Migrations | Goose                                            |
| Providers  | In-process mocks (no real payment network calls) |


---



## Getting started

Every command below has been run and verified against a live deployment.

### Run locally

```bash
# 1. Clone and enter the repo
git clone https://github.com/insignificantGuy/fastPay.git && cd fastPay

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
```

### Smoke-test the live deployment

The curl example below works against the [live deployment](https://tezzpay.up.railway.app) as well as a local instance — just swap the host.

```bash
# 1. Create a payment (idempotent)
curl -sS -X POST https://tezzpay.up.railway.app/v1/payments \
  -H "Content-Type: application/json" \
  -H "checkoutID: test-001" \
  -d '{"amount": 1000, "currency": "USD"}'
# -> 201 {"id": "...", "status": "succeeded", ...}

# 2. Replay the same checkoutID — same payment id, no second charge
curl -sS -X POST https://tezzpay.up.railway.app/v1/payments \
  -H "Content-Type: application/json" \
  -H "checkoutID: test-001" \
  -d '{"amount": 1000, "currency": "USD"}'
# -> 201 {"id": "<same as above>", "status": "succeeded", "message": "idempotent replay"}

# 3. Reconciliation report (ledger vs. provider report)
curl -sS https://tezzpay.up.railway.app/v1/reconciliation
# -> 200 {"matches": [...], "mismatches": [], "orphans_ledger": [], "orphans_provider": []}

# 4. Health probes
curl -sS https://tezzpay.up.railway.app/v1/healthy
# -> 200 {"status":"ok"}
curl -sS https://tezzpay.up.railway.app/v1/ready
# -> 200 {"status":"ok"}
```

### Try the failure modes

Set the provider modes in `.env` before starting the service:

```bash
PROVIDER_A_MODE=success
PROVIDER_B_MODE=decline    # decline -> 422 "failed"
# PROVIDER_A_MODE=timeout   # timeout -> 503 "failed_pending_review" after retries
# PROVIDER_A_MODE=late_webhook # accepted -> 202 "pending", then async webhook converges
```

`POST /v1/webhooks` accepts `{ "payment_id", "provider_id", "provider_txn_id", "amount", "success" }` for late-webhook / out-of-order tests. In late-webhook mock mode the process also delivers that event internally after a short delay.

---



## Testing

- **Unit tests** — mock modes (including `Lookup` / `Report`), weighted round-robin, backoff math, reconciliation matches/mismatches/orphans, HTTP mapping, idempotency (new / replay / mid-flight), retry vs decline, lost-success lookup.
- **Integration tests** — `TestCreateIntegrationPostgres` when `DATABASE_URL` is set: success and decline persist payment + ledger.

Coverage that is explicitly tested: retryable vs terminal classification; idempotency branches (new key / seen key / key seen mid-flight); reconciliation happy path and seeded drift/orphans.

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

- [x] Move the item from "Planned" to done in the relevant section above, and remove the `(planned)` tag from that section's heading if it was the last planned piece in it. (Phases 1–3)
- [x] If the item touched the data model, update the [Data model](#data-model) tables to match the real schema (column types, constraints), not just the conceptual shape.
- [x] If the item introduced a real design trade-off not already captured in [Design decisions](#design-decisions), add it — that section is the actual portfolio value of this repo, more than the code itself.
- [x] Update [Getting started](#getting-started) if the run/setup steps changed.
- [x] Check off the corresponding box in [Roadmap](#roadmap).
- [x] If this was the last item in the current Phase, write one sentence in the Phase heading about what actually shipped vs. what was originally planned for it.

---



## Roadmap



### Phase 1 — Routing + mocks

Shipped: in-process mocks with four deterministic modes, weighted round-robin routing, and `POST /v1/payments`.

- [x] Mock payment providers (success / decline / timeout / late webhook)
- [x] Routing rules engine (pick a provider per payment)
- [x] Payment create endpoint wired to routing + a mock provider



### Phase 2 — Idempotency, retries, webhooks

Shipped: required `checkoutID` header, bounded retry with lookup-before-retry, `POST /v1/webhooks` plus in-process late delivery. Failover to a second provider on decline was not added (still a documented extension).

- [x] Idempotency keys on payment creation
- [x] Retry logic with backoff on retryable provider errors
- [x] Async webhook endpoint + out-of-order handling



### Phase 3 — Reconciliation

Shipped: ledger upserts on payment outcomes; `GET /v1/reconciliation` reports matches, mismatches, and orphans. Not a scheduled cron — it runs when requested.

- [x] Ledger of payments and their final state
- [x] Reconciliation report (ledger vs. provider report)
- [x] Tests: routing, idempotency, retry, reconciliation



### Phase 4 — Ship

- [x] Deploy live
- [x] Design-decisions write-up + diagram
- [x] Keep this README current
- [x] Portfolio write-up

---



## Related projects

Part of a 3-project portfolio built to demonstrate backend engineering breadth:

- **[Slotly](https://github.com/insignificantGuy/Slotly)** — multi-tenant SaaS backend API (auth, RBAC, CRUD, Postgres, Go) — shipped.
- **[fastPay](https://github.com/insignificantGuy/fastPay)** (this repo) — payment routing and fault tolerance — shipped (Phases 1–4 done).
- **Event-driven observability pipeline** — Kafka, backpressure, circuit breakers, Prometheus/Grafana, load testing — not yet started.

---



## Portfolio write-up

> **fastPay** is a production-shaped payment routing service that demonstrates the core engineering challenges of moving money between providers without losing a cent. It is not a toy or a tutorial — it is a complete, tested, and deployable system that solves the real problems: idempotency, retry safety, out-of-order webhooks, and reconciliation.

### The problem

Any payment system that routes to external providers faces three hard requirements:

1. **Never charge twice.** Clients retry on network blips; the API must deduplicate by the client's intent (the idempotency key), not by payload similarity.
2. **Survive provider failures.** A timeout does not mean "the payment failed." It means "the response was lost" — the service must call `Lookup` before retrying, or it will double-charge a payment that actually succeeded.
3. **Prove it afterward.** Reconciliation is not optional. The ledger (what we think happened) must be compared against each provider's report (what they actually processed), and any drift must be surfaced and timestamped.

### Architecture

```
HTTP (Gin)
  → routes
    → controllers
      → services
        → routing engine      (deterministic weighted round-robin)
        → payment service     (idempotency keys, bounded retry with backoff + jitter)
        → webhook handler     (async, converges out-of-order events)
        → reconciliation      (batched ledger vs. provider report compare)
      → repositories (GORM)
        → Postgres
    → mock providers (in-process, four deterministic modes)
```

### Key design decisions

| Decision | Why |
|----------|-----|
| Client-supplied `checkoutID` idempotency key | Industry standard; server-generated dedup fails on legitimate repeat payments. |
| Insert key *before* provider call | Eliminates the "crash mid-request" window where a retry could fall through as new. |
| `Lookup` before every retry on timeout | Prevents double-charging a charge that succeeded but whose response was lost. |
| Bounded exponential backoff with jitter | Naive retries amplify load when the provider is already struggling. |
| Declines are terminal | Failover-on-decline is a future extension, not a free pass to retry. |
| Reconciliation is a separate job | API latency must not depend on batch/reporting systems. |
| In-process mock providers | Zero dependency on real payment networks; zero risk of touching real financial data. |
| Deterministic weighted round-robin | Random picks are hard to test and hard to explain after the fact. |

### What this proves at scale

The codebase is written to be inspectable at every layer — the routing engine, retry logic, idempotency semantics, and reconciliation compare are all pure functions with no hidden magic. The scaling fixes applied in Phase 4 demonstrate production awareness:

- **Bounded ledger fetch** (`ListLedger(ctx, limit)`) — no unbounded memory growth.
- **Batched reconciliation stamps** (`ReconcileStamps`) — eliminates N+1 UPDATEs.
- **Connection pool configuration** — `SetMaxOpenConns`, `SetConnMaxLifetime`, `SetConnMaxIdleTime` prevent Postgres connection exhaustion.
- **Graceful webhook fan-out** — buffered channels avoid blocking on slow handlers.

### Running it

Every command in the [Getting Started](#getting-started) section has been run and verified against a live deployment. The service is API-only, single-instance, and uses in-process mocks — so there's no PCI scope, no external sandbox to manage, and no real money at risk.

### What comes next

This is Project 2 of a 3-project backend portfolio:

1. **Slotly** — multi-tenant SaaS API (auth, RBAC, CRUD) ✓
2. **fastPay** — payment routing & fault tolerance ✓
3. **Event-driven observability pipeline** — Kafka, backpressure, circuit breakers, Prometheus/Grafana, load testing → coming next

---

## License

MIT
