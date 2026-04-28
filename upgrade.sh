#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# Balanciz — Upgrade Script for Ubuntu 24.04 LTS
# ─────────────────────────────────────────────────────────────────────────────
#
# Usage:
#   sudo bash upgrade.sh                  # upgrade from the source beside this script (no git pull)
#   sudo bash upgrade.sh /path/to/source  # upgrade from a freshly pulled/cloned source tree
#
# What this script does:
#   1. Verifies the existing installation is intact
#   2. Creates a pre-upgrade database backup
#   3. Copies new source code (preserving .env and data/)
#   4. Rebuilds Go binaries, CSS, and React static assets
#   5. Runs database migrations
#   6. Restarts the Balanciz service
#   7. Verifies the service is healthy
#
# If anything fails, the database backup and old binaries remain available
# for manual rollback.
# ─────────────────────────────────────────────────────────────────────────────

set -euo pipefail

# ── Constants ─────────────────────────────────────────────────────────────────

PRIMARY_INSTALL_DIR="/opt/balanciz"
PRIMARY_BACKUP_DIR="/var/backups/balanciz"
LEGACY_INSTALL_DIR="/opt/gobooks"
LEGACY_BACKUP_DIR="/var/backups/gobooks"
GO_VERSION="1.26.1"
GO_MIN_MAJOR=1
GO_MIN_MINOR=26
GO_MIN_PATCH=0
NODE_INSTALL_MAJOR=22
APP_LABEL="Balanciz"
APP_BIN="balanciz"
MIGRATE_BIN="balanciz-migrate"
SERVICE_NAME="balanciz"
DEFAULT_DB_USER="balanciz"
DEFAULT_DB_NAME="balanciz"
DEFAULT_SERVICE_USER="balanciz"

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

if [[ -f "${PRIMARY_INSTALL_DIR}/.env" ]]; then
    INSTALL_DIR="${PRIMARY_INSTALL_DIR}"
    BACKUP_DIR="${PRIMARY_BACKUP_DIR}"
elif [[ -f "${LEGACY_INSTALL_DIR}/.env" ]]; then
    INSTALL_DIR="${LEGACY_INSTALL_DIR}"
    BACKUP_DIR="${LEGACY_BACKUP_DIR}"
    APP_BIN="gobooks"
    MIGRATE_BIN="gobooks-migrate"
    SERVICE_NAME="gobooks"
    DEFAULT_DB_USER="gobooks"
    DEFAULT_DB_NAME="gobooks"
    DEFAULT_SERVICE_USER="gobooks"
else
    INSTALL_DIR="${PRIMARY_INSTALL_DIR}"
    BACKUP_DIR="${PRIMARY_BACKUP_DIR}"
fi

if [[ ! -f "${INSTALL_DIR}/.env" ]]; then
    err "No existing installation found at ${PRIMARY_INSTALL_DIR} or ${LEGACY_INSTALL_DIR}."
    err "Run install.sh for a fresh installation."
    exit 1
fi

if [[ ! -f "${INSTALL_DIR}/bin/${APP_BIN}" ]]; then
    err "${APP_LABEL} binary not found at ${INSTALL_DIR}/bin/${APP_BIN}."
    err "Installation appears incomplete. Run install.sh instead."
    exit 1
fi

# Determine source directory
SOURCE_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)}"

if [[ ! -f "${SOURCE_DIR}/go.mod" ]]; then
    err "No go.mod found in ${SOURCE_DIR}."
    err "Usage: sudo bash upgrade.sh [/path/to/balanciz-source]"
    exit 1
fi

read_source_version() {
    local version_file="$1"
    if [[ ! -f "$version_file" ]]; then
        echo "unknown"
        return 0
    fi
    grep -oP 'Version = "\K[^"]+' "$version_file" | head -n1 || echo "unknown"
}

SOURCE_VERSION="$(read_source_version "${SOURCE_DIR}/internal/version/version.go")"
INSTALLED_SOURCE_VERSION="$(read_source_version "${INSTALL_DIR}/internal/version/version.go")"

