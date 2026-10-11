# Backups and restore

All of Graphly's state lives in Postgres: accounts, sessions, workspaces, projects, issues,
links, comments, forecast history, notifications, the delivery outbox, API tokens (hashed),
webhook secrets, the audit log and import mappings. The server keeps nothing on disk, so backing
up the database is backing up Graphly. Secrets in the environment (`DATABASE_URL`, OAuth client
secrets, SMTP password, `METRICS_TOKEN`) live in your secret store and need their own backup.

## Two layers

**1. Point-in-time recovery (recommended for production).** Use your provider's continuous
backup: RDS/Aurora automated backups, Cloud SQL PITR, Neon/Supabase/Crunchy branching. Self-hosted,
use WAL archiving with pgBackRest or WAL-G. This is what keeps data loss to seconds or minutes.

**2. Logical dumps (`scripts/backup.sh`).** Portable, provider-independent archives. You can
restore them anywhere: a laptop, another cloud, a scratch database for an investigation. Also
your fallback if the PITR system itself is the problem.

```sh
DATABASE_URL=postgres://... scripts/backup.sh /var/backups/graphly
# hourly, with upload and 14 days of local copies:
# 0 * * * *  DATABASE_URL=... BACKUP_UPLOAD_CMD="aws s3 cp --sse AES256" \
#            sh -c 'scripts/backup.sh /var/backups/graphly'   # (upload cmd gets the file path appended;
#                                                             #  wrap it in a script to add a destination)
```

The archive is `pg_dump --format=custom`, compressed. It's listed (to prove it's readable) and
checksummed (`.sha256`) before the script reports success. It's taken from one consistent
snapshot while Graphly keeps serving. The realtime `events` table is saved as schema only: it's a
one-day outbox, and clients reload after a restore anyway.

Store archives off the database host, in a different account or project from production, with
object lock or versioning. Treat them as sensitive: they contain emails, password hashes,
session token hashes, webhook secrets and everything users wrote.

## RPO and RTO

| | PITR | Hourly `backup.sh` |
|---|---|---|
| RPO (data you can lose) | Seconds to ~5 minutes (WAL archive interval) | Up to 1 hour, i.e. the dump interval |
| RTO (time to be back) | Provider restore time (typically 10 to 30 minutes) plus a deploy | Restore time plus a deploy, see below |

Measured: a database holding a 5,000-issue project (17k links) plus test data is a 0.55 MB
archive. It dumps in under a second and restores in about one, and `scripts/restore-test.sh`
completes, including booting Graphly on the copy, in about 5 seconds. Restore time grows roughly
linearly with size. Run the drill on your real data to learn your own number, and budget about
15 minutes on top for the deploy and checks.

Pick the dump interval from the RPO you can accept. Hourly is a reasonable default next to PITR.
Without PITR, run it every 15 minutes.

## Restore drill (do this regularly)

```sh
ADMIN_DATABASE_URL=postgres://admin@host/postgres BACKUP_DIR=/var/backups/graphly \
GRAPHLY_BIN=./graphly scripts/restore-test.sh
```

It restores the newest archive into a scratch database. It then checks every table's row count
against the archive, checks the audit log's append-only triggers and the migrations table, boots
Graphly on the copy until `/readyz` passes, prints the timings and drops the copy. Schedule it
weekly. Alert on a non-zero exit and on the newest archive being older than twice the dump
interval.

## Runbook: restoring production

1. **Decide the restore point.** For PITR, the timestamp just before the incident. For dumps,
   the newest archive before it. Note what will be lost: everything after that point.
2. **Stop writes.** Scale every Graphly instance to zero, or put the load balancer in
   maintenance. Instances left running would write into a half-restored database.
3. **Restore into a new database.** Don't overwrite the damaged one; keep it for forensics and
   as a fallback.
   - PITR: use your provider's "restore to point in time", which creates a new instance.
   - Dump:
     ```sh
     createdb -h host -U admin graphly_restored
     TARGET_DATABASE_URL=postgres://admin@host/graphly_restored scripts/restore.sh graphly-<stamp>.dump
     ```
     The restore runs in one transaction, so a failure leaves the target empty, not half-loaded.
     The script verifies the checksum first.
4. **Check it.** Look at the counts `restore.sh` prints, and spot-check a recent project.
5. **Point Graphly at it.** Update `DATABASE_URL`, then start one instance and wait for `/readyz`.
   It runs any newer migrations on start. Then scale back up.
6. **Clients.** Open browsers reconnect and reload on their own. The server tells any client
   holding a newer event sequence than the restored database to resync. People signed in after
   the restore point must sign in again.
7. **Integrations.** Webhook secrets are restored with the data, so GitHub and GitLab hooks
   keep working. If you rotated a secret after the restore point, rotate it again in Settings and
   update the hook. Pull requests and commits pushed between the restore point and now won't be
   linked until their next event.
8. **Notifications.** Deliveries that were pending at the restore point will be sent once more.
   People may get a repeat digest.
9. **Write it down.** Note the restore point, the data lost, the timings and anything that
   surprised you, and feed the timings back into the RTO above.
