#!/bin/bash

# Installs mytonprovider-backend as a coordinator on Debian.
# Run this script from an already cloned mytonprovider-backend repository.

set -euo pipefail

GO_VERSION="1.24.5"
DEFAULT_INSTALL_DIR="/opt/mytonprovider-coordinator"
DEFAULT_SERVICE_USER="mytonprovider-coordinator"
DEFAULT_TON_CONFIG_URL="https://ton-blockchain.github.io/global.config.json"
DEFAULT_MASTER_ADDRESS="UQB3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d0x0"
DEFAULT_SYSTEM_PORT="9090"
DEFAULT_ADNL_PORT="16167"
DEFAULT_BATCH_SIZE="100"
DEFAULT_LOG_LEVEL="0"
DEFAULT_STORE_HISTORY_DAYS="90"
DEFAULT_PG_VERSION="15"
GO_BIN=""
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

print_status() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1" >&2
}

ask() {
    local var_name="$1"
    local prompt="$2"
    local default_value="$3"
    local value

    if [[ -n "${!var_name:-}" ]]; then
        return
    fi

    read -r -p "$prompt [$default_value]: " value
    printf -v "$var_name" '%s' "${value:-$default_value}"
}

ask_required() {
    local var_name="$1"
    local prompt="$2"
    local secret="${3:-false}"
    local value

    if [[ -n "${!var_name:-}" ]]; then
        return
    fi

    while true; do
        if [[ "$secret" == "true" ]]; then
            read -r -s -p "$prompt: " value
            echo ""
        else
            read -r -p "$prompt: " value
        fi

        if [[ -n "$value" ]]; then
            printf -v "$var_name" '%s' "$value"
            return
        fi

        print_warning "Value cannot be empty."
    done
}

ask_optional_secret() {
    local var_name="$1"
    local prompt="$2"
    local value

    if [[ -n "${!var_name:-}" ]]; then
        return
    fi

    read -r -s -p "$prompt: " value
    echo ""
    printf -v "$var_name" '%s' "$value"
}

ask_yes_no() {
    local var_name="$1"
    local prompt="$2"
    local default_value="$3"
    local value

    local suffix="y/N"
    if [[ "$default_value" == "yes" ]]; then
        suffix="Y/n"
    fi

    if [[ -n "${!var_name:-}" ]]; then
        return
    fi

    while true; do
        read -r -p "$prompt [$suffix]: " value
        value="${value:-$default_value}"
        case "$value" in
            y|Y|yes|YES|Yes)
                printf -v "$var_name" '%s' "yes"
                return
                ;;
            n|N|no|NO|No)
                printf -v "$var_name" '%s' "no"
                return
                ;;
            *)
                print_warning "Please answer yes or no."
                ;;
        esac
    done
}

env_quote() {
    local value="$1"
    value="${value//\\/\\\\}"
    value="${value//\"/\\\"}"
    printf '"%s"' "$value"
}

require_root() {
    if [[ "${EUID}" -ne 0 ]]; then
        print_error "This script must be run as root. Use: sudo $0"
        exit 1
    fi
}

check_debian() {
    if [[ -f /etc/os-release ]]; then
        # shellcheck disable=SC1091
        . /etc/os-release
        if [[ "${ID:-}" != "debian" && "${ID_LIKE:-}" != *"debian"* ]]; then
            print_warning "This script is intended for Debian. Detected: ${PRETTY_NAME:-unknown}."
            ask_yes_no CONTINUE_UNSUPPORTED "Continue anyway?" "no"
            if [[ "$CONTINUE_UNSUPPORTED" != "yes" ]]; then
                exit 1
            fi
        fi
    fi
}

check_source_tree() {
    if [[ ! -f "$REPO_DIR/go.mod" || ! -d "$REPO_DIR/cmd" || ! -f "$REPO_DIR/db/init.sql" ]]; then
        print_error "Cannot find backend source tree at $REPO_DIR."
        print_error "Run this script from a cloned mytonprovider-backend repository."
        exit 1
    fi
}

