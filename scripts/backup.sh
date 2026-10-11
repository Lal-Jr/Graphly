#!/bin/sh
# Logical backup of the Graphly database: a compressed pg_dump archive (custom format), checked and
# checksummed, with optional upload and local retention. Safe to run while Graphly is serving:
# pg_dump reads one consistent snapshot.
#
#   DATABASE_URL=postgres://... scripts/backup.sh [output-dir]
#
# Environment:
#   DATABASE_URL        database to back up (required)
#   BACKUP_DIR          where archives go (default ./backups, or the first argument)
#   BACKUP_KEEP_DAYS    delete local archives older than this (default 14; 0 keeps everything)
#   BACKUP_UPLOAD_CMD   optional command run with the archive path appended, e.g.
#                       "aws s3 cp --sse AES256" plus a destination: see docs/backups.md
#
# The realtime events table is backed up as schema only: it's a short-lived outbox (pruned after
# a day), and clients resync after a restore anyway.
set -eu

: "${DATABASE_URL:?set DATABASE_URL}"
dir=${1:-${BACKUP_DIR:-./backups}}
keep=${BACKUP_KEEP_DAYS:-14}
mkdir -p "$dir"

stamp=$(date -u +%Y%m%dT%H%M%SZ)
out="$dir/graphly-$stamp.dump"
tmp="$out.partial"
trap 'rm -f "$tmp"' EXIT

start=$(date +%s)
pg_dump --format=custom --compress=6 --no-owner --no-acl \
	--exclude-table-data=events \
	--file="$tmp" "$DATABASE_URL"

# A dump that can't be listed is useless; fail loudly now rather than at restore time.
pg_restore --list "$tmp" >/dev/null
mv "$tmp" "$out"
trap - EXIT
(cd "$dir" && sha256sum "$(basename "$out")" >"$(basename "$out").sha256")

size=$(du -h "$out" | cut -f1)
echo "backup: $out ($size, $(($(date +%s) - start))s)"

if [ -n "${BACKUP_UPLOAD_CMD:-}" ]; then
	# shellcheck disable=SC2086 # the command is deliberately word-split
	$BACKUP_UPLOAD_CMD "$out"
	$BACKUP_UPLOAD_CMD "$out.sha256"
	echo "backup: uploaded"
fi

if [ "$keep" -gt 0 ]; then
	find "$dir" -maxdepth 1 -name 'graphly-*.dump*' -mtime +"$keep" -print -delete | sed 's/^/backup: pruned /'
fi
