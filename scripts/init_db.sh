#!/bin/bash

# This script initializes the database with tables, schemas, functions and triggers from db/init.sql

set -e

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
SQL_FILE="$SCRIPT_DIR/../db/init.sql"
TMP_SQL_FILE=""

cleanup() {
    if [[ -n "$TMP_SQL_FILE" && -f "$TMP_SQL_FILE" ]]; then
        rm -f "$TMP_SQL_FILE"
    fi
}

trap cleanup EXIT

if [[ -z "$PG_USER" || -z "$PG_PASSWORD" || -z "$PG_DB" ]]; then
    echo "❌ Missing required environment variables"
    echo ""
    echo "Usage:"
    echo "PG_HOST=<host> PG_PORT=<port> PG_USER=<username> PG_PASSWORD=<password> PG_DB=<database> bash init_db.sh"
    echo "Example:"
    echo "PG_HOST=127.0.0.1 PG_PORT=5432 PG_USER=pguser PG_PASSWORD=secret PG_DB=providerdb bash init_db.sh"
    echo ""
    echo "PG_HOST and PG_PORT are optional"
    exit 1
fi

PG_HOST="${PG_HOST:-127.0.0.1}"
PG_PORT="${PG_PORT:-5432}"

echo "Initializing database from $SQL_FILE..."
TMP_SQL_FILE=$(mktemp)
sed "s/__PG_USER__/$PG_USER/g" "$SQL_FILE" > "$TMP_SQL_FILE"

if PGPASSWORD="$PG_PASSWORD" psql -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$PG_DB" -f "$TMP_SQL_FILE"; then
    echo "✅ Database initialization completed successfully"
else
    echo "❌ Database initialization failed"
    exit 1
fi

echo "Done!"
