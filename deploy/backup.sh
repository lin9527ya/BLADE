#!/usr/bin/env bash
# Consistent backup of PostgreSQL, data/ (SQLite traffic + evidence files),
# config.json and skills/. Stop blade first and set BLADE_BACKUP_QUIESCED=1.
set -euo pipefail

die() { printf 'backup: %s\n' "$*" >&2; exit 1; }

app_dir="${BLADE_APP_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
dsn="${BLADE_PG_DSN:-}"
backup_root="${BLADE_BACKUP_DIR:-}"
data_dir="${BLADE_DATA_DIR:-$app_dir/data}"
config_file="${BLADE_CONFIG_FILE:-$app_dir/config.json}"
skills_dir="${BLADE_SKILLS_DIR:-$app_dir/skills}"

[ -n "$dsn" ] || die "set BLADE_PG_DSN"
[ -n "$backup_root" ] || die "set BLADE_BACKUP_DIR to a protected directory"
[ -d "$data_dir" ] || die "data directory not found: $data_dir"
[ -d "$skills_dir" ] || die "skills directory not found: $skills_dir"
command -v pg_dump >/dev/null 2>&1 || die "pg_dump is required"
if [ "${BLADE_BACKUP_QUIESCED:-0}" != "1" ] && [ "${BLADE_BACKUP_ALLOW_ONLINE:-0}" != "1" ]; then
  die "stop blade first and set BLADE_BACKUP_QUIESCED=1; BLADE_BACKUP_ALLOW_ONLINE=1 accepts an inconsistent online snapshot"
fi

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
dest="$backup_root/blade-backup-$stamp"
umask 077
mkdir -p "$dest"

pg_dump "$dsn" --format=custom --no-owner --no-privileges --file="$dest/postgres.dump"
cp -a "$data_dir" "$dest/data"
cp -a "$skills_dir" "$dest/skills"
[ -f "$config_file" ] && cp -a "$config_file" "$dest/config.json"

cat > "$dest/manifest.json" <<MANIFEST
{
  "format": 1,
  "created_at": "$stamp",
  "app_dir": "$app_dir",
  "data_dir": "$data_dir",
  "config_file": "$config_file",
  "skills_dir": "$skills_dir"
}
MANIFEST

(
  cd "$dest"
  find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS
)
printf 'backup complete: %s\n' "$dest"
