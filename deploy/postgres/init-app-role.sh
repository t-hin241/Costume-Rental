#!/bin/sh
# Runs once, when the database volume is created. The application owns its
# database but is not a superuser; btree_gist is a trusted extension, so the
# migrations can still install it.
set -eu

psql -v ON_ERROR_STOP=1 -v app_password="$WEBEE_DB_PASSWORD" --username "$POSTGRES_USER" --dbname postgres <<'SQL'
CREATE ROLE webee LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD :'app_password';
CREATE DATABASE webee_rentor OWNER webee;
SQL
