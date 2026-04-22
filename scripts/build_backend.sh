#!/bin/bash

# This script builds the backend application for the TON provider.
# Also generates the .env file with necessary configurations.

cd "$WORK_DIR/mytonprovider-backend/"

go build -buildvcs=false -o mtpo-backend ./cmd

cat <<EOL > config.env
SYSTEM_PORT=9090
SYSTEM_ADNL_PORT=16167
MASTER_ADDRESS=UQB3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d0x0
TON_CONFIG_URL=https://ton-blockchain.github.io/global.config.json
SYSTEM_ACCESS_TOKENS=
BATCH_SIZE=100
DB_HOST=127.0.0.1
DB_PORT=5432
DB_USER=${PG_USER}
DB_PASSWORD=${PG_PASSWORD}
DB_NAME=${PG_DB}
SYSTEM_LOG_LEVEL=0
EOL

mv mtpo-backend /opt/provider/
mv config.env /opt/provider/

echo "Backend application built and configuration file created successfully."
