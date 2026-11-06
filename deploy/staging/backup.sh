#!/bin/sh
# Nightly staging backup (decision S-07): database dump plus uploaded media,
# checked, kept locally for BACKUP_KEEP runs and copied to R2 with rclone.
# Exits non-zero on any failure, for cron's log. The staging-edge Worker
# watches R2 and alerts on Telegram when the newest dump there is too old, so
# a failed or skipped run is noticed without a ping service.
#   15 19 * * * sh /home/ubuntu/webee_rentor/deploy/staging/backup.sh >>/home/ubuntu/webee-backup.log 2>&1
# Needs the stack running (postgres and api).
set -eu
. "$(dirname "$0")/lib.sh"

backup_dir=$(env_get BACKUP_DIR)
backup_dir=${backup_dir:-$HOME/webee-backups}
keep=$(env_get BACKUP_KEEP)
keep=${keep:-7}
remote=$(env_get BACKUP_RCLONE_REMOTE)

stamp=$(date -u +%Y%m%dT%H%M%SZ)
umask 077
mkdir -p "$backup_dir"
work="$backup_dir/.partial-$stamp"
mkdir "$work"
trap 'rm -rf "$work"' EXIT

echo "$(date -u +%FT%TZ) backup $stamp: database"
# As the application role over TCP: it owns every object it needs to dump.
dc exec -T postgres sh -c \
	'PGPASSWORD="$WEBEE_DB_PASSWORD" pg_dump -h 127.0.0.1 -U webee -d webee_rentor -Fc' >"$work/db.dump"
# A dump pg_restore cannot list is not a backup.
dc exec -T postgres pg_restore --list <"$work/db.dump" >/dev/null

echo "$(date -u +%FT%TZ) backup $stamp: media"
dc exec -T api tar -C /data/media -cf - . >"$work/media.tar"
tar -tf "$work/media.tar" >/dev/null

(cd "$work" && sha256sum db.dump media.tar >SHA256SUMS)
mv "$work" "$backup_dir/$stamp"

# Keep the newest $keep backups; names sort by time.
ls -1 "$backup_dir" | grep -E '^[0-9]{8}T[0-9]{6}Z$' | sort -r | tail -n +"$((keep + 1))" |
	while read -r old; do rm -rf "${backup_dir:?}/$old"; done

if [ -z "$remote" ]; then
	echo "warning: BACKUP_RCLONE_REMOTE is empty; this backup stays on the VM only and the monitor will report it stale" >&2
	exit 0
fi
echo "$(date -u +%FT%TZ) backup $stamp: copy to $remote"
# The dump goes last: the monitor counts a backup once its db.dump is in R2.
rclone copy --exclude db.dump "$backup_dir/$stamp" "$remote/$stamp"
rclone copyto "$backup_dir/$stamp/db.dump" "$remote/$stamp/db.dump"
echo "$(date -u +%FT%TZ) backup $stamp: done"
