#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# GoBooks — Upgrade Script for Ubuntu 24.04 LTS
# ─────────────────────────────────────────────────────────────────────────────
#
# Usage:
#   sudo bash upgrade.sh                  # upgrade from current directory
#   sudo bash upgrade.sh /path/to/source  # upgrade from specified source
#
# What this script does:
#   1. Verifies the existing installation is intact
#   2. Creates a pre-upgrade database backup
#   3. Copies new source code (preserving .env and data/)
#   4. Rebuilds Go binaries and Tailwind CSS
#   5. Runs database migrations
#   6. Restarts the GoBooks service
#   7. Verifies the service is healthy
#
# If anything fails, the database backup and old binaries remain available
# for manual rollback.
# ─────────────────────────────────────────────────────────────────────────────

set -euo pipefail

# ── Constants ─────────────────────────────────────────────────────────────────

INSTALL_DIR="/opt/gobooks"
BACKUP_DIR="/var/backups/gobooks"
GO_VERSION="1.23.6"
GO_MIN_MAJOR=1
GO_MIN_MINOR=23
NODE_MAJOR=20

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

log()  { echo -e "${GREEN}[Upgrade]${NC} $*"; }
warn() { echo -e "${YELLOW}[Upgrade]${NC} $*"; }
err()  { echo -e "${RED}[Upgrade]${NC} $*" >&2; }

# ── Pre-flight checks ────────────────────────────────────────────────────────

if [[ $EUID -ne 0 ]]; then
    err "This script must be run as root (sudo bash upgrade.sh)"
    exit 1
fi

if [[ ! -f "${INSTALL_DIR}/.env" ]]; then
    err "No existing installation found at ${INSTALL_DIR}."
    err "Run install.sh for a fresh installation."
    exit 1
fi

if [[ ! -f "${INSTALL_DIR}/bin/gobooks" ]]; then
    err "GoBooks binary not found at ${INSTALL_DIR}/bin/gobooks."
    err "Installation appears incomplete. Run install.sh instead."
    exit 1
fi

# Determine source directory
SOURCE_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)}"

if [[ ! -f "${SOURCE_DIR}/go.mod" ]]; then
    err "No go.mod found in ${SOURCE_DIR}."
    err "Usage: sudo bash upgrade.sh [/path/to/gobooks-source]"
    exit 1
fi

if ! command -v rsync &>/dev/null; then
    log "Installing missing dependency: rsync..."
    apt-get update -qq
    apt-get install -y -qq rsync
fi

# ── Ensure Go is available and meets minimum version ─────────────────────────

export PATH=$PATH:/usr/local/go/bin

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

if ! go_version_ok; then
    if command -v go &>/dev/null; then
        warn "Installed Go $(go version | grep -oP 'go[0-9.]+') is below minimum ${GO_MIN_MAJOR}.${GO_MIN_MINOR}. Upgrading..."
    else
        log "Go not found. Installing..."
    fi
    rm -rf /usr/local/go
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | tar -C /usr/local -xzf -
    cat > /etc/profile.d/golang.sh <<'GOEOF'
export PATH=$PATH:/usr/local/go/bin
GOEOF
    export PATH=$PATH:/usr/local/go/bin
    hash -r 2>/dev/null || true
    log "Go $(go version) installed."
else
    log "Go $(go version | grep -oP 'go[0-9.]+') OK (>= ${GO_MIN_MAJOR}.${GO_MIN_MINOR})."
fi

# ── Read current config ──────────────────────────────────────────────────────

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
    apt-get install -y -qq curl ca-certificates gnupg
    curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash -
    apt-get install -y -qq nodejs
    log "Node.js $(node -v) installed."
}

if node_version_ok; then
    log "Node.js $(node -v) OK (>= ${NODE_MAJOR})."
else
    if command -v node &>/dev/null; then
        warn "Installed Node.js $(node -v) is below minimum ${NODE_MAJOR}. Upgrading..."
    else
        log "Node.js not found. Installing..."
    fi
    install_node
fi

set -a; source "${INSTALL_DIR}/.env"; set +a
DB_USER="${DB_USER:-gobooks}"
DB_NAME="${DB_NAME:-gobooks}"

SERVICE_USER=$(stat -c '%U' "${INSTALL_DIR}/bin/gobooks" 2>/dev/null || echo "gobooks")

# ── Start upgrade ─────────────────────────────────────────────────────────────

echo ""
echo -e "${CYAN}── GoBooks Upgrade ──${NC}"
echo ""
echo -e "  Install dir:  ${INSTALL_DIR}"
echo -e "  Source dir:    ${SOURCE_DIR}"
echo -e "  Service user:  ${SERVICE_USER}"
echo ""

# ── 1. Pre-upgrade database backup ───────────────────────────────────────────

BACKUP_FILE="${BACKUP_DIR}/gobooks_pre_upgrade_$(date +%Y%m%d_%H%M%S).sql.gz"
mkdir -p "$BACKUP_DIR"