collect_answers() {
    echo ""
    print_status "Coordinator configuration"
    echo "SYSTEM_ACCESS_TOKENS must contain comma-separated md5 hashes of raw agent tokens."
    echo "Agents send raw tokens as Authorization: Bearer <token>."
    echo ""

    ask INSTALL_DIR "Install directory" "$DEFAULT_INSTALL_DIR"
    ask SERVICE_USER "Linux service user" "$DEFAULT_SERVICE_USER"
    ask SERVICE_NAME "Systemd service name" "mytonprovider-coordinator"

    ask SYSTEM_PORT "Public HTTP port" "$DEFAULT_SYSTEM_PORT"
    ask SYSTEM_ADNL_PORT "Local ADNL port" "$DEFAULT_ADNL_PORT"
    ask_optional_secret SYSTEM_ACCESS_TOKENS "Allowed agent token md5 hashes, comma-separated; leave empty to disable authorized agent/API access"
    ask SYSTEM_LOG_LEVEL "Log level" "$DEFAULT_LOG_LEVEL"
    ask SYSTEM_STORE_HISTORY_DAYS "History retention days" "$DEFAULT_STORE_HISTORY_DAYS"

    ask MASTER_ADDRESS "Master provider address" "$DEFAULT_MASTER_ADDRESS"
    ask TON_CONFIG_URL "TON global config URL" "$DEFAULT_TON_CONFIG_URL"
    ask BATCH_SIZE "TON batch size" "$DEFAULT_BATCH_SIZE"

    ask_yes_no INSTALL_POSTGRES "Install/configure local PostgreSQL?" "yes"
    if [[ "$INSTALL_POSTGRES" == "yes" ]]; then
        ask PG_VERSION "PostgreSQL version" "$DEFAULT_PG_VERSION"
        ask DB_HOST "PostgreSQL host" "127.0.0.1"
        ask DB_PORT "PostgreSQL port" "5432"
    else
        ask DB_HOST "PostgreSQL host" "127.0.0.1"
        ask DB_PORT "PostgreSQL port" "5432"
    fi
    ask_required DB_USER "PostgreSQL user"
    ask_required DB_PASSWORD "PostgreSQL password" "true"
    ask_required DB_NAME "PostgreSQL database"
    ask_yes_no INIT_DB "Initialize database schema from db/init.sql?" "yes"
    ask_yes_no START_SERVICE "Start/restart service after install?" "yes"
}

print_summary() {
    echo ""
    print_status "Installation summary"
    echo "Source dir: $REPO_DIR"
    echo "Install dir: $INSTALL_DIR"
    echo "Service user: $SERVICE_USER"
    echo "Service name: $SERVICE_NAME"
    echo "HTTP port: $SYSTEM_PORT"
    echo "ADNL port: $SYSTEM_ADNL_PORT"
    echo "PostgreSQL: $DB_HOST:$DB_PORT/$DB_NAME as $DB_USER"
    echo "Install local PostgreSQL: $INSTALL_POSTGRES"
    echo "Initialize DB schema: $INIT_DB"
    echo ""
    ask_yes_no CONFIRM_INSTALL "Proceed with installation?" "yes"
    if [[ "$CONFIRM_INSTALL" != "yes" ]]; then
        print_warning "Installation cancelled."
        exit 0
    fi
}

install_dependencies() {
    print_status "Installing system dependencies..."
    apt-get update
    apt-get install -y ca-certificates curl wget gnupg lsb-release tar build-essential postgresql-client
}

install_go() {
    if command -v go >/dev/null 2>&1; then
        GO_BIN="$(command -v go)"
        print_status "Go is already installed: $(go version)"
        return
    fi

    local arch
    case "$(uname -m)" in
        x86_64|amd64)
            arch="amd64"
            ;;
        aarch64|arm64)
            arch="arm64"
            ;;
        *)
            print_error "Unsupported CPU architecture: $(uname -m)"
            exit 1
            ;;
    esac

    local archive="go${GO_VERSION}.linux-${arch}.tar.gz"
    local url="https://go.dev/dl/${archive}"

    print_status "Installing Go ${GO_VERSION} for ${arch}..."
    wget -q "$url" -O "/tmp/${archive}"
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "/tmp/${archive}"
    rm -f "/tmp/${archive}"
    ln -sf /usr/local/go/bin/go /usr/local/bin/go
    ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
    GO_BIN="/usr/local/go/bin/go"
}

setup_postgres() {
    if [[ "$INSTALL_POSTGRES" != "yes" ]]; then
        print_status "Skipping local PostgreSQL installation."
        return
    fi

    print_status "Installing and configuring local PostgreSQL..."
    PG_USER="$DB_USER" PG_PASSWORD="$DB_PASSWORD" PG_DB="$DB_NAME" PG_VERSION="$PG_VERSION" bash "$SCRIPT_DIR/psql_setup.sh"
}

init_database() {
    if [[ "$INIT_DB" != "yes" ]]; then
        print_status "Skipping DB schema initialization."
        return
    fi

    print_status "Initializing database schema..."
    PG_HOST="$DB_HOST" PG_PORT="$DB_PORT" PG_USER="$DB_USER" PG_PASSWORD="$DB_PASSWORD" PG_DB="$DB_NAME" bash "$SCRIPT_DIR/init_db.sh"
}