if [[ "${SOURCE_DIR}" == "${INSTALL_DIR}" && ! -d "${SOURCE_DIR}/.git" ]]; then
    warn "Using ${INSTALL_DIR} as the upgrade source."
    warn "This directory is typically a deployed source snapshot, not a live git checkout."
    warn "upgrade.sh does not download updates from GitHub by itself."
    warn "If you expected a newer version, clone or pull the latest repo elsewhere and run:"
    warn "  sudo bash upgrade.sh /path/to/latest-balanciz-source"
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
    ver=$(go version | grep -oE 'go[0-9]+\.[0-9]+(\.[0-9]+)?' | head -n1 | sed 's/^go//')
    local maj min patch
    maj=$(echo "$ver" | cut -d. -f1)
    min=$(echo "$ver" | cut -d. -f2)
    patch=$(echo "$ver" | cut -d. -f3)
    patch="${patch:-0}"
    if [[ -z "$maj" || -z "$min" ]]; then return 1; fi
    if [[ "$maj" -gt "$GO_MIN_MAJOR" ]]; then return 0; fi
    if [[ "$maj" -lt "$GO_MIN_MAJOR" ]]; then return 1; fi
    if [[ "$min" -gt "$GO_MIN_MINOR" ]]; then return 0; fi
    if [[ "$min" -lt "$GO_MIN_MINOR" ]]; then return 1; fi
    [[ "$patch" -ge "$GO_MIN_PATCH" ]]
}

if ! go_version_ok; then
    if command -v go &>/dev/null; then
        warn "Installed Go $(go version | grep -oE 'go[0-9.]+') is below minimum ${GO_MIN_MAJOR}.${GO_MIN_MINOR}.${GO_MIN_PATCH}. Upgrading..."
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
    log "Go $(go version | grep -oE 'go[0-9.]+') OK (>= ${GO_MIN_MAJOR}.${GO_MIN_MINOR}.${GO_MIN_PATCH})."
fi

# ── Read current config ──────────────────────────────────────────────────────

node_version_ok() {
    if ! command -v node &>/dev/null; then
        return 1
    fi
    local ver major minor patch
    ver=$(node -v | sed -E 's/^v([0-9]+\.[0-9]+\.[0-9]+).*/\1/')
    major=$(echo "$ver" | cut -d. -f1)
    minor=$(echo "$ver" | cut -d. -f2)
    patch=$(echo "$ver" | cut -d. -f3)
    patch="${patch:-0}"
    if [[ -z "$major" || -z "$minor" ]]; then return 1; fi

    # Vite/Rolldown requires Node ^20.19.0 or >=22.12.0.
    if [[ "$major" -eq 20 ]]; then
        if [[ "$minor" -gt 19 ]]; then return 0; fi
        if [[ "$minor" -eq 19 && "$patch" -ge 0 ]]; then return 0; fi
        return 1
    fi
    if [[ "$major" -eq 22 ]]; then
        if [[ "$minor" -gt 12 ]]; then return 0; fi
        if [[ "$minor" -eq 12 && "$patch" -ge 0 ]]; then return 0; fi
        return 1
    fi
    [[ "$major" -gt 22 ]]
}

install_node() {
    log "Installing Node.js ${NODE_INSTALL_MAJOR}.x..."
    apt-get install -y -qq curl ca-certificates gnupg
    curl -fsSL "https://deb.nodesource.com/setup_${NODE_INSTALL_MAJOR}.x" | bash -
    apt-get install -y -qq nodejs
    log "Node.js $(node -v) installed."
}

if node_version_ok; then
    log "Node.js $(node -v) OK (compatible with Vite: ^20.19.0 or >=22.12.0)."
else
    if command -v node &>/dev/null; then
        warn "Installed Node.js $(node -v) is below the required Vite runtime (^20.19.0 or >=22.12.0). Upgrading..."
    else
        log "Node.js not found. Installing..."
    fi
    install_node
fi

set -a; source "${INSTALL_DIR}/.env"; set +a
DB_USER="${DB_USER:-${DEFAULT_DB_USER}}"
DB_NAME="${DB_NAME:-${DEFAULT_DB_NAME}}"

SERVICE_USER=$(stat -c '%U' "${INSTALL_DIR}/bin/${APP_BIN}" 2>/dev/null || echo "${DEFAULT_SERVICE_USER}")

# ── Start upgrade ─────────────────────────────────────────────────────────────

echo ""
echo -e "${CYAN}── Balanciz Upgrade ──${NC}"
echo ""
echo -e "  Install dir:  ${INSTALL_DIR}"
echo -e "  Source dir:    ${SOURCE_DIR}"
echo -e "  Installed src: ${INSTALLED_SOURCE_VERSION}"
echo -e "  Upgrade src:   ${SOURCE_VERSION}"
echo -e "  Service user:  ${SERVICE_USER}"
echo ""

# ── 1. Pre-upgrade database backup ───────────────────────────────────────────

