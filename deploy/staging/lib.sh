# Shared by the staging scripts; sourced, not run.
# shellcheck shell=sh

staging_root=$(cd "$(dirname "$0")/../.." && pwd)
staging_env=${STAGING_ENV_FILE:-$staging_root/.env.staging}
# dc ARGS: docker compose for the staging stack (works without the exec bit).
dc() { sh "$staging_root/deploy/staging/dc" "$@"; }

# env_get NAME: the value of NAME in the staging env file (last one wins),
# without surrounding quotes. The file is never sourced: its values are not
# shell syntax.
env_get() {
	sed -n "s/^$1=//p" "$staging_env" | tail -n 1 |
		sed "s/^'\(.*\)'\$/\1/; s/^\"\(.*\)\"\$/\1/"
}

if [ ! -f "$staging_env" ]; then
	echo "missing $staging_env: copy deploy/staging/env.example and fill it in" >&2
	exit 1
fi
