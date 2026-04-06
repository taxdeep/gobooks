# GoBooks

A structured, multi-company accounting system built with Go.

Designed around a single principle: **correctness before convenience**. The posting engine enforces double-entry bookkeeping, the tax engine handles recoverability at the line level, and the reconciliation engine produces auditable suggestions — the user always has final authority.

---

## Table of Contents

- [Features](#features)
- [Tech Stack](#tech-stack)
- [Architecture](#architecture)
- [Project Structure](#project-structure)
- [Quick Start — Docker](#quick-start--docker)
- [Local Development](#local-development)
- [Production Deployment](#production-deployment)
- [Deploy on Ubuntu 24.04](#deploy-on-ubuntu-2404-bare-metal--vps)
- [Migration Strategy](#migration-strategy)
- [Useful Commands](#useful-commands)
- [Troubleshooting](#troubleshooting)
- [License](#license)

---

## Features

### Multi-Company Accounting

Every record carries a `company_id`. Business data never crosses company boundaries at any layer — models, services, queries, and handlers all enforce isolation explicitly.

Users belong to one or more companies via a membership model with role-based access control: **owner, admin, bookkeeper, ap, viewer**. Each role maps to a distinct permission set enforced server-side on every write route.

---

### Chart of Accounts

- Strict code prefix rules: `1xxxx` → Asset, `2xxxx` → Liability, `3xxxx` → Equity, `4xxxx` → Revenue, `5xxxx` → Cost of Sales, `6xxxx` → Expense — violations are rejected at the backend
- Root type + detail type classification
- GIFI code support with format validation
- No deletion — accounts are marked inactive; history is always preserved
- Default COA template auto-applied on company creation
- Rule-based and optional AI-assisted account name/code suggestions (AI is assistive only; suggestions never bypass validation)

---

### Posting Engine

All accounting entries flow through a single `PostingEngine` that coordinates the full lifecycle:

```
Document → Validation → Tax → Posting Fragments → Aggregation → Journal Entry → Ledger Entries
```

Journal lines are **aggregated by account before persistence** — same-account, same-side fragments are merged into canonical debit/credit lines. Fragmented journal output is never written to the database.

**Concurrency-safe posting** uses a three-layer defence:
1. Pre-flight status check before acquiring any lock
2. `SELECT FOR UPDATE` row lock + status re-validation inside the transaction
3. Unique partial index `uq_journal_entries_posted_source` as database backstop

Sentinels `ErrAlreadyPosted` and `ErrConcurrentPostingConflict` are returned to callers for clean error handling.

---

### Journal Entry Lifecycle

```
draft → posted → voided | reversed
```

Every journal entry carries `status`, `source_type`, and `source_id` — it is never a free-floating record. Reversals create a new offsetting entry and mark the original `reversed`; ledger entries are updated atomically. Voiding resets the source document status.

A **ledger entry projection layer** (`ledger_entries` table) is maintained alongside journal lines. It enables running-balance queries without full journal scans.

---

### Sales Tax Engine

- Tax codes are company-scoped with configurable rates
- Tax is calculated **per line**, then **aggregated per account** before posting
- **Sales tax**: revenue line + tax payable credit — always on the same journal entry
- **Purchase tax (recoverable)**: expense debit + recoverable ITC debit
- **Purchase tax (non-recoverable)**: tax amount folded into the expense debit
- Tax accounts are configurable per tax code

---

### Invoices (AR) and Bills (AP)

**Invoices:**
- Full lifecycle: Draft → Issued → Sent → Paid / Voided
- Line items with per-line product/service, quantity, price, and tax code
- Document number sequencing (configurable prefix and format)
- Issue triggers the posting engine: validation → fragments → aggregation → JE + ledger
- Customer/account snapshots frozen at issue time for immutable audit trail
- Template system with Classic and Modern styles, configurable accent color, display toggles
- HTML preview and PDF export (wkhtmltopdf)
- Email sending with PDF attachment via company SMTP (SMTP must be configured and verified)
- Email send logging with full audit trail
- Draft invoices can be deleted; posted invoices can only be voided (reversal JE created)

**Bills:**
- Same lifecycle as invoices (draft → posted)
- Linked to vendor; optional due date and payment terms
- Pay Bills flow records the bank-side and AP-side journal entry in one transaction
- Linking paid bills to their originating bill is tracked

**Receive Payment:**
- Records customer payment against open invoices
- Creates the bank-side debit and AR-side credit in one transaction
- Optional invoice linkage

---

### Task + Billable Expense

A work-execution and cost-tracking layer that bridges service delivery with the invoice workflow.

**Tasks** represent a discrete unit of billable or non-billable work for a customer:
- Status machine: `open → completed → invoiced | cancelled`
- Snapshot fields (rate, quantity, unit type, currency) are locked once the task is completed
- Only completed, billable tasks enter the invoicing pipeline

**Billable cost linkage** (Expenses and Bill lines) can be attached to a task:
- Expenses and bill-line items carry `task_id`, `is_billable`, and `reinvoice_status` (`uninvoiced | invoiced | excluded | —`)
- `NormalizeTaskCostLinkage` is the single service-layer truth engine for all task-cost rules: auto-derives `billable_customer_id` from the task, validates task status, rejects customer mismatches
- Non-billable costs remain visible on the task but never enter the invoicing pipeline

**Draft Generator** (`/tasks/billable-work`):
- Selects completed + billable Tasks, task-linked + billable + uninvoiced Expenses, and task-linked + billable + uninvoiced Bill lines for a single customer
- Creates an Invoice Draft in one transaction: invoice header + invoice lines + `task_invoice_sources` bridge rows + source cache updates + source status transitions
- System items `TASK_LABOR` (for task labor lines) and `TASK_REIM` (for expense/cost pass-through lines) are bootstrapped per company and looked up by `system_code`, never by hardcoded ID
- Currency: all sources must share the same document currency; mixed-currency batches are rejected with a clear error

**Source lifecycle**:
- `generate` → Task moves to `invoiced`; Expense/Bill line `reinvoice_status` → `invoiced`; bridge row created (active)
- `delete draft` → sources released back to draftable state; bridge row preserved but `voided_at` set and invoice refs cleared
- `void invoice` → sources released back; bridge row preserved with invoice refs retained for audit history
- `re-generate` → works cleanly after any release; partial-unique index on `task_invoice_sources (source_type, source_id) WHERE voided_at IS NULL` prevents duplicate active linkages at the database layer

**Task-generated draft protection** (Batch 5):
- The Invoice Editor detects task-generated drafts via `HasActiveTaskInvoiceSources` (bridge table, not cache)
- Task-generated drafts are read-only in the editor: fieldset disabled + two-layer backend block (pre-check redirect + in-transaction backstop)
- Delete Draft remains available for users with `ActionInvoiceDelete` permission, allowing source release and regeneration

**Billing visibility** (Batch 6):
- `/tasks/billable-work/report` — read-only Billable Work Report: lists all currently draftable work, shows per-customer summary, supports customer filter
- Task detail page — full billing trace showing current billing state, active invoice linkage, and full history from `task_invoice_sources` (not source cache)
- Customers list — per-customer unbilled labor / expense / total columns linked to the report
- All amounts are grouped by document currency; no FX normalization is performed; explicitly presented as operational visibility, not a profitability or accounting report

---

### Bank Reconciliation

QuickBooks-style reconciliation UI with a four-metric summary bar (Statement Ending Balance, Beginning Balance, Cleared Balance, Difference). Finish Now is only enabled when Difference = $0.00, enforced both client-side (Alpine.js) and server-side inside a database transaction.

**Void:** Only the most recent non-voided reconciliation may be voided. Void requires a written reason, unreconciles all linked journal lines atomically, and is permanently recorded in history — not deleted.

---

### AI-Assisted Auto Match Engine

A three-layer matching engine that **suggests** reconciliation matches — the user always confirms.

**Layer 1 — Deterministic:** Exact amount match against the outstanding balance. Subset-sum search for pairs and triples of candidates that sum exactly to the target.

**Layer 2 — Heuristic scoring:** Four named signals with explicit weights:

| Signal | Weight | Logic |
|--------|--------|-------|
| `exact_amount_match` | 0.35 | Candidate amount equals outstanding balance exactly |
| `date_proximity` | 0.25 | Days between entry date and statement date |
| `source_reliability` | 0.15 | Source type (payment > invoice/bill > manual > opening balance) |
| `historical_match` | 0.25 | Confidence boost from reconciliation memory |

**Layer 3 — Structured explanation:** Every suggestion stores a `MatchExplanation` JSON with a human-readable `Summary`, named `Signals` (score + detail sentence each), `NetAmount`, and confidence `Tier`. The UI renders this in an expandable signal detail panel — there is no opaque ML output.

**Confidence tiers:** High (≥ 0.75) · Medium (≥ 0.45) · Low (< 0.45)

**Suggestion types:** `one_to_one` · `one_to_many` · `many_to_one` · `split`

**Accept / Reject flow:**
- Accept: sets status to `accepted`, updates reconciliation memory, pre-selects the suggested lines in the UI. No journal line is modified.
- Reject: sets status to `rejected`. No accounting data is touched.
- Accepted suggestions remain visible in the panel with a static badge so the user can see what is driving pre-selected checkboxes.
- Finish Now creates the Reconciliation record and sets `reconciliation_id` on the selected journal lines — that is the only point where accounting state changes.

**Reconciliation memory:** Learns from accepted suggestions. Per `(company, account, normalized_memo, source_type)` pattern: `confidence_boost` grows 0.05 per acceptance, hard-capped at 0.30. Bounded, auditable, never silently grows.

**Suggestion lifecycle:** `pending → accepted | rejected | expired | archived`. Suggestions are never deleted; they are retained for full audit history. Running Auto Match again transitions prior pending suggestions to `expired`. Voiding a reconciliation transitions its linked accepted suggestions to `archived`.

---

### Reports

- **Trial Balance** — debit/credit totals by account as of a date
- **Income Statement** — revenue vs. expense for a period
- **Balance Sheet** — assets, liabilities, and equity as of a date
- **Journal Entry Report** — filterable entry list with line detail

All reports are company-scoped and derived from posted journal lines only.

---

### Audit Log

Every critical action (post, void, reverse, reconcile, accept suggestion, etc.) writes an immutable audit log row with: action name, entity type, entity ID, actor (user email), metadata JSON, company ID, user ID, and timestamp. The audit log is append-only and viewable in the settings panel.

---

### SysAdmin Console

A separate login at `/admin/login` with its own session — no company business data is accessible from the SysAdmin layer.

Capabilities:
- Company and user management
- Maintenance mode toggle (blocks all non-admin logins)
- Audit log viewer (system-wide)
- Runtime metrics: CPU usage, memory, database size, storage
- System log viewer

---

### Settings

- **Company profile:** name, address, currency, fiscal year
- **Document numbering:** prefix and sequence configuration per document type
- **Sales tax codes:** create and manage tax codes and rates
- **AI Connect:** optional external AI provider (OpenAI) for account suggestions — API key stored encrypted at rest
- **Notifications:** SMTP and SMS provider configuration with test endpoints
- **Security:** session timeout and related settings
- **Members:** invite users to the company; manage roles

---

## Tech Stack

| Layer | Technology |
|-------|-----------|
| Language | Go 1.23 |
| Web framework | Fiber v2 |
| ORM | GORM |
| Database | PostgreSQL (production) · SQLite (testing) |
| Templates | Templ |
| CSS | Tailwind CSS |
| Client interactivity | Alpine.js |
| Money arithmetic | shopspring/decimal |
| UUIDs | google/uuid |
| Crypto | golang.org/x/crypto |

---

## Architecture

```
cmd/
  gobooks/          — web server entrypoint
  gobooks-migrate/  — migration runner (run before app start)
  gobooks-reset/    — dev database reset utility
  verifydb/         — database verification tool

internal/
  config/           — environment variable loading
  db/               — database connection + GORM AutoMigrate + SQL file migrations
  models/           — GORM structs: accounts, journal, ledger, invoices, bills,
                      banking, reconciliation suggestions, tax, users, audit
  services/         — all business logic: posting engine, tax engine, reports,
                      reconciliation match engine, memo normalisation, document
                      numbering, audit helpers, AI integration, notifications
  web/
    handlers/       — per-feature HTTP handlers (banking, invoices, bills, accounts, …)
    routes.go       — all routes with middleware composition
    templates/
      layout/       — base HTML layout
      ui/           — reusable UI components (sidebar, forms, badges)
      pages/        — per-page Templ components + view models

migrations/         — 022 numbered SQL files; applied in order via schema_migrations table
```

### Dual-layer architecture

**Business app** — one active company per session; all data is company-scoped. Routes are protected by `RequireAuth` + `RequireMembership` + `RequirePermission(Action)` middleware chains.

**SysAdmin** — separate session, separate login, system-level only. Cannot write business data.

---

## Project Structure

```
gobooks/
├── cmd/
│   ├── gobooks/            Web server entry point
│   ├── gobooks-migrate/    Migration runner
│   ├── gobooks-reset/      Dev DB reset
│   └── verifydb/           DB verification
├── internal/
│   ├── config/             Env config
│   ├── db/                 DB connection + migrate
│   ├── models/             GORM models (31 files)
│   ├── services/           Business logic (54+ files)
│   └── web/
│       ├── templates/      Templ components + page VMs
│       ├── routes.go       Route definitions
│       └── *_handlers.go   HTTP handlers per feature area
├── migrations/             SQL migration files (001–021)
├── static/                 Compiled Tailwind CSS
├── docker-compose.yml
├── Dockerfile
└── .env.example
```

---

## Quick Start — Docker

**Prerequisites:** Docker Desktop installed and running.

```bash
docker compose up --build
```

Docker automatically:
1. Waits for PostgreSQL to pass its health check
2. Runs `gobooks-migrate` (GORM AutoMigrate + all SQL migrations) to completion
3. Starts the application

Open: [http://localhost:6768](http://localhost:6768)

On first run the Setup page appears to create the initial company and owner account.

---

## Local Development

**Prerequisites:** Go 1.23+, Node.js 18+, PostgreSQL 14+

**1. Configure environment**

```bash
cp .env.example .env
```

Edit `.env` with local PostgreSQL credentials. See `.env.example` for all supported variables.

**2. Install frontend dependencies**

```bash
npm install
```

**3. Build Tailwind CSS**

```bash
npm run build:css
```

Watch mode (separate terminal):

```bash
npm run dev:css
```

**4. Run migrations**

```bash
go run ./cmd/gobooks-migrate
```

Applies both GORM AutoMigrate and all SQL files in `migrations/`. Idempotent — safe to run repeatedly.

**5. Run the application**

```bash
go run ./cmd/gobooks
```

Open: [http://localhost:6768](http://localhost:6768)

---

## Production Deployment

Always run the migration binary before the application binary:

```bash
# Step 1 — apply all migrations (exits 0 on success)
./gobooks-migrate

# Step 2 — start the application
./gobooks
```

With Kubernetes or a process manager, use `gobooks-migrate` as an init container or pre-start hook.

---

## Deploy on Ubuntu 24.04 (Bare Metal / VPS)

A step-by-step guide for deploying GoBooks on a fresh Ubuntu 24.04 LTS server. Covers two paths:
- **Option A — Docker (recommended):** simplest setup, handles all dependencies
- **Option B — Native build:** compile from source, run as systemd service

### Prerequisites (both options)

```bash
# Update system packages
sudo apt update && sudo apt upgrade -y

# Install basic tools
sudo apt install -y curl git ufw rsync
```

**Firewall setup:**

```bash
sudo ufw allow OpenSSH
sudo ufw allow 6768/tcp     # GoBooks port (or 80/443 if using reverse proxy)
sudo ufw enable
```

---

### Option A — Docker Deployment (Recommended)

**1. Install Docker Engine**

```bash
# Add Docker's official GPG key and repository
sudo apt install -y ca-certificates gnupg
sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg
sudo chmod a+r /etc/apt/keyrings/docker.gpg

echo \
  "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
  https://download.docker.com/linux/ubuntu \
  $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | \
  sudo tee /etc/apt/sources.list.d/docker.list > /dev/null

sudo apt update
sudo apt install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin

# Allow your user to run Docker without sudo
sudo usermod -aG docker $USER
newgrp docker
```

**2. Clone and configure**

```bash
cd /opt
sudo git clone https://github.com/your-org/gobooks.git
sudo chown -R $USER:$USER /opt/gobooks
cd /opt/gobooks

cp .env.example .env
```

Edit `.env` for production:

```bash
APP_ENV=prod
APP_ADDR=:6768
DB_HOST=db
DB_PORT=5432
DB_USER=gobooks
DB_PASSWORD=<strong-random-password>
DB_NAME=gobooks
DB_SSLMODE=disable
AI_SECRET_KEY=<base64-encoded-32-byte-key>   # optional, for AI features
```

**3. Start the stack**

```bash
docker compose up -d --build
```

Docker will:
1. Start PostgreSQL 16 and wait for health check
2. Run `gobooks-migrate` (schema + SQL migrations)
3. Start the GoBooks application

Verify:

```bash
docker compose ps          # All services should be "running" or "exited (0)"
docker compose logs app    # Check for startup errors
curl -s http://localhost:6768 | head -5
```

**4. Manage the service**

```bash
docker compose down              # Stop all services
docker compose up -d             # Start (no rebuild)
docker compose up -d --build     # Rebuild and start
docker compose logs -f app       # Follow application logs
docker compose exec db psql -U gobooks gobooks   # Database shell
```

**5. Data persistence**

PostgreSQL data is stored in a Docker volume (`gobooks_pgdata`). To back up:

```bash
docker compose exec db pg_dump -U gobooks gobooks > backup_$(date +%Y%m%d).sql
```

To restore:

```bash
cat backup_20260330.sql | docker compose exec -T db psql -U gobooks gobooks
```

---

### Option B — Native Build Deployment

**Fast path for a fresh VPS (recommended):**

```bash
cd /opt
sudo git clone https://github.com/imlei/gobooks.git
cd /opt/gobooks
chmod +x install.sh
# Interactive install
sudo bash ./install.sh

# Or fully automatic passwords/secrets
sudo bash ./install.sh --defaults
```

The install script will:
1. Install Go, Node.js 20, PostgreSQL, Nginx, wkhtmltopdf, `rsync`, and other system dependencies
2. Create the `gobooks` system user
3. Create the PostgreSQL role and database
4. Build the Go binaries and Tailwind CSS bundle
5. Write `/opt/gobooks/.env`
6. Run database migrations
7. Install the `gobooks` systemd service
8. Configure Nginx on port 80
9. Create a daily PostgreSQL backup cron job in `/var/backups/gobooks`

For upgrades after that, use a freshly pulled source tree. Do not run upgrades from `/opt/gobooks` in place: `upgrade.sh` rebuilds whatever source tree you point it at, and it does not run `git pull` by itself.

```bash
cd /tmp
rm -rf gobooks-latest
git clone https://github.com/imlei/gobooks.git gobooks-latest
cd /tmp/gobooks-latest
git checkout main
git pull origin main
sudo bash ./upgrade.sh /tmp/gobooks-latest
```

During the upgrade, confirm the script prints both of these lines and that `Upgrade src` matches the version you expect:

```text
Installed src: ...
Upgrade src:   ...
```

**Manual native build (advanced):**

**1. Install Go 1.23+**

```bash
GO_VERSION=1.23.6
curl -fsSL https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz | sudo tar -C /usr/local -xzf -
echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/golang.sh
source /etc/profile.d/golang.sh
go version   # Should print go1.23.x
```

**2. Install Node.js 20+ (for Tailwind CSS build)**

```bash
curl -fsSL https://deb.nodesource.com/setup_20.x | sudo -E bash -
sudo apt install -y nodejs
node -v && npm -v
```

**3. Install PostgreSQL 16**

```bash
sudo apt install -y postgresql postgresql-contrib

# Create database and user
sudo -u postgres psql <<SQL
CREATE USER gobooks WITH PASSWORD '<strong-random-password>';
CREATE DATABASE gobooks OWNER gobooks;
GRANT ALL PRIVILEGES ON DATABASE gobooks TO gobooks;
SQL

# Verify connection
psql -h localhost -U gobooks -d gobooks -c "SELECT 1;"
```

**4. Install wkhtmltopdf (for PDF generation)**

```bash
sudo apt install -y wkhtmltopdf
wkhtmltopdf --version   # Should print 0.12.x
```

**5. Clone and build**

```bash
cd /opt
sudo git clone https://github.com/your-org/gobooks.git
sudo chown -R $USER:$USER /opt/gobooks
cd /opt/gobooks

# Install frontend dependencies and build CSS
npm install
npm run build:css

# Build Go binaries
CGO_ENABLED=0 go build -o ./bin/gobooks         ./cmd/gobooks
CGO_ENABLED=0 go build -o ./bin/gobooks-migrate  ./cmd/gobooks-migrate
```

**6. Configure environment**

```bash
cp .env.example .env
```

Edit `.env`:

```bash
APP_ENV=prod
APP_ADDR=:6768
DB_HOST=localhost
DB_PORT=5432
DB_USER=gobooks
DB_PASSWORD=<strong-random-password>
DB_NAME=gobooks
DB_SSLMODE=disable
```

**7. Run migrations**

```bash
./bin/gobooks-migrate
```

**8. Create systemd service**

```bash
sudo tee /etc/systemd/system/gobooks.service > /dev/null <<EOF
[Unit]
Description=GoBooks Accounting System
After=network.target postgresql.service
Requires=postgresql.service

[Service]
Type=simple
User=gobooks
Group=gobooks
WorkingDirectory=/opt/gobooks
EnvironmentFile=/opt/gobooks/.env
ExecStartPre=/opt/gobooks/bin/gobooks-migrate
ExecStart=/opt/gobooks/bin/gobooks
Restart=on-failure
RestartSec=5
StandardOutput=journal
StandardError=journal

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/gobooks/data
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
```

Set ownership and create data directory:

```bash
sudo mkdir -p /opt/gobooks/data
sudo id gobooks >/dev/null 2>&1 || sudo useradd --system --no-create-home --shell /usr/sbin/nologin gobooks
sudo chown -R gobooks:gobooks /opt/gobooks
```

Enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable gobooks
sudo systemctl start gobooks

# Check status
sudo systemctl status gobooks
sudo journalctl -u gobooks -f   # Follow logs
```

---

### Reverse Proxy with Nginx (recommended for production)

```bash
sudo apt install -y nginx
```

Create site config:

```bash
sudo tee /etc/nginx/sites-available/gobooks > /dev/null <<'EOF'
server {
    listen 80;
    server_name your-domain.com;

    client_max_body_size 10M;

    location / {
        proxy_pass http://127.0.0.1:6768;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
EOF

sudo ln -s /etc/nginx/sites-available/gobooks /etc/nginx/sites-enabled/
sudo rm -f /etc/nginx/sites-enabled/default
sudo nginx -t && sudo systemctl reload nginx
```

**Add HTTPS with Let's Encrypt:**

```bash
sudo apt install -y certbot python3-certbot-nginx
sudo certbot --nginx -d your-domain.com
```

Certbot will auto-configure Nginx for HTTPS and set up auto-renewal.

---

### Post-Deployment Checklist

| Step | Command / Action |
|------|-----------------|
| Open browser | `https://your-domain.com` |
| Complete setup wizard | Create initial company + owner account |
| Configure SMTP | Settings > Notifications (required for invoice email) |
| Upload company logo | Settings > Company Profile |
| Import chart of accounts | Auto-imported on company creation |
| Create first customer | Sales > Customers |
| Create first invoice | Invoices > New Invoice |

### Backup Strategy

**Automated daily backup (cron):**

```bash
sudo tee /etc/cron.d/gobooks-backup > /dev/null <<EOF
0 2 * * * postgres pg_dump gobooks | gzip > /var/backups/gobooks/gobooks_\$(date +\%Y\%m\%d).sql.gz
EOF

sudo mkdir -p /var/backups/gobooks
sudo chown postgres:postgres /var/backups/gobooks
```

**Restore from backup:**

```bash
gunzip -c /var/backups/gobooks/gobooks_20260330.sql.gz | sudo -u postgres psql gobooks
```

### Upgrading

```bash
# Native deployment: upgrade from a fresh source tree, not from /opt/gobooks
cd /tmp
rm -rf gobooks-latest
git clone https://github.com/imlei/gobooks.git gobooks-latest
cd /tmp/gobooks-latest
git checkout main
git pull origin main
sudo bash ./upgrade.sh /tmp/gobooks-latest

# Docker deployment: rebuild from the latest checked-out repo
docker compose up -d --build
```

`upgrade.sh` now prints `Installed src` and `Upgrade src` near the top. If `Upgrade src` is not the release you expect, stop there and refresh the source tree before continuing.

---

## Migration Strategy

Gobooks uses a two-phase explicit migration model:

| Phase | Tool | What it does |
|-------|------|-------------|
| 1 — GORM AutoMigrate | `db.Migrate()` | Creates/alters tables based on model structs (never drops columns) |
| 2 — SQL file migrations | `db.ApplySQLMigrations()` | Applies `migrations/*.sql` files tracked in `schema_migrations` |

`cmd/gobooks-migrate` runs both phases in order and is the canonical migration entry point. The app server runs Phase 1 only on startup as a local-dev safety net; SQL migrations must be applied separately in production.

---

## Useful Commands

| Task | Command |
|------|---------|
| Start full stack (Docker) | `docker compose up --build` |
| Stop stack | `docker compose down` |
| Run migrations only (Docker) | `docker compose run --rm migrate` |
| Run migrations (local) | `go run ./cmd/gobooks-migrate` |
| Run app (local) | `go run ./cmd/gobooks` |
| Build CSS once | `npm run build:css` |
| Watch CSS | `npm run dev:css` |
| Reset dev DB | `go run ./cmd/gobooks-reset` |

---

## Troubleshooting

**`go` command not found** — Install Go 1.23+ and add it to `PATH`.

**`docker` command not found** — Install Docker Desktop and restart the terminal.

**Database connection error** — Check `.env`: `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`.

**Page has no styles** — Run `npm run build:css`.

**Migration error on startup** — Run `go run ./cmd/gobooks-migrate` manually and inspect the output. The app server only runs Phase 1; SQL migrations may be pending.

---

## License

**SPDX-License-Identifier: AGPL-3.0-only**

GoBooks is licensed under the **GNU Affero General Public License v3.0 (AGPL-3.0)**. See [`LICENSE.md`](LICENSE.md) for the full text.

- You are free to use, modify, and distribute this software.
- If you run a modified version of GoBooks as a network service, you must make the source code of your modifications available to users.

### Commercial License

For use in a commercial SaaS product without complying with AGPL requirements, contact **TAXDEEP CORP.** at [info@taxdeep.com](mailto:info@taxdeep.com).

### Trademark

**GoBooks** is a trademark of **TAXDEEP CORP.** and may not be used in derivative products without permission.
