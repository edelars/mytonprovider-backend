#!/bin/bash

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env}"

if [[ ! -f "$ENV_FILE" ]]; then
    echo "❌ Env file not found: $ENV_FILE"
    echo "Copy .env.example to .env and adjust values if needed."
    exit 1
fi

set -a
source "$ENV_FILE"
set +a

cd "$ROOT_DIR"
go run -tags=debug ./cmd
