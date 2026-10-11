#!/bin/sh
# Check that two Graphly instances sharing one Postgres behave as one: a write through either
# reaches WebSocket clients on both, in order; presence crosses instances; and a client that
# reconnects to the other instance replays what it missed.
#
#   scripts/multi-instance.sh                 start `docker compose --profile multi`, test, stop it
#   scripts/multi-instance.sh URL1 URL2 [..]  test instances that are already running
#
# Needs Go (the checks are TestMultiInstanceProcesses in server/multiinstance_test.go).
set -eu
cd "$(dirname "$0")/.."

if [ $# -ge 2 ]; then
	urls=$(printf '%s,' "$@")
	urls=${urls%,}
else
	docker compose --profile multi up -d --build --wait
	trap 'docker compose --profile multi down' EXIT
	urls=http://localhost:8080,http://localhost:8081
fi

for u in $(echo "$urls" | tr ',' ' '); do
	for _ in $(seq 1 30); do
		curl -fsS "$u/readyz" >/dev/null 2>&1 && break
		sleep 1
	done
done

cd server
MULTI_INSTANCE_URLS="$urls" go test -count=1 -v -run 'TestMultiInstanceProcesses' .
