# Balanciz

A structured, multi-company accounting system built with Go.


Designed around a single principle: **correctness before convenience**. The posting engine enforces double-entry bookkeeping, the tax engine handles recoverability at the line level, and the reconciliation engine produces auditable suggestions — the user always has final authority.

Built on Go · Fiber · GORM · PostgreSQL · Templ · Alpine.js · Tailwind CSS.

---

## Table of Contents

- [AI Product Architecture](#ai-product-architecture)
- [Local Development](#local-development)
- [Production Deployment](#production-deployment)
- [Deploy on Ubuntu 24.04](#deploy-on-ubuntu-2404-bare-metal--vps)
- [Migration Strategy](#migration-strategy)
- [Useful Commands](#useful-commands)
- [Troubleshooting](#troubleshooting)
- [License](#license)


---

## AI Product Architecture

Balanciz AI is governed by `AI_PRODUCT_ARCHITECTURE.md`.

The architecture separates four layers:

- Business Truth Layer: accounting, posting, tax, reconciliation, permission, and audit engines remain authoritative.
- AI Learning Module: learns company-scoped behavior and habits without mutating accounting truth.
- AI Output Module: turns learning into recommendations, dashboard suggestions, tasks, insights, OCR output, and reviewable drafts.
- AI Infrastructure Layer: owns AI Gateway, model routing, prompts, job runs, request logs, validation, traces, cost controls, and feature flags.

Core rule: AI can assist, but Balanciz backend engines remain the accountant of record.

---


### 1. Global Search + Advanced Search

Topbar search box backed by a dedicated **search projection** (`search_documents` table) covering all 19 entity families — invoices, bills, quotes, sales orders, purchase orders, receipts, expenses, journal entries, credit notes, returns, refunds, deposits, prepayments, customers, vendors, and product/services.

- **Topbar dropdown** (`Cmd-K`) — three-tier ranking (doc number → counterparty name → memo), grouped by family with per-group caps, debounced fetch, IME-safe keyboard handling, per-user 20 req/sec rate limit.
- **Advanced Search** (`/advanced-search`) — full-page filter view: search + entity-type + date range + status, paginated flat results.
- **Projection drift reconciler** (`cmd/search-reconcile`) — detect/repair/cron-friendly modes.
- **SysAdmin "Rebuild search index"** button — kicks off a background backfill from `/admin/system`. Useful after deploys or if existing rows pre-date the projection.
- **Backend modes** — `SEARCH_ENGINE=ent | dual | legacy` env var; defaults to `ent` (the projection-backed engine). The `legacy` mode is an empty-fallback for emergencies; `dual` is reserved for cutover validation.

### 2. Reports system

Eleven reports organised into a **categorized hub** with per-user, per-company **favourites**:

| Category | Reports |
|----------|---------|
| Financial Statements | Profit & Loss, Balance Sheet, Trial Balance, **Cash Flow Summary** |
| Sales | **Sales by Customer** |
| Expenses | **Expense by Vendor** |
| Who owes you | A/R Aging |
| What you owe | A/P Aging *(now in the Reports hub)* |
| Sales Tax | Sales Tax Report |
| For my accountant | **General Ledger**, Journal Entries, Account Transactions |

**Universal drill-through:** every per-account / per-counterparty money cell on Balance Sheet / Income Statement / Trial Balance / AR Aging / AP Aging / Cash Flow / Sales by Customer / Expense by Vendor is a hyperlink. The chain is:

```
Summary report (BS/IS/TB/Aging/...)
    → click cell
    → Account Transactions / customer or vendor workspace
        → click row #
        → Source document (Bill / Invoice / Expense / Journal Entry / ...)
```

The Account Transactions page itself was enhanced with **Type / # / Name** columns: source-document type label, the document number as a clickable link to the originating record, and the counterparty name resolved from the JE line's `party_type`/`party_id`. Manual JEs and unmapped source types fall back to the JE detail page so every row is clickable.

### 3. List-page filter unification (16 pages)

Every list surface in the app now shares the same **compact one-row filter pattern**: counterparty SmartPicker (typeahead) + status select + date range + Apply / Reset. Unified pages:

- **AR side** — Sales Orders, Quotes, Invoices, Receipts, Customer Deposits, AR Returns, AR Refunds
- **AP side** — Purchase Orders, Bills, Vendor Credit Notes, Vendor Prepayments, Vendor Returns, Vendor Refunds
- **Contacts/Items** — Customers, Vendors, Products & Services (Search + Status, Products adds Type + Stock filter)

Architectural sediment from this work:

- **`pages.listFilterInputClass()`** — single styling source.
- **`web.parseListDateRange()`** + **`lookupCustomerName()` / `lookupVendorName()`** — single date / name-resolution source.
- **`services.XxxListFilter` structs** — replaces positional args on every `ListXxx` service func; future filter additions don't churn call sites.
- **`pages.MoneyCell{Amount, DrillURL}`** + **`services.AccountDrillURL()`** — single money-with-drill primitive.
- **`services.AllReports()` registry** — adding a new report is one struct entry: hub auto-picks-it-up + favourites toggle works without glue.


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
go run ./cmd/balanciz-migrate
```

Applies both GORM AutoMigrate and all SQL files in `migrations/`. Idempotent — safe to run repeatedly.

**5. Run the application**

```bash
go run ./cmd/balanciz
```

Open: [http://localhost:6768](http://localhost:6768)

---

## Production Deployment

Always run the migration binary before the application binary:

```bash
# Step 1 — apply all migrations (exits 0 on success)
./balanciz-migrate

# Step 2 — start the application
./balanciz
```

With Kubernetes or a process manager, use `balanciz-migrate` as an init container or pre-start hook.

---

## Deploy on Ubuntu 24.04 (Bare Metal / VPS)

A step-by-step guide for deploying Balanciz on a fresh Ubuntu 24.04 LTS server. Covers two paths:
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
sudo ufw allow 6768/tcp     # Balanciz port (or 80/443 if using reverse proxy)
sudo ufw enable
```

---


### Option A — Native Build Deployment

**Fast path for a fresh VPS (recommended):**

```bash
cd /opt
sudo git clone https://github.com/taxdeep/Balanciz.git
cd /opt/balanciz
chmod +x install.sh
# Interactive install
sudo bash ./install.sh

# Or fully automatic passwords/secrets
sudo bash ./install.sh --defaults
```

The install script will:
1. Install Go, Node.js 20, PostgreSQL, Nginx, wkhtmltopdf, `rsync`, and other system dependencies
2. Create the `balanciz` system user
3. Create the PostgreSQL role and database
4. Build the Go binaries and Tailwind CSS bundle
5. Write `/opt/balanciz/.env`
6. Run database migrations
7. Install the `balanciz` systemd service
8. Configure Nginx on port 80
9. Create a daily PostgreSQL backup cron job in `/var/backups/balanciz`

For upgrades after that, use a freshly pulled source tree. Do not run upgrades from `/opt/balanciz` in place: `upgrade.sh` rebuilds whatever source tree you point it at, and it does not run `git pull` by itself.

```bash
cd /tmp
rm -rf balanciz-latest
git clone https://github.com/taxdeep/Balanciz.git balanciz-latest
cd /tmp/balanciz-latest
git checkout main
git pull origin main
sudo bash ./upgrade.sh /tmp/balanciz-latest
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
CREATE USER balanciz WITH PASSWORD '<strong-random-password>';
CREATE DATABASE balanciz OWNER balanciz;
GRANT ALL PRIVILEGES ON DATABASE balanciz TO balanciz;
SQL

# Verify connection
psql -h localhost -U balanciz -d balanciz -c "SELECT 1;"
```

**4. Install wkhtmltopdf (for PDF generation)**

```bash
sudo apt install -y wkhtmltopdf
wkhtmltopdf --version   # Should print 0.12.x
```

**5. Clone and build**

```bash
cd /opt
sudo git clone https://github.com/taxdeep/Balanciz.git
sudo chown -R $USER:$USER /opt/balanciz
cd /opt/balanciz

# Install frontend dependencies and build CSS
npm install
npm run build:css

# Build Go binaries
CGO_ENABLED=0 go build -o ./bin/balanciz         ./cmd/balanciz
CGO_ENABLED=0 go build -o ./bin/balanciz-migrate  ./cmd/balanciz-migrate
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
DB_USER=balanciz
DB_PASSWORD=<strong-random-password>
DB_NAME=balanciz
DB_SSLMODE=disable
```

**7. Run migrations**

```bash
./bin/balanciz-migrate
```

**8. Create systemd service**

```bash
sudo tee /etc/systemd/system/balanciz.service > /dev/null <<EOF
[Unit]
Description=Balanciz Accounting System
After=network.target postgresql.service
Requires=postgresql.service

[Service]
Type=simple
User=balanciz
Group=balanciz
WorkingDirectory=/opt/balanciz
EnvironmentFile=/opt/balanciz/.env
ExecStartPre=/opt/balanciz/bin/balanciz-migrate
ExecStart=/opt/balanciz/bin/balanciz
Restart=on-failure
RestartSec=5
StandardOutput=journal
StandardError=journal

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/balanciz/data
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
```

Set ownership and create data directory:

```bash
sudo mkdir -p /opt/balanciz/data
sudo id balanciz >/dev/null 2>&1 || sudo useradd --system --no-create-home --shell /usr/sbin/nologin balanciz
sudo chown -R balanciz:balanciz /opt/balanciz
```

Enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable balanciz
sudo systemctl start balanciz

# Check status
sudo systemctl status balanciz
sudo journalctl -u balanciz -f   # Follow logs
```

---

### Reverse Proxy with Nginx (recommended for production)

```bash
sudo apt install -y nginx
```

Create site config:

```bash
sudo tee /etc/nginx/sites-available/balanciz > /dev/null <<'EOF'
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

sudo ln -s /etc/nginx/sites-available/balanciz /etc/nginx/sites-enabled/
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
sudo tee /etc/cron.d/balanciz-backup > /dev/null <<EOF
0 2 * * * postgres pg_dump balanciz | gzip > /var/backups/balanciz/balanciz_\$(date +\%Y\%m\%d).sql.gz
EOF

sudo mkdir -p /var/backups/balanciz
sudo chown postgres:postgres /var/backups/balanciz
```

**Restore from backup:**

```bash
gunzip -c /var/backups/balanciz/balanciz_20260330.sql.gz | sudo -u postgres psql balanciz
```

### Upgrading

```bash
# Native deployment: upgrade from a fresh source tree, not from /opt/balanciz
cd /tmp
rm -rf balanciz-latest
git clone https://github.com/taxdeep/Balanciz.git balanciz-latest
cd /tmp/balanciz-latest
git checkout main
git pull origin main
sudo bash ./upgrade.sh /tmp/balanciz-latest

# Docker deployment: rebuild from the latest checked-out repo
docker compose up -d --build
```

`upgrade.sh` prints `Installed src` and `Upgrade src` near the top. If `Upgrade src` is not the release you expect, stop there and refresh the source tree before continuing.

---

## Migration Strategy

Balanciz uses a two-phase explicit migration model:

| Phase | Tool | What it does |
|-------|------|-------------|
| 1 — GORM AutoMigrate | `db.Migrate()` | Creates/alters tables based on model structs (never drops columns) |
| 2 — SQL file migrations | `db.ApplySQLMigrations()` | Applies `migrations/*.sql` files tracked in `schema_migrations` |

`cmd/balanciz-migrate` runs both phases in order and is the canonical migration entry point. The app server runs Phase 1 only on startup as a local-dev safety net; SQL migrations must be applied separately in production.

---

## Useful Commands

| Task | Command |
|------|---------|
| Start full stack (Docker) | `docker compose up --build` |
| Stop stack | `docker compose down` |
| Run migrations only (Docker) | `docker compose run --rm migrate` |
| Run migrations (local) | `go run ./cmd/balanciz-migrate` |
| Run app (local) | `go run ./cmd/balanciz` |
| Build CSS once | `npm run build:css` |
| Watch CSS | `npm run dev:css` |
| Reset dev DB | `go run ./cmd/balanciz-reset` |

---

## Troubleshooting

**`go` command not found** — Install Go 1.23+ and add it to `PATH`.

**`docker` command not found** — Install Docker Desktop and restart the terminal.

**Database connection error** — Check `.env`: `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`.

**Page has no styles** — Run `npm run build:css`.

**Migration error on startup** — Run `go run ./cmd/balanciz-migrate` manually and inspect the output. The app server only runs Phase 1; SQL migrations may be pending.

---

## License

**SPDX-License-Identifier: AGPL-3.0-only**

Balanciz is licensed under the **GNU Affero General Public License v3.0 (AGPL-3.0)**. See [`LICENSE.md`](LICENSE.md) for the full text.

- You are free to use, modify, and distribute this software.
- If you run a modified version of Balanciz as a network service, you must make the source code of your modifications available to users.

### Commercial License

For use in a commercial SaaS product without complying with AGPL requirements, contact **TAXDEEP CORP.** at [info@taxdeep.com](mailto:info@taxdeep.com).
