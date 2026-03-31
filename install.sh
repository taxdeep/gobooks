#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# GoBooks — Fresh Install Script for Ubuntu 24.04 LTS
# ─────────────────────────────────────────────────────────────────────────────
#
# Usage:
#   sudo bash install.sh                  # interactive (prompts for passwords)
#   sudo bash install.sh --defaults       # non-interactive (random passwords)
#
# What this script does:
#   1. Installs system dependencies (Go, Node.js, PostgreSQL, Nginx, wkhtmltopdf)
#   2. Creates a "gobooks" system user
#   3. Creates the PostgreSQL database and role
#   4. Builds GoBooks from source (Go binaries + Tailwind CSS)
#   5. Writes the .env configuration file
#   6. Runs database migrations
#   7. Installs a systemd service (auto-start on boot)
#   8. Configures Nginx reverse proxy on port 80
#   9. Sets up a daily database backup cron job
#
# After install, open http://<server-ip> to complete the setup wizard.
# ─────────────────────────────────────────────────────────────────────────────

set -euo pipefail

# ── Defaults & constants ─────────────────────────────────────────────────────

INSTALL_DIR="/opt/gobooks"
DATA_DIR="/opt/gobooks/data"
BACKUP_DIR="/var/backups/gobooks"
GO_VERSION="1.23.6"
NODE_MAJOR=20
SERVICE_USER="gobooks"
DB_NAME="gobooks"
DB_USER="gobooks"
APP_PORT=6768

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

log()  { echo -e "${GREEN}[GoBooks]${NC} $*"; }
warn() { echo -e "${YELLOW}[GoBooks]${NC} $*"; }
err()  { echo -e "${RED}[GoBooks]${NC} $*" >&2; }

# ── Pre-flight checks ────────────────────────────────────────────────────────

if [[ $EUID -ne 0 ]]; then
    err "This script must be run as root (sudo bash install.sh)"
    exit 1
fi

if ! grep -qi "ubuntu" /etc/os-release 2>/dev/null; then
    warn "This script is designed for Ubuntu 24.04. Proceeding anyway..."
fi

if [[ -f "$INSTALL_DIR/.env" ]] || [[ -f "$INSTALL_DIR/bin/gobooks" ]]; then
    err "Existing installation detected at ${INSTALL_DIR}."
    err "Run upgrade.sh for upgrades, or remove ${INSTALL_DIR} before a fresh install."
    exit 1
fi

# ── Parse arguments ──────────────────────────────────────────────────────────

USE_DEFAULTS=false
if [[ "${1:-}" == "--defaults" ]]; then
    USE_DEFAULTS=true
fi

# ── Prompt or generate passwords ─────────────────────────────────────────────

generate_password() {
    openssl rand -base64 24 | tr -d '/+=' | head -c 32
}

if [[ "$USE_DEFAULTS" == true ]]; then
    DB_PASSWORD=$(generate_password)
    AI_SECRET_KEY=$(openssl rand -base64 32)
    log "Generated random DB password and AI secret key."
else
    echo ""
    echo -e "${CYAN}── GoBooks Installation ──${NC}"
    echo ""
    read -rsp "PostgreSQL password for '$DB_USER' (leave empty to auto-generate): " DB_PASSWORD
    echo ""
    if [[ -z "$DB_PASSWORD" ]]; then
        DB_PASSWORD=$(generate_password)
        log "Generated DB password."
    fi

    read -rsp "AI secret key (base64, leave empty to auto-generate): " AI_SECRET_KEY
    echo ""
    if [[ -z "$AI_SECRET_KEY" ]]; then
        AI_SECRET_KEY=$(openssl rand -base64 32)
        log "Generated AI secret key."
    fi
fi

# ── 1. System packages ───────────────────────────────────────────────────────

log "Updating system packages..."
apt-get update -qq
apt-get upgrade -y -qq

log "Installing base dependencies..."
apt-get install -y -qq \
    curl git build-essential ufw openssl rsync \
    ca-certificates gnupg lsb-release \
    wkhtmltopdf \
    nginx certbot python3-certbot-nginx

# ── 2. Install Go ────────────────────────────────────────────────────────────

GO_MIN_MAJOR=1
GO_MIN_MINOR=23

# Compare installed Go version against minimum requirement.
# Returns 0 if installed version is sufficient, 1 otherwise.
go_version_ok() {
    if ! command -v go &>/dev/null; then
        return 1
    fi
    local ver
    ver=$(go version | grep -oP 'go\K[0-9]+\.[0-9]+')
    local maj min
    maj=$(echo "$ver" | cut -d. -f1)
    min=$(echo "$ver" | cut -d. -f2)
    if [[ "$maj" -gt "$GO_MIN_MAJOR" ]]; then return 0; fi
    if [[ "$maj" -eq "$GO_MIN_MAJOR" && "$min" -ge "$GO_MIN_MINOR" ]]; then return 0; fi
    return 1
}

