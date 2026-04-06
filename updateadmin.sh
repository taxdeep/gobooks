#!/usr/bin/env bash
# updateadmin.sh — 重置 GoBooks 系统管理员密码
#
# 用法：
#   sudo bash updateadmin.sh                        # 自动搜索 .env
#   sudo bash updateadmin.sh /opt/gobooks/.env      # 指定 .env 路径

set -euo pipefail

# ── 1. 定位 .env ──────────────────────────────────────────────────────────────
ENV_FILE=""

if [[ $# -ge 1 && -f "$1" ]]; then
  ENV_FILE="$1"
else
  # 按优先级搜索常见部署位置
  for candidate in \
    /opt/gobooks/.env \
    /home/gobooks/.env \
    /srv/gobooks/.env \
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
  echo "错误：找不到 .env 文件"
  echo ""
  echo "请手动指定路径，例如："
  echo "  sudo bash updateadmin.sh /opt/gobooks/.env"
  echo ""
  echo "或手动输入数据库连接信息："
  read -rp "DB_HOST [localhost]: " DB_HOST;     DB_HOST="${DB_HOST:-localhost}"
  read -rp "DB_PORT [5432]: "     DB_PORT;     DB_PORT="${DB_PORT:-5432}"
  read -rp "DB_USER [gobooks]: "  DB_USER;     DB_USER="${DB_USER:-gobooks}"
  read -rsp "DB_PASSWORD: "       DB_PASSWORD; echo
  read -rp "DB_NAME [gobooks]: "  DB_NAME;     DB_NAME="${DB_NAME:-gobooks}"
  DB_SSLMODE="disable"
else
  echo "使用配置文件：$ENV_FILE"
  DB_HOST="localhost"; DB_PORT="5432"; DB_USER="gobooks"
  DB_PASSWORD=""; DB_NAME="gobooks"; DB_SSLMODE="disable"

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
  echo "请检查 .env 中的数据库配置"
  exit 1
fi

# ── 3. 列出所有管理员账号 ──────────────────────────────────────────────────────
echo ""
echo "当前管理员账号："
$PG -c "SELECT id, email, is_active FROM sysadmin_users ORDER BY id;"

# 让用户选择要重置的账号
echo ""
read -rp "请输入要重置密码的管理员 Email: " ADMIN_EMAIL

USER_COUNT=$($PG -tAc "SELECT COUNT(*) FROM sysadmin_users WHERE email = '$ADMIN_EMAIL';")
if [[ "$USER_COUNT" == "0" ]]; then
  echo "错误：找不到 email 为 '$ADMIN_EMAIL' 的管理员账号"
  exit 1
fi

# ── 4. 输入新密码 ──────────────────────────────────────────────────────────────
read -rsp "新密码（至少8位）: " NEW_PASSWORD; echo
read -rsp "确认新密码: "       NEW_PASSWORD2; echo

if [[ "$NEW_PASSWORD" != "$NEW_PASSWORD2" ]]; then
  echo "错误：两次输入的密码不一致"
  exit 1
fi
if [[ ${#NEW_PASSWORD} -lt 8 ]]; then
  echo "错误：密码至少需要8位"
  exit 1
fi

# ── 5. 生成 bcrypt hash（rounds=10 = Go bcrypt.DefaultCost）────────────────────
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
  echo "错误：bcrypt hash 生成失败"
  exit 1
fi

# ── 6. 写入数据库 ──────────────────────────────────────────────────────────────
$PG -c "UPDATE sysadmin_users
        SET password_hash = '$HASH', updated_at = NOW()
        WHERE email = '$ADMIN_EMAIL';"

echo ""
echo "✓ 密码已更新：$ADMIN_EMAIL"
echo "  请前往 /admin/login 使用新密码登录"
