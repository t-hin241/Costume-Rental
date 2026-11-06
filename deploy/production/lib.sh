# Shared by the public-stack scripts; sourced, not run.
# shellcheck shell=sh

production_root=$(cd "$(dirname "$0")/../.." && pwd)
production_env=${PRODUCTION_ENV_FILE:-$production_root/.env.production}
# dc ARGS: docker compose for the public stack (works without the exec bit).
dc() { sh "$production_root/deploy/production/dc" "$@"; }

# env_get NAME: the value of NAME in the env file (last one wins), without
# surrounding quotes. The file is never sourced: its values are not shell
# syntax.
env_get() {
	sed -n "s/^$1=//p" "$production_env" | tail -n 1 | tr -d '\r' |
		sed "s/^'\(.*\)'\$/\1/; s/^\"\(.*\)\"\$/\1/"
}

if [ ! -f "$production_env" ]; then
	echo "missing $production_env: copy deploy/production/env.example and fill it in" >&2
	exit 1
fi
