#!/bin/sh
# Restore drill (decision S-07): loads a backup into a throwaway database
# beside the live one, prints row counts, then drops it. The live database and
# media volume are never touched. Usage:
#   deploy/staging/restore.sh ~/webee-backups/20261105T021500Z [--keep]
# --keep leaves the database webee_restore_check for manual inspection; run
# the script again without --keep, or drop it by hand, when done.
set -eu
. "$(dirname "$0")/lib.sh"

src=${1:?usage: restore.sh <backup folder> [--keep]}
keep_db=${2:-}
check_db=webee_restore_check

(cd "$src" && sha256sum -c --quiet SHA256SUMS)

superuser() {
	dc exec -T postgres sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" psql -X -q -v ON_ERROR_STOP=1 -h 127.0.0.1 -U postgres -d postgres "$@"' sh "$@"
}

superuser -c "DROP DATABASE IF EXISTS $check_db" -c "CREATE DATABASE $check_db OWNER webee"
if [ "$keep_db" != "--keep" ]; then
	trap 'superuser -c "DROP DATABASE IF EXISTS '"$check_db"'"' EXIT
fi

echo "restoring database into $check_db"
dc exec -T postgres sh -c \
	'PGPASSWORD="$WEBEE_DB_PASSWORD" pg_restore -h 127.0.0.1 -U webee -d '"$check_db"' --exit-on-error --no-owner' <"$src/db.dump"

dc exec -T postgres sh -c \
	'PGPASSWORD="$WEBEE_DB_PASSWORD" psql -X -h 127.0.0.1 -U webee -d '"$check_db"' -c "
SELECT (SELECT count(*) FROM users) AS users,
       (SELECT count(*) FROM catalog_items) AS catalog_items,
       (SELECT count(*) FROM assets) AS assets,
       (SELECT count(*) FROM bookings) AS bookings,
       (SELECT count(*) FROM payments) AS payments,
       (SELECT count(*) FROM audit_events) AS audit_events"'

echo "media files in backup: $(tar -tf "$src/media.tar" | grep -cv '/$' || true)"
echo "restore drill ok"
