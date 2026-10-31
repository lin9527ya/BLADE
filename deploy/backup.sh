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
# jwt.key 与 .env 也属于"恢复时要一起回来"的状态，此前不在备份集里：
#   - jwt.key 只在把数据恢复到**旧版本**二进制时才需要（v0.7.x 起库是权威存储），
#     但少了它就无法回滚到只认文件的版本；
#   - .env 持有 POSTGRES_PASSWORD，compose 部署没有它起不来。
jwt_key_file="${BLADE_JWT_KEY_FILE:-$app_dir/jwt.key}"
env_file="${BLADE_ENV_FILE:-$app_dir/.env}"

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
# jwt.key / .env 缺失是正常情况（自更新镜像、非 compose 部署），不作为失败条件。
jwt_key_backed_up=0
if [ -f "$jwt_key_file" ]; then cp -a "$jwt_key_file" "$dest/jwt.key"; jwt_key_backed_up=1; fi
env_backed_up=0
if [ -f "$env_file" ]; then cp -a "$env_file" "$dest/env"; env_backed_up=1; fi

cat > "$dest/manifest.json" <<MANIFEST
{
  "format": 1,
  "created_at": "$stamp",
  "app_dir": "$app_dir",
  "data_dir": "$data_dir",
  "config_file": "$config_file",
  "skills_dir": "$skills_dir",
  "jwt_key_file": "$jwt_key_file",
  "jwt_key_backed_up": $jwt_key_backed_up,
  "env_file": "$env_file",
  "env_backed_up": $env_backed_up,
  "note": "jwt.key 仅是回滚到旧版本的兼容镜像（库内 auth.jwt_key 才是权威）；env 是 compose 的 POSTGRES_PASSWORD 等"
}
MANIFEST

(
  cd "$dest"
  find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS
)
printf 'backup complete: %s\n' "$dest"
