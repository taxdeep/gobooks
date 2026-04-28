#!/usr/bin/env bash
# updateadmin.sh — 创建或重置 Balanciz 系统管理员账号
#
# 用法：
#   sudo bash updateadmin.sh                    # 自动搜索 .env
#   sudo bash updateadmin.sh /opt/balanciz/.env  # 指定 .env 路径

set -euo pipefail

# ── 1. 定位 .env ──────────────────────────────────────────────────────────────
ENV_FILE=""

if [[ $# -ge 1 && -f "$1" ]]; then
  ENV_FILE="$1"
else
  for candidate in \
    /opt/balanciz/.env \
    /opt/gobooks/.env \
    /home/balanciz/.env \
    /home/gobooks/.env \
    /srv/balanciz/.env \
    /srv/gobooks/.env \
    /var/www/balanciz/.env \
    /var/www/gobooks/.env \
    "$(dirname "$0")/.env" \
    "$HOME/.env"
  do
    if [[ -f "$candidate" ]]; then
      ENV_FILE="$candidate"
      break
    fi
  done
fi

if [[ -z "$ENV_FILE" ]]; then
  echo "找不到 .env，请手动输入数据库连接信息："
  read -rp  "DB_HOST [localhost]: " DB_HOST;     DB_HOST="${DB_HOST:-localhost}"
  read -rp  "DB_PORT [5432]: "     DB_PORT;     DB_PORT="${DB_PORT:-5432}"
  read -rp  "DB_USER [balanciz]: "  DB_USER;     DB_USER="${DB_USER:-balanciz}"
  read -rsp "DB_PASSWORD: "        DB_PASSWORD; echo
  read -rp  "DB_NAME [balanciz]: "  DB_NAME;     DB_NAME="${DB_NAME:-balanciz}"
  DB_SSLMODE="disable"
else
  echo "使用配置文件：$ENV_FILE"
  DB_HOST="localhost"; DB_PORT="5432"; DB_USER="balanciz"
  DB_PASSWORD=""; DB_NAME="balanciz"; DB_SSLMODE="disable"

  while IFS='=' read -r key value; do
    [[ "$key" =~ ^[[:space:]]*# || -z "${key// }" ]] && continue
    key="$(echo "$key" | xargs)"
    value="$(echo "$value" | xargs)"
    case "$key" in
      DB_HOST)     DB_HOST="$value" ;;
      DB_PORT)     DB_PORT="$value" ;;
      DB_USER)     DB_USER="$value" ;;
      DB_PASSWORD) DB_PASSWORD="$value" ;;
      DB_NAME)     DB_NAME="$value" ;;
      DB_SSLMODE)  DB_SSLMODE="$value" ;;
    esac
  done < "$ENV_FILE"
fi

# ── 2. 测试数据库连接 ──────────────────────────────────────────────────────────
export PGPASSWORD="$DB_PASSWORD"
PG="psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME --no-password"

if ! $PG -tAc "SELECT 1;" >/dev/null 2>&1; then
  echo "错误：无法连接数据库（$DB_USER@$DB_HOST:$DB_PORT/$DB_NAME）"
  exit 1
fi

# ── 3. 检查是否已有管理员 ──────────────────────────────────────────────────────
ADMIN_COUNT=$($PG -tAc "SELECT COUNT(*) FROM sysadmin_users;")

if [[ "$ADMIN_COUNT" == "0" ]]; then
  echo ""
  echo "当前没有管理员账号，将创建新账号。"
  MODE="create"
else
  echo ""
  echo "当前管理员账号："
  $PG -c "SELECT id, email, is_active FROM sysadmin_users ORDER BY id;"
  MODE="update"
fi

# ── 4. 输入 Email ─────────────────────────────────────────────────────────────
echo ""
read -rp "管理员 Email: " ADMIN_EMAIL

if [[ "$MODE" == "update" ]]; then
  USER_COUNT=$($PG -tAc "SELECT COUNT(*) FROM sysadmin_users WHERE email = '$ADMIN_EMAIL';")
  if [[ "$USER_COUNT" == "0" ]]; then
    echo ""
    read -rp "该 Email 不存在，是否创建新账号？[y/N] " CONFIRM
    [[ "$CONFIRM" =~ ^[Yy]$ ]] || exit 0
    MODE="create"
  fi
fi

# ── 5. 输入新密码 ──────────────────────────────────────────────────────────────
read -rsp "密码（至少8位）: "  NEW_PASSWORD;  echo
read -rsp "确认密码: "         NEW_PASSWORD2; echo

if [[ "$NEW_PASSWORD" != "$NEW_PASSWORD2" ]]; then
  echo "错误：两次输入的密码不一致"; exit 1
fi
if [[ ${#NEW_PASSWORD} -lt 8 ]]; then
  echo "错误：密码至少需要8位"; exit 1
fi

# ── 6. 生成 bcrypt hash（rounds=10 = Go bcrypt.DefaultCost）────────────────────
if ! python3 -c "import bcrypt" 2>/dev/null; then
  echo "正在安装 python3-bcrypt..."
  pip3 install bcrypt --quiet
fi

HASH=$(python3 -c "
import bcrypt, sys
pw = sys.argv[1].encode()
print(bcrypt.hashpw(pw, bcrypt.gensalt(rounds=10)).decode())
" "$NEW_PASSWORD")

if [[ -z "$HASH" ]]; then
  echo "错误：bcrypt hash 生成失败"; exit 1
fi

# ── 7. 写入数据库 ──────────────────────────────────────────────────────────────
if [[ "$MODE" == "create" ]]; then
  $PG -c "INSERT INTO sysadmin_users (email, password_hash, is_active, created_at, updated_at)
          VALUES ('$ADMIN_EMAIL', '$HASH', true, NOW(), NOW());"
  echo ""
  echo "✓ 管理员账号已创建：$ADMIN_EMAIL"
else
  $PG -c "UPDATE sysadmin_users
          SET password_hash = '$HASH', updated_at = NOW()
          WHERE email = '$ADMIN_EMAIL';"
  echo ""
  echo "✓ 密码已更新：$ADMIN_EMAIL"
fi

echo "  请前往 /admin/login 登录"
