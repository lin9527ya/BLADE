#!/usr/bin/env bash
# P1–P7 机制 + P11 交付导出 + P12 拦截可信化 + P13 执行台账与收尾门 + P14 领域化压缩 + P18 约束可执行化 + P19 批量交付
# + P15 结构化裁决与改参放行 + P20 探索策略门 + P21 情报/测绘工具面 + P22 计划任务 + P23 公司知识
# + P24 执行限额与 MCP 熔断 + P25 MCP 工具级开关 + P26 CSV 资产导入 + P27 战果层的端到端验收清单：在一次性隔离库(artex_acceptance)上跑 server.TestAcceptance，
# 逐机制输出 PASS/FAIL——每次大改后重跑它即可，无需真实 LLM 与外部目标。
#
#   ./acceptance.sh           # 建库 → 跑清单 → 删库
#   ./acceptance.sh --keep    # 保留库（排障用）
#
# 连接信息：ARTEX_PG_DSN 优先，否则取 config.json 的 database 段。
# 建库/删库：本机 psql 可用就用它，否则回退 docker exec artex-pg。
set -euo pipefail
cd "$(dirname "$0")"

DB_NAME=artex_acceptance
KEEP=0
[ "${1:-}" = "--keep" ] && KEEP=1

eval "$(python3 - <<'PY'
import json, os, shlex, urllib.parse

def parts(dsn):
    u = urllib.parse.urlparse(dsn)
    return (u.hostname or "127.0.0.1", u.port or 5432,
            urllib.parse.unquote(u.username or "artex"),
            urllib.parse.unquote(u.password or ""))

if os.environ.get("ARTEX_PG_DSN"):
    host, port, user, pw = parts(os.environ["ARTEX_PG_DSN"])
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
  elif docker ps --format '{{.Names}}' 2>/dev/null | grep -qx artex-pg; then
    docker exec -i artex-pg env PGPASSWORD="$PG_RAW_PASSWORD" psql -U "$PG_USER" -d postgres -v ON_ERROR_STOP=1 -q -c "$1"
  else
    echo "找不到 psql，也没有 artex-pg 容器：请设置 ARTEX_PG_DSN 或安装 postgresql-client" >&2
    exit 1
  fi
}

echo "==> 重建验收库 $DB_NAME"
admin "DROP DATABASE IF EXISTS $DB_NAME"
admin "CREATE DATABASE $DB_NAME"
export ARTEX_PG_DSN="$(printf '%s' "$PG_ADMIN_DSN" | sed "s#/postgres?#/$DB_NAME?#")"

echo "==> 运行验收清单（P1–P7 + P11 + P12 + P13 + P14 + P15 + P18 + P19 + P20 + P21 + P22 + P23 + P24 + P25 + P26 + P27，逐机制 PASS/FAIL）"
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
