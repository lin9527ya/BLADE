#!/usr/bin/env bash
# 验收清单：P1–P7、P11–P15、P18–P50（共 45 个阶段；P8–P10、P16–P17 从未实现，编号留空）。
# 权威清单以 server/acceptance_test.go 里的 t.Run("P…") 为准——本注释只写范围，
# 不再逐条枚举（此前枚举到 P42 就停更，落后了两个版本）。
#
# 在一次性隔离库(blade_acceptance)上跑 server.TestAcceptance，逐机制输出 PASS/FAIL——
# 每次大改后重跑它即可，无需真实 LLM 与外部目标。
#
#   ./acceptance.sh           # 建库 → 跑清单 → 删库
#   ./acceptance.sh --keep    # 保留库（排障用）
#
# 连接信息：BLADE_PG_DSN 优先，否则取 config.json 的 database 段。
# 建库/删库：本机 psql 可用就用它，否则回退 docker exec blade-pg。
# 注意：本脚本会设置 BLADE_REQUIRE_ACCEPTANCE=1——连不上库时 TestAcceptance 必须失败，
# 不能以整体 Skip 的退出码 0 伪装成"全部通过"。
set -euo pipefail
cd "$(dirname "$0")"

DB_NAME=blade_acceptance
KEEP=0
[ "${1:-}" = "--keep" ] && KEEP=1

eval "$(python3 - <<'PY'
import json, os, shlex, urllib.parse

def parts(dsn):
    u = urllib.parse.urlparse(dsn)
    return (u.hostname or "127.0.0.1", u.port or 5432,
            urllib.parse.unquote(u.username or "blade"),
            urllib.parse.unquote(u.password or ""))

if os.environ.get("BLADE_PG_DSN"):
    host, port, user, pw = parts(os.environ["BLADE_PG_DSN"])
else:
    d = json.load(open("config.json"))["database"]
    host, port, user, pw = d["host"], int(d["port"]), d["user"], d["password"]

admin_dsn = "postgres://%s:%s@%s:%d/postgres?sslmode=disable" % (
    urllib.parse.quote(user, safe=""), urllib.parse.quote(pw, safe=""), host, port)
print(f"export PG_ADMIN_DSN={shlex.quote(admin_dsn)}")
print(f"export PG_HOST={shlex.quote(str(host))}")
print(f"export PG_PORT={shlex.quote(str(port))}")
print(f"export PG_USER={shlex.quote(user)}")
print(f"export PG_RAW_PASSWORD={shlex.quote(pw)}")
PY
)"

admin() {
  if command -v psql >/dev/null 2>&1; then
    PGPASSWORD="$PG_RAW_PASSWORD" psql -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d postgres -v ON_ERROR_STOP=1 -q -c "$1"
  elif docker ps --format '{{.Names}}' 2>/dev/null | grep -qx blade-pg; then
    docker exec -i blade-pg env PGPASSWORD="$PG_RAW_PASSWORD" psql -U "$PG_USER" -d postgres -v ON_ERROR_STOP=1 -q -c "$1"
  else
    echo "找不到 psql，也没有 blade-pg 容器：请设置 BLADE_PG_DSN 或安装 postgresql-client" >&2
    exit 1
  fi
}

echo "==> 重建验收库 $DB_NAME"
admin "DROP DATABASE IF EXISTS $DB_NAME"
# TEMPLATE template0：template1 可能带着本机自己加的东西，也会在 PG 升级后与系统
# collation 版本对不上（此时 `CREATE DATABASE` 直接失败，报
# "template database template1 has a collation version mismatch"）。验收库是一次性
# 的、不需要继承任何本地模板，用最干净的 template0 最稳。
admin "CREATE DATABASE $DB_NAME TEMPLATE template0"
export BLADE_PG_DSN="$(printf '%s' "$PG_ADMIN_DSN" | sed "s#/postgres?#/$DB_NAME?#")"
# 显式要求验收：TestAcceptance 连不上库时失败而不是 Skip（Skip 的退出码是 0）。
export BLADE_REQUIRE_ACCEPTANCE=1

echo "==> 运行验收清单（P1–P7、P11–P15、P18–P50，逐机制 PASS/FAIL；权威清单见 server/acceptance_test.go）"
rc=0
go test -count=1 -run TestAcceptance -v ./server/ || rc=1

if [ "$KEEP" = "1" ]; then
  echo "==> 保留验收库 $DB_NAME（--keep）"
else
  admin "DROP DATABASE IF EXISTS $DB_NAME" >/dev/null
fi
if [ "$rc" = "0" ]; then
  echo "==> ✅ 验收清单全部通过"
else
  echo "==> ❌ 验收清单存在失败项" >&2
fi
exit "$rc"