create_service_user() {
    if id "$SERVICE_USER" >/dev/null 2>&1; then
        print_status "Service user already exists: $SERVICE_USER"
        return
    fi

    print_status "Creating service user: $SERVICE_USER"
    useradd --system --home-dir "$INSTALL_DIR" --shell /usr/sbin/nologin "$SERVICE_USER"
}

prepare_directories() {
    print_status "Preparing directories..."
    mkdir -p "$INSTALL_DIR"
    mkdir -p /var/log/mytonprovider-coordinator
    chown -R "$SERVICE_USER:$SERVICE_USER" "$INSTALL_DIR" /var/log/mytonprovider-coordinator
    chmod 750 "$INSTALL_DIR"
}

build_backend() {
    local build_dir
    build_dir="$(mktemp -d)"

    print_status "Building backend binary..."
    "$GO_BIN" build -buildvcs=false -o "$build_dir/mtpo-backend" "$REPO_DIR/cmd"

    install -m 0755 "$build_dir/mtpo-backend" "$INSTALL_DIR/mtpo-backend"
    rm -rf "$build_dir"
}

write_config() {
    print_status "Writing coordinator environment file..."

    cat > "$INSTALL_DIR/config.env" <<EOF
APP_ROLE="coordinator"
SYSTEM_PORT=$(env_quote "$SYSTEM_PORT")
SYSTEM_ADNL_PORT=$(env_quote "$SYSTEM_ADNL_PORT")
SYSTEM_ACCESS_TOKENS=$(env_quote "$SYSTEM_ACCESS_TOKENS")
SYSTEM_LOG_LEVEL=$(env_quote "$SYSTEM_LOG_LEVEL")
SYSTEM_STORE_HISTORY_DAYS=$(env_quote "$SYSTEM_STORE_HISTORY_DAYS")
MASTER_ADDRESS=$(env_quote "$MASTER_ADDRESS")
TON_CONFIG_URL=$(env_quote "$TON_CONFIG_URL")
BATCH_SIZE=$(env_quote "$BATCH_SIZE")
DB_HOST=$(env_quote "$DB_HOST")
DB_PORT=$(env_quote "$DB_PORT")
DB_USER=$(env_quote "$DB_USER")
DB_PASSWORD=$(env_quote "$DB_PASSWORD")
DB_NAME=$(env_quote "$DB_NAME")
AGENT_ID=""
AGENT_COORDINATOR_URL=""
AGENT_ACCESS_TOKEN=""
AGENT_BATCH_SIZE="100"
AGENT_POLL_INTERVAL_SECONDS="30"
EOF

    chown "$SERVICE_USER:$SERVICE_USER" "$INSTALL_DIR/config.env"
    chmod 600 "$INSTALL_DIR/config.env"
}

write_systemd_service() {
    print_status "Writing systemd service..."

    cat > "/etc/systemd/system/${SERVICE_NAME}.service" <<EOF
[Unit]
Description=MyTonProvider Coordinator
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=simple
User=${SERVICE_USER}
Group=${SERVICE_USER}
WorkingDirectory=${INSTALL_DIR}
EnvironmentFile=${INSTALL_DIR}/config.env
ExecStart=${INSTALL_DIR}/mtpo-backend
Restart=always
RestartSec=10
StandardOutput=append:/var/log/mytonprovider-coordinator/coordinator.log
StandardError=append:/var/log/mytonprovider-coordinator/coordinator.log
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable "$SERVICE_NAME"
}

start_service() {
    if [[ "$START_SERVICE" != "yes" ]]; then
        print_warning "Service was installed but not started. Start it with: systemctl start $SERVICE_NAME"
        return
    fi

    print_status "Starting service..."
    systemctl restart "$SERVICE_NAME"
    sleep 2
    systemctl --no-pager --full status "$SERVICE_NAME" || true
}

main() {
    require_root
    check_debian
    check_source_tree
    collect_answers
    print_summary
    install_dependencies
    install_go
    setup_postgres
    init_database
    create_service_user
    prepare_directories
    build_backend
    write_config
    write_systemd_service
    start_service

    echo ""
    print_success "Coordinator installation completed."
    echo "Config: $INSTALL_DIR/config.env"
    echo "Binary: $INSTALL_DIR/mtpo-backend"
    echo "Logs: /var/log/mytonprovider-coordinator/coordinator.log"
    echo "Status: systemctl status $SERVICE_NAME"
    echo "Restart: systemctl restart $SERVICE_NAME"
}

main "$@"
