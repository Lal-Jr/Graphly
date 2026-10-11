#!/bin/sh
# Restore a Graphly backup made by scripts/backup.sh.
#
#   TARGET_DATABASE_URL=postgres://... scripts/restore.sh graphly-20261011T020000Z.dump
#
# Prefer restoring into a NEW, empty database and pointing DATABASE_URL at it once checked: the
# old one stays untouched as a fallback. Restoring over an existing database (--clean) replaces
# everything in it.
#
# Environment:
#   TARGET_DATABASE_URL  database to restore into (required). Create it first (createdb).
#   RESTORE_CONFIRM      set to the target database's name to skip the interactive prompt.
#
# Stop every Graphly instance first (or scale to zero). Instances that keep running would write
# into a half-restored database. Browsers that were open reload automatically on reconnect.
set -eu

dump=${1:?usage: TARGET_DATABASE_URL=... scripts/restore.sh <backup.dump>}
: "${TARGET_DATABASE_URL:?set TARGET_DATABASE_URL}"
[ -f "$dump" ] || { echo "restore: no such file: $dump" >&2; exit 1; }

if [ -f "$dump.sha256" ]; then
	(cd "$(dirname "$dump")" && sha256sum -c "$(basename "$dump").sha256" >/dev/null) || {
		echo "restore: checksum mismatch for $dump" >&2
		exit 1
	}
	echo "restore: checksum ok"
fi

db=$(psql "$TARGET_DATABASE_URL" -Atc 'SELECT current_database()')
tables=$(psql "$TARGET_DATABASE_URL" -Atc "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'")
if [ "$tables" -gt 0 ]; then
	echo "restore: $db already has $tables tables. They will be DROPPED and replaced."
fi
if [ "${RESTORE_CONFIRM:-}" != "$db" ]; then
	printf 'Type the database name (%s) to restore into it: ' "$db"
	read -r answer
	[ "$answer" = "$db" ] || { echo "restore: aborted"; exit 1; }
fi

start=$(date +%s)
# One transaction: a restore that fails halfway leaves the target as it was.
pg_restore --clean --if-exists --no-owner --no-acl --single-transaction --exit-on-error \
	--dbname="$TARGET_DATABASE_URL" "$dump"
psql "$TARGET_DATABASE_URL" -q -c 'ANALYZE'
echo "restore: $dump -> $db in $(($(date +%s) - start))s"
psql "$TARGET_DATABASE_URL" -c "SELECT
	(SELECT max(name) FROM schema_migrations) AS last_migration,
	(SELECT count(*) FROM users) AS users,
	(SELECT count(*) FROM workspaces) AS workspaces,
	(SELECT count(*) FROM projects) AS projects,
	(SELECT count(*) FROM issues) AS issues"