BACKUP_FILE="${BACKUP_DIR}/${APP_BIN}_pre_upgrade_$(date +%Y%m%d_%H%M%S).sql.gz"
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

log "Stopping ${APP_LABEL} service..."
systemctl stop "${SERVICE_NAME}" 2>/dev/null || true

# ── 3. Backup old binaries ───────────────────────────────────────────────────

if [[ -f "${INSTALL_DIR}/bin/${APP_BIN}" ]]; then
    log "Backing up old binaries..."
    cp "${INSTALL_DIR}/bin/${APP_BIN}"     "${INSTALL_DIR}/bin/${APP_BIN}.bak"     2>/dev/null || true
    cp "${INSTALL_DIR}/bin/${MIGRATE_BIN}" "${INSTALL_DIR}/bin/${MIGRATE_BIN}.bak" 2>/dev/null || true
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
npm ci --include=dev --silent 2>/dev/null || npm install --include=dev --silent

log "Type-checking React TypeScript islands..."
npm run typecheck:react

log "Building Tailwind CSS..."
npm run build:css

log "Building React static assets..."
npm run build:react

log "Building Go binaries..."
mkdir -p bin

log "Installing templ code generator..."
GOBIN="$(pwd)/bin" GOFLAGS="-buildvcs=false" go install github.com/a-h/templ/cmd/templ@v0.3.1001
export PATH="$(pwd)/bin:$PATH"

log "Generating templ files..."
templ generate -build-vcs-version=false 2>/dev/null || templ generate

NEW_APP_BIN="${INSTALL_DIR}/bin/${APP_BIN}.new"
NEW_MIGRATE_BIN="${INSTALL_DIR}/bin/${MIGRATE_BIN}.new"
rm -f "$NEW_APP_BIN" "$NEW_MIGRATE_BIN"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$NEW_APP_BIN"      ./cmd/balanciz
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$NEW_MIGRATE_BIN"  ./cmd/balanciz-migrate

log "Build complete."

# ── 6. Run migrations ────────────────────────────────────────────────────────

log "Running database migrations..."
set -a; source "${INSTALL_DIR}/.env"; set +a
"${NEW_MIGRATE_BIN}"
log "Migrations complete."

log "Activating new binaries..."
mv -f "$NEW_APP_BIN" "${INSTALL_DIR}/bin/${APP_BIN}"
mv -f "$NEW_MIGRATE_BIN" "${INSTALL_DIR}/bin/${MIGRATE_BIN}"

# ── 7. Fix ownership ─────────────────────────────────────────────────────────

chown -R "$SERVICE_USER":"$SERVICE_USER" "$INSTALL_DIR"

# ── 8. Restart service ───────────────────────────────────────────────────────

log "Starting ${APP_LABEL} service..."
systemctl daemon-reload
systemctl start "${SERVICE_NAME}"

# ── 9. Health check ──────────────────────────────────────────────────────────

log "Waiting for service to become healthy..."
HEALTHY=false
for i in $(seq 1 10); do
    sleep 1
    if systemctl is-active --quiet "${SERVICE_NAME}"; then
        HEALTHY=true
        break
    fi
done

if [[ "$HEALTHY" == true ]]; then
    log "Service is running."
else
    err "Service did not start. Check logs: sudo journalctl -u ${SERVICE_NAME} -n 50"
    warn "Old binaries backed up at ${INSTALL_DIR}/bin/*.bak"
    warn "Database backup at ${BACKUP_FILE}"
    warn "To rollback: copy *.bak over binaries and restart."
    exit 1
fi

# ── 10. Clean up old backups ─────────────────────────────────────────────────

rm -f "${INSTALL_DIR}/bin/${APP_BIN}.bak" "${INSTALL_DIR}/bin/${MIGRATE_BIN}.bak"
rm -f "${INSTALL_DIR}/bin/${APP_BIN}.new" "${INSTALL_DIR}/bin/${MIGRATE_BIN}.new"

# ── Done ──────────────────────────────────────────────────────────────────────

echo ""
echo -e "${GREEN}════════════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  ${APP_LABEL} upgraded successfully!${NC}"
echo -e "${GREEN}════════════════════════════════════════════════════════════════${NC}"
echo ""
echo -e "  Status:   ${CYAN}sudo systemctl status ${SERVICE_NAME}${NC}"
echo -e "  Logs:     ${CYAN}sudo journalctl -u ${SERVICE_NAME} -f${NC}"
echo -e "  Backup:   ${CYAN}${BACKUP_FILE}${NC}"
echo ""
