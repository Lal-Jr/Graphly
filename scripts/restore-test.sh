#!/bin/sh
# Restore drill: prove a backup restores into a working Graphly database, and time it (your RTO).
#
#   ADMIN_DATABASE_URL=postgres://user:pass@host:5432/postgres scripts/restore-test.sh [backup.dump]
#
# With no argument it restores the newest archive in BACKUP_DIR (default ./backups). It:
#   1. creates a scratch database (graphly_restore_test_<timestamp>) on the admin connection's server,
#   2. restores the archive into it with scripts/restore.sh,
#   3. checks every table's row count against the rows in the archive itself, and that the
#      append-only audit log trigger and the migrations table came back,
#   4. if GRAPHLY_BIN points at a built server, boots it on the restored database and waits for
#      /readyz (the app runs its migrations on start, so this also proves a newer binary can
#      upgrade the backup),
#   5. drops the scratch database (KEEP_RESTORE_DB=1 keeps it for inspection).
#
# Run it on a schedule (weekly is a good start) and alert when it fails. See docs/backups.md.
set -eu

: "${ADMIN_DATABASE_URL:?set ADMIN_DATABASE_URL (a connection that may CREATE DATABASE)}"
here=$(cd "$(dirname "$0")" && pwd)
dump=${1:-$(ls -1t "${BACKUP_DIR:-./backups}"/graphly-*.dump 2>/dev/null | head -1)}
[ -n "$dump" ] && [ -f "$dump" ] || { echo "restore-test: no backup found" >&2; exit 1; }

scratch="graphly_restore_test_$(date -u +%Y%m%d%H%M%S)"
target=$(printf '%s' "$ADMIN_DATABASE_URL" | sed -E "s#/[^/?]*(\?|$)#/$scratch\1#")
psql "$ADMIN_DATABASE_URL" -q -c "CREATE DATABASE $scratch"
pid=""
cleanup() {
	[ -n "$pid" ] && kill "$pid" 2>/dev/null || true
	if [ "${KEEP_RESTORE_DB:-}" = 1 ]; then
		echo "restore-test: kept $scratch"
	else
		psql "$ADMIN_DATABASE_URL" -q -c "DROP DATABASE IF EXISTS $scratch WITH (FORCE)"
	fi
}
trap cleanup EXIT

start=$(date +%s)
TARGET_DATABASE_URL="$target" RESTORE_CONFIRM="$scratch" "$here/restore.sh" "$dump" >/dev/null
restored=$(($(date +%s) - start))

# Row counts: what the archive holds vs. what landed in the database.
fail=0
for table in $(pg_restore --list "$dump" | awk '$4 == "TABLE" && $5 == "DATA" { print $7 }' | sort -u); do
	in_archive=$(pg_restore --data-only --table="$table" --file=- "$dump" |
		awk '/^COPY /{on=1; next} /^\\\.$/{on=0} on{n++} END{print n+0}')
	in_db=$(psql "$target" -Atc "SELECT count(*) FROM public.\"$table\"")
	if [ "$in_archive" != "$in_db" ]; then
		echo "restore-test: $table has $in_db rows, archive has $in_archive" >&2
		fail=1
	fi
done
[ "$(psql "$target" -Atc "SELECT count(*) FROM pg_trigger WHERE tgname LIKE 'audit_log_%'")" -gt 0 ] ||
	{ echo "restore-test: audit log trigger missing" >&2; fail=1; }
[ "$(psql "$target" -Atc 'SELECT count(*) FROM schema_migrations')" -gt 0 ] ||
	{ echo "restore-test: migrations table empty" >&2; fail=1; }

if [ -n "${GRAPHLY_BIN:-}" ]; then
	port=${RESTORE_TEST_PORT:-18080}
	DATABASE_URL="$target" ADDR=":$port" LOG_REQUESTS=false "$GRAPHLY_BIN" >/dev/null 2>&1 &
	pid=$!
	ok=""
	for _ in $(seq 1 60); do
		if curl -fsS "http://localhost:$port/readyz" >/dev/null 2>&1; then ok=1; break; fi
		sleep 1
	done
	[ -n "$ok" ] || { echo "restore-test: Graphly did not become ready on the restored database" >&2; fail=1; }
fi

[ "$fail" = 0 ] || exit 1
echo "restore-test: ok ($(basename "$dump"), restored in ${restored}s, total $(($(date +%s) - start))s)"
