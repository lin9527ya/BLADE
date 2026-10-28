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

(
  cd "$snapshot"
  sha256sum -c SHA256SUMS
)

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
stage="$app_dir/.pre-restore-$stamp"
mkdir -p "$stage"
restored_paths=(data skills config.json)
for name in "${restored_paths[@]}"; do
  [ -e "$app_dir/$name" ] && mv "$app_dir/$name" "$stage/$name"
done

rollback_files() {
  for name in "${restored_paths[@]}"; do
    rm -rf "$app_dir/$name"
    [ -e "$stage/$name" ] && mv "$stage/$name" "$app_dir/$name"
  done
}
trap 'rollback_files' ERR

for name in data skills; do
  [ -e "$snapshot/$name" ] && cp -a "$snapshot/$name" "$app_dir/$name"
done
[ -f "$snapshot/config.json" ] && cp -a "$snapshot/config.json" "$app_dir/config.json"

pg_restore --clean --if-exists --no-owner --no-privileges --dbname="$dsn" "$snapshot/postgres.dump"
trap - ERR
printf 'restore complete. Previous files kept at %s\n' "$stage"