install_go() {
    log "Installing Go ${GO_VERSION} (minimum required: ${GO_MIN_MAJOR}.${GO_MIN_MINOR})..."
    # Remove any existing /usr/local/go to avoid mixing versions
    rm -rf /usr/local/go
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | tar -C /usr/local -xzf -
    cat > /etc/profile.d/golang.sh <<'GOEOF'
export PATH=$PATH:/usr/local/go/bin
GOEOF
    export PATH=$PATH:/usr/local/go/bin
    hash -r 2>/dev/null || true
    log "Go $(go version) installed."
}

export PATH=$PATH:/usr/local/go/bin
if go_version_ok; then
    log "Go $(go version | grep -oP 'go[0-9.]+') already installed (>= ${GO_MIN_MAJOR}.${GO_MIN_MINOR})."
else
    if command -v go &>/dev/null; then
        warn "Installed Go $(go version | grep -oP 'go[0-9.]+') is below minimum ${GO_MIN_MAJOR}.${GO_MIN_MINOR}. Upgrading..."
    fi
    install_go
fi

# ── 3. Install Node.js ───────────────────────────────────────────────────────

node_version_ok() {
    if ! command -v node &>/dev/null; then
        return 1
    fi
    local major
    major=$(node -v | sed -E 's/^v([0-9]+).*/\1/')
    [[ -n "$major" && "$major" -ge "$NODE_MAJOR" ]]
}

install_node() {
    log "Installing Node.js ${NODE_MAJOR}.x..."
    curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash -
    apt-get install -y -qq nodejs
    log "Node.js $(node -v) installed."
}

if node_version_ok; then
    log "Node.js $(node -v) already installed (>= ${NODE_MAJOR})."
else
    if command -v node &>/dev/null; then
        warn "Installed Node.js $(node -v) is below minimum ${NODE_MAJOR}. Upgrading..."
    else
        log "Node.js not found. Installing..."
    fi
    install_node
fi

# ── 4. Install and configure PostgreSQL ──────────────────────────────────────

if ! command -v psql &>/dev/null; then
    log "Installing PostgreSQL..."
    apt-get install -y -qq postgresql postgresql-contrib
fi

log "Configuring PostgreSQL database..."
systemctl enable --now postgresql

# Create role and database (idempotent)
sudo -u postgres psql -v ON_ERROR_STOP=0 <<SQL
DO \$\$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '${DB_USER}') THEN
        CREATE ROLE ${DB_USER} WITH LOGIN PASSWORD '${DB_PASSWORD}';
    ELSE
        ALTER ROLE ${DB_USER} WITH PASSWORD '${DB_PASSWORD}';
    END IF;
END
\$\$;

SELECT 'CREATE DATABASE ${DB_NAME} OWNER ${DB_USER}'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '${DB_NAME}')\gexec

GRANT ALL PRIVILEGES ON DATABASE ${DB_NAME} TO ${DB_USER};
SQL

log "PostgreSQL configured: database=${DB_NAME}, user=${DB_USER}."

# ── 5. Create system user ────────────────────────────────────────────────────

if id "$SERVICE_USER" &>/dev/null; then
    log "System user '${SERVICE_USER}' already exists."
else
    log "Creating system user '${SERVICE_USER}'..."
    useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
fi

# ── 6. Clone / copy source and build ─────────────────────────────────────────

log "Setting up ${INSTALL_DIR}..."
mkdir -p "$INSTALL_DIR" "$DATA_DIR" "$BACKUP_DIR"

# If we're running from inside the repo, copy source; otherwise expect it's already there
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -f "${SCRIPT_DIR}/go.mod" ]] && [[ "${SCRIPT_DIR}" != "${INSTALL_DIR}" ]]; then
    log "Copying source from ${SCRIPT_DIR} to ${INSTALL_DIR}..."
    rsync -a --exclude='.git' --exclude='node_modules' --exclude='bin/' "${SCRIPT_DIR}/" "${INSTALL_DIR}/"
fi

cd "$INSTALL_DIR"

if [[ ! -f "go.mod" ]]; then
    err "go.mod not found in ${INSTALL_DIR}. Clone the repo here first."
    exit 1
fi

log "Installing Node.js dependencies..."
npm ci --silent 2>/dev/null || npm install --silent

log "Building Tailwind CSS..."
npm run build:css

log "Building Go binaries..."
export PATH=$PATH:/usr/local/go/bin
mkdir -p bin

log "Installing templ code generator..."
GOBIN="$(pwd)/bin" go install github.com/a-h/templ/cmd/templ@v0.3.1001
export PATH="$(pwd)/bin:$PATH"

log "Generating templ files..."
templ generate

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/gobooks         ./cmd/gobooks
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/gobooks-migrate  ./cmd/gobooks-migrate

log "Build complete."

# ── 7. Write .env ─────────────────────────────────────────────────────────────

ENV_FILE="${INSTALL_DIR}/.env"

