# GoBooks

A structured, multi-company accounting system built with Go.

Designed around a single principle: **correctness before convenience**. The posting engine enforces double-entry bookkeeping, the tax engine handles recoverability at the line level, and the reconciliation engine produces auditable suggestions — the user always has final authority.

Built on Go · Fiber · GORM · PostgreSQL · Templ · Alpine.js · Tailwind CSS.

---

## Table of Contents

- [Quick Start — Docker](#quick-start--docker)
- [Local Development](#local-development)
- [Production Deployment](#production-deployment)
- [Deploy on Ubuntu 24.04](#deploy-on-ubuntu-2404-bare-metal--vps)
- [Migration Strategy](#migration-strategy)
- [Useful Commands](#useful-commands)
- [Troubleshooting](#troubleshooting)
- [License](#license)

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

`upgrade.sh` prints `Installed src` and `Upgrade src` near the top. If `Upgrade src` is not the release you expect, stop there and refresh the source tree before continuing.

---

## Migration Strategy

GoBooks uses a two-phase explicit migration model:

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