log "Creating pre-upgrade database backup..."
if sudo -u postgres pg_dump "$DB_NAME" | gzip > "$BACKUP_FILE"; then
    BACKUP_SIZE=$(du -h "$BACKUP_FILE" | cut -f1)
    log "Backup saved: ${BACKUP_FILE} (${BACKUP_SIZE})"
else
    err "Database backup failed. Upgrade aborted."
    exit 1
fi

# ── 2. Stop service ──────────────────────────────────────────────────────────

log "Stopping GoBooks service..."
systemctl stop gobooks 2>/dev/null || true

# ── 3. Backup old binaries ───────────────────────────────────────────────────

if [[ -f "${INSTALL_DIR}/bin/gobooks" ]]; then
    log "Backing up old binaries..."
    cp "${INSTALL_DIR}/bin/gobooks"         "${INSTALL_DIR}/bin/gobooks.bak"         2>/dev/null || true
    cp "${INSTALL_DIR}/bin/gobooks-migrate" "${INSTALL_DIR}/bin/gobooks-migrate.bak" 2>/dev/null || true
fi

# ── 4. Sync source code ──────────────────────────────────────────────────────

log "Syncing source code from ${SOURCE_DIR}..."
if [[ "${SOURCE_DIR}" != "${INSTALL_DIR}" ]]; then
    rsync -a \
        --exclude='.git' \
        --exclude='node_modules' \
        --exclude='bin/' \
        --exclude='.env' \
        --exclude='data/' \
        --exclude='*.bak' \
        "${SOURCE_DIR}/" "${INSTALL_DIR}/"
else
    log "Source directory matches install directory; using in-place checked out files."
fi

# ── 5. Rebuild ────────────────────────────────────────────────────────────────

cd "$INSTALL_DIR"

log "Installing Node.js dependencies..."
npm ci --silent 2>/dev/null || npm install --silent

log "Building Tailwind CSS..."
npm run build:css

log "Building Go binaries..."
mkdir -p bin

log "Installing templ code generator..."
GOBIN="$(pwd)/bin" go install github.com/a-h/templ/cmd/templ@v0.3.1001
export PATH="$(pwd)/bin:$PATH"

log "Generating templ files..."
templ generate

NEW_APP_BIN="${INSTALL_DIR}/bin/gobooks.new"
NEW_MIGRATE_BIN="${INSTALL_DIR}/bin/gobooks-migrate.new"
rm -f "$NEW_APP_BIN" "$NEW_MIGRATE_BIN"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$NEW_APP_BIN"      ./cmd/gobooks
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$NEW_MIGRATE_BIN"  ./cmd/gobooks-migrate

log "Build complete."

# ── 6. Run migrations ────────────────────────────────────────────────────────

log "Running database migrations..."
set -a; source "${INSTALL_DIR}/.env"; set +a
"${NEW_MIGRATE_BIN}"
log "Migrations complete."

log "Activating new binaries..."
mv -f "$NEW_APP_BIN" "${INSTALL_DIR}/bin/gobooks"
mv -f "$NEW_MIGRATE_BIN" "${INSTALL_DIR}/bin/gobooks-migrate"

# ── 7. Fix ownership ─────────────────────────────────────────────────────────

chown -R "$SERVICE_USER":"$SERVICE_USER" "$INSTALL_DIR"

# ── 8. Restart service ───────────────────────────────────────────────────────

log "Starting GoBooks service..."
systemctl daemon-reload
systemctl start gobooks

# ── 9. Health check ──────────────────────────────────────────────────────────

log "Waiting for service to become healthy..."
HEALTHY=false
for i in $(seq 1 10); do
    sleep 1
    if systemctl is-active --quiet gobooks; then
        HEALTHY=true
        break
    fi
done

if [[ "$HEALTHY" == true ]]; then
    log "Service is running."
else
    err "Service did not start. Check logs: sudo journalctl -u gobooks -n 50"
    warn "Old binaries backed up at ${INSTALL_DIR}/bin/*.bak"
    warn "Database backup at ${BACKUP_FILE}"
    warn "To rollback: copy *.bak over binaries and restart."
    exit 1
fi

# ── 10. Clean up old backups ─────────────────────────────────────────────────

rm -f "${INSTALL_DIR}/bin/gobooks.bak" "${INSTALL_DIR}/bin/gobooks-migrate.bak"
rm -f "${INSTALL_DIR}/bin/gobooks.new" "${INSTALL_DIR}/bin/gobooks-migrate.new"

# ── Done ──────────────────────────────────────────────────────────────────────

echo ""
echo -e "${GREEN}════════════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  GoBooks upgraded successfully!${NC}"
echo -e "${GREEN}════════════════════════════════════════════════════════════════${NC}"
echo ""
echo -e "  Status:   ${CYAN}sudo systemctl status gobooks${NC}"
echo -e "  Logs:     ${CYAN}sudo journalctl -u gobooks -f${NC}"
echo -e "  Backup:   ${CYAN}${BACKUP_FILE}${NC}"
echo ""
