#!/usr/bin/env bash
# Restore one backup created by deploy/backup.sh. Destructive: requires --yes,
# a stopped blade, and an explicit snapshot path.
set -euo pipefail

die() { printf 'restore: %s\n' "$*" >&2; exit 1; }

[ "${1:-}" = "--yes" ] || die "refusing to overwrite data; pass --yes after reading this script"
snapshot="${BLADE_BACKUP_SNAPSHOT:-}"
dsn="${BLADE_PG_DSN:-}"
app_dir="${BLADE_APP_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
[ -n "$snapshot" ] || die "set BLADE_BACKUP_SNAPSHOT to the blade-backup-* directory"
[ -n "$dsn" ] || die "set BLADE_PG_DSN"
[ -f "$snapshot/SHA256SUMS" ] || die "missing $snapshot/SHA256SUMS"
[ -f "$snapshot/postgres.dump" ] || die "missing $snapshot/postgres.dump"
[ "${BLADE_RESTORE_QUIESCED:-0}" = "1" ] || die "stop blade first and set BLADE_RESTORE_QUIESCED=1"
command -v pg_restore >/dev/null 2>&1 || die "pg_restore is required"
command -v pg_dump >/dev/null 2>&1 || die "pg_dump is required（恢复前要留一份当前库的快照）"

# 客户端工具比服务端**新**时会写出旧服务端不认识的语句（典型：pg_dump 17+ 的
# `SET transaction_timeout = 0`，在 PG16 上直接报 unrecognized configuration parameter）。
# 那时 pg_restore 会以非零退出，脚本会在"库已经被 --clean 删了一半"的位置中止。
# 与其事后回滚，不如在动手之前就看清楚：版本不匹配就明确失败，并给出可执行的指引。
client_major="$(pg_restore --version | sed -n 's/.*PostgreSQL) \([0-9]\+\).*/\1/p')"
server_major="$(psql "$dsn" -tAc 'show server_version' 2>/dev/null | cut -d. -f1)"
if [ -n "$client_major" ] && [ -n "$server_major" ] && [ "$client_major" -gt "$server_major" ]; then
  die "pg_restore 是 PostgreSQL $client_major，而服务端是 $server_major。"\
"新客户端写的 dump 里含有旧服务端不认识的语句（如 SET transaction_timeout），"\
"恢复会在 --clean 之后中途失败。请改用与**服务端**同版本或更旧的客户端工具"\
"（Debian/Ubuntu: 装 postgresql-client-$server_major），或用 PGDATA 里的二进制。"
fi

(
  cd "$snapshot"
  sha256sum -c SHA256SUMS
)

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
stage="$app_dir/.pre-restore-$stamp"
mkdir -p "$stage"
# 逻辑名 → 真实的仓库内路径。.env 是点文件：快照里叫 env（避免打包工具忽略点文件），
# 落回去必须写成 .env——此前两者同名处理，结果"原 .env 既不进暂存区、也不被回滚"。
restored_paths=(data skills config.json jwt.key .env)
snapshot_name_for() {
  case "$1" in
    .env) printf 'env' ;;
    *) printf '%s' "$1" ;;
  esac
}
for name in "${restored_paths[@]}"; do
  [ -e "$app_dir/$name" ] && mv "$app_dir/$name" "$stage/$(snapshot_name_for "$name")"
done

# rollback_files 把数据文件退回恢复前的状态。
#
# 两个必须注意的点（都是实测踩出来的）：
#   1. **必须幂等**：它既可能被 ERR trap 触发，也可能被下面的失败分支显式调用。
#      第二次执行时 $stage 里已经没有东西，若照常 `rm -rf` 就会把"刚被第一次回滚
#      恢复好的文件"删掉——症状是库回滚了、文件却没了。
#   2. **必须返回 0**：函数最后一条若是 `[ -e x ] && mv ...`（条件为假时返回 1），
#      在 set -e 下会把"回滚成功"变成一次新的错误，进而再触发 ERR trap。
rolled_back=0
rollback_files() {
  [ "$rolled_back" = "1" ] && return 0
  rolled_back=1
  for name in "${restored_paths[@]}"; do
    rm -rf "$app_dir/$name"
    snap="$(snapshot_name_for "$name")"
    if [ -e "$stage/$snap" ]; then mv "$stage/$snap" "$app_dir/$name"; fi
  done
  return 0
}
trap 'rollback_files' ERR

for name in data skills; do
  [ -e "$snapshot/$name" ] && cp -a "$snapshot/$name" "$app_dir/$name"
done
[ -f "$snapshot/config.json" ] && cp -a "$snapshot/config.json" "$app_dir/config.json"
# 这两个只在备份里确实存在时才覆盖（见 backup.sh 的说明）。
[ -f "$snapshot/jwt.key" ] && cp -a "$snapshot/jwt.key" "$app_dir/jwt.key"
[ -f "$snapshot/env" ] && cp -a "$snapshot/env" "$app_dir/.env"

# pg_restore --clean 会**先删后建**：中途失败时数据库已经被删掉一半，而此前只有
# 文件回滚、没有库回滚，结果是"数据没了、脚本还说恢复了文件"。先导一份"恢复前"
# 快照，失败时用它尽力回滚（它本身失败则明确告诉操作者手工处理）。
pre_dump="$stage/pre-restore.dump"
pg_dump "$dsn" --format=custom --no-owner --no-privileges --file="$pre_dump" \
  || die "无法在恢复前备份当前数据库；已中止（文件尚未改动）"

restore_failed=0
if ! pg_restore --clean --if-exists --no-owner --no-privileges --dbname="$dsn" "$snapshot/postgres.dump"; then
  restore_failed=1
  printf 'restore: pg_restore 失败，正在用恢复前快照回滚数据库…\n' >&2
  if ! pg_restore --clean --if-exists --no-owner --no-privileges --dbname="$dsn" "$pre_dump"; then
    printf 'restore: 数据库回滚也失败了，请手工处理：%s\n' "$pre_dump" >&2
    printf 'restore: 文件仍保留在 %s\n' "$stage" >&2
    exit 1
  fi
  printf 'restore: 数据库已回滚到恢复前状态\n' >&2
fi
if [ "$restore_failed" = "1" ]; then
  # 把数据文件也退回去。注意：**不能只靠 ERR trap** —— 上面把 pg_restore 包在
  # `if !` 里之后，它失败不会再触发 ERR（这正是回滚测试抓到的洞：库回滚了、
  # 文件却停在"已恢复"状态，用户看到一半新一半旧）。
  trap - ERR
  rollback_files
  printf 'restore: 文件也已回滚到恢复前状态（快照保留在 %s）\n' "$stage" >&2
  exit 1
fi
trap - ERR
printf 'restore complete. Previous files kept at %s\n' "$stage"