cat > "$ENV_FILE" <<ENVEOF
# GoBooks production configuration — generated by install.sh
APP_ENV=prod
APP_ADDR=:${APP_PORT}
LOG_LEVEL=INFO

DB_HOST=localhost
DB_PORT=5432
DB_USER=${DB_USER}
DB_PASSWORD=${DB_PASSWORD}
DB_NAME=${DB_NAME}
DB_SSLMODE=disable

AI_SECRET_KEY=${AI_SECRET_KEY}
ENVEOF

chmod 600 "$ENV_FILE"
log ".env written to ${ENV_FILE} (mode 600)."

# ── 8. Run migrations ────────────────────────────────────────────────────────

log "Running database migrations..."
cd "$INSTALL_DIR"
set -a; source .env; set +a
./bin/gobooks-migrate
log "Migrations complete."

# ── 9. Set ownership ─────────────────────────────────────────────────────────

chown -R "$SERVICE_USER":"$SERVICE_USER" "$INSTALL_DIR"
chown -R postgres:postgres "$BACKUP_DIR"
chmod 750 "$BACKUP_DIR"

# ── 10. Install systemd service ──────────────────────────────────────────────

log "Installing systemd service..."
cat > /etc/systemd/system/gobooks.service <<SVCEOF
[Unit]
Description=GoBooks Accounting System
After=network.target postgresql.service
Requires=postgresql.service

[Service]
Type=simple
User=${SERVICE_USER}
Group=${SERVICE_USER}
WorkingDirectory=${INSTALL_DIR}
EnvironmentFile=${INSTALL_DIR}/.env
ExecStartPre=${INSTALL_DIR}/bin/gobooks-migrate
ExecStart=${INSTALL_DIR}/bin/gobooks
Restart=on-failure
RestartSec=5
StandardOutput=journal
StandardError=journal

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=${INSTALL_DIR}/data
PrivateTmp=true

[Install]
WantedBy=multi-user.target
SVCEOF

systemctl daemon-reload
systemctl enable gobooks
systemctl start gobooks

log "GoBooks service started."

# ── 11. Configure Nginx reverse proxy ────────────────────────────────────────

log "Configuring Nginx reverse proxy..."
cat > /etc/nginx/sites-available/gobooks <<'NGXEOF'
server {
    listen 80 default_server;
    listen [::]:80 default_server;
    server_name _;

    client_max_body_size 10M;

    location / {
        proxy_pass http://127.0.0.1:6768;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Connection "";
        proxy_read_timeout 90s;
    }
}
NGXEOF

ln -sf /etc/nginx/sites-available/gobooks /etc/nginx/sites-enabled/gobooks
rm -f /etc/nginx/sites-enabled/default

nginx -t && systemctl reload nginx
log "Nginx configured (port 80 → GoBooks)."

# ── 12. Setup daily backup cron ──────────────────────────────────────────────

log "Setting up daily backup cron..."
cat > /etc/cron.d/gobooks-backup <<CRONEOF
# GoBooks daily database backup at 02:00 AM, keep 30 days
0 2 * * * postgres pg_dump ${DB_NAME} | gzip > ${BACKUP_DIR}/gobooks_\$(date +\%Y\%m\%d).sql.gz 2>/dev/null
0 3 * * * root find ${BACKUP_DIR} -name "gobooks_*.sql.gz" -mtime +30 -delete 2>/dev/null
CRONEOF

chmod 644 /etc/cron.d/gobooks-backup
log "Daily backup configured (${BACKUP_DIR}, 30-day retention)."

# ── 13. Firewall ─────────────────────────────────────────────────────────────

if command -v ufw &>/dev/null; then
    ufw allow OpenSSH >/dev/null 2>&1 || true
    ufw allow 'Nginx Full' >/dev/null 2>&1 || true
    ufw --force enable >/dev/null 2>&1 || true
    log "Firewall configured (SSH + HTTP/HTTPS)."
fi

# ── Done ──────────────────────────────────────────────────────────────────────

echo ""
echo -e "${GREEN}════════════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  GoBooks installed successfully!${NC}"
echo -e "${GREEN}════════════════════════════════════════════════════════════════${NC}"
echo ""
echo -e "  Open:     ${CYAN}http://$(hostname -I | awk '{print $1}')${NC}"
echo -e "  Status:   ${CYAN}sudo systemctl status gobooks${NC}"
echo -e "  Logs:     ${CYAN}sudo journalctl -u gobooks -f${NC}"
echo -e "  Backups:  ${CYAN}${BACKUP_DIR}/${NC}"
echo ""
echo -e "  ${YELLOW}Next steps:${NC}"
echo -e "  1. Open the URL above and complete the setup wizard"
echo -e "  2. (Optional) Add HTTPS:  ${CYAN}sudo certbot --nginx -d your-domain.com${NC}"
echo ""
echo -e "  DB password saved in ${CYAN}${ENV_FILE}${NC} (root-only readable)"
echo ""
