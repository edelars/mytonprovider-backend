#!/bin/bash

# Installs mytonprovider-backend as a storage proof checking agent on Debian.
# The agent does not install PostgreSQL and talks to the coordinator over HTTP.
# Run this script from an already cloned mytonprovider-backend repository.

set -euo pipefail

GO_VERSION="1.24.5"
DEFAULT_INSTALL_DIR="/opt/mytonprovider-agent"
DEFAULT_SERVICE_USER="mytonprovider-agent"
DEFAULT_AGENT_ID="$(hostname -s 2>/dev/null || echo agent-1)"
DEFAULT_COORDINATOR_URL="https://mytonprovider.org"
DEFAULT_TON_CONFIG_URL="https://ton-blockchain.github.io/global.config.json"
DEFAULT_ADNL_PORT="16167"
DEFAULT_BATCH_SIZE="100"
DEFAULT_POLL_INTERVAL="30"
DEFAULT_LOG_LEVEL="0"
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
    if [[ ! -f "$REPO_DIR/go.mod" || ! -d "$REPO_DIR/cmd" ]]; then
        print_error "Cannot find backend source tree at $REPO_DIR."
        print_error "Run this script from a cloned mytonprovider-backend repository."
        exit 1
    fi
}

collect_answers() {
    echo ""
    print_status "Agent configuration"
    echo "Coordinator must have SYSTEM_ACCESS_TOKENS set to md5(raw agent token)."
    echo "This script asks for the raw token; the agent sends it as Authorization: Bearer <token>."
    echo ""

    ask INSTALL_DIR "Install directory" "$DEFAULT_INSTALL_DIR"
    ask SERVICE_USER "Linux service user" "$DEFAULT_SERVICE_USER"
    ask SERVICE_NAME "Systemd service name" "mytonprovider-agent"

    ask AGENT_ID "Agent ID" "$DEFAULT_AGENT_ID"
    ask AGENT_COORDINATOR_URL "Coordinator base URL" "$DEFAULT_COORDINATOR_URL"
    ask_required AGENT_ACCESS_TOKEN "Raw agent access token" "true"
    ask SYSTEM_ADNL_PORT "Local ADNL port" "$DEFAULT_ADNL_PORT"
    ask TON_CONFIG_URL "TON global config URL" "$DEFAULT_TON_CONFIG_URL"
    ask AGENT_BATCH_SIZE "Contracts per task batch" "$DEFAULT_BATCH_SIZE"
    ask AGENT_POLL_INTERVAL_SECONDS "Poll interval in seconds" "$DEFAULT_POLL_INTERVAL"
    ask SYSTEM_LOG_LEVEL "Log level" "$DEFAULT_LOG_LEVEL"
    ask_yes_no START_SERVICE "Start/restart service after install?" "yes"
}

print_summary() {
    echo ""
    print_status "Installation summary"
    echo "Source dir: $REPO_DIR"
    echo "Install dir: $INSTALL_DIR"
    echo "Service user: $SERVICE_USER"
    echo "Service name: $SERVICE_NAME"
    echo "Agent ID: $AGENT_ID"
    echo "Coordinator: $AGENT_COORDINATOR_URL"
    echo "ADNL port: $SYSTEM_ADNL_PORT"
    echo "Batch size: $AGENT_BATCH_SIZE"
    echo "Poll interval: $AGENT_POLL_INTERVAL_SECONDS"
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
    apt-get install -y ca-certificates curl wget tar build-essential
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
    mkdir -p /var/log/mytonprovider-agent
    chown -R "$SERVICE_USER:$SERVICE_USER" "$INSTALL_DIR" /var/log/mytonprovider-agent
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
    print_status "Writing agent environment file..."

    cat > "$INSTALL_DIR/config.env" <<EOF
APP_ROLE="agent"
SYSTEM_PORT="9090"
SYSTEM_ADNL_PORT=$(env_quote "$SYSTEM_ADNL_PORT")
SYSTEM_ACCESS_TOKENS=""
SYSTEM_LOG_LEVEL=$(env_quote "$SYSTEM_LOG_LEVEL")
SYSTEM_STORE_HISTORY_DAYS="90"
MASTER_ADDRESS="UQB3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d0x0"
TON_CONFIG_URL=$(env_quote "$TON_CONFIG_URL")
BATCH_SIZE="100"
DB_HOST="127.0.0.1"
DB_PORT="5432"
DB_USER=""
DB_PASSWORD=""
DB_NAME=""
AGENT_ID=$(env_quote "$AGENT_ID")
AGENT_COORDINATOR_URL=$(env_quote "$AGENT_COORDINATOR_URL")
AGENT_ACCESS_TOKEN=$(env_quote "$AGENT_ACCESS_TOKEN")
AGENT_BATCH_SIZE=$(env_quote "$AGENT_BATCH_SIZE")
AGENT_POLL_INTERVAL_SECONDS=$(env_quote "$AGENT_POLL_INTERVAL_SECONDS")
EOF

    chown "$SERVICE_USER:$SERVICE_USER" "$INSTALL_DIR/config.env"
    chmod 600 "$INSTALL_DIR/config.env"
}

write_systemd_service() {
    print_status "Writing systemd service..."

    cat > "/etc/systemd/system/${SERVICE_NAME}.service" <<EOF
[Unit]
Description=MyTonProvider Agent
After=network-online.target
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
StandardOutput=append:/var/log/mytonprovider-agent/agent.log
StandardError=append:/var/log/mytonprovider-agent/agent.log
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
    create_service_user
    prepare_directories
    build_backend
    write_config
    write_systemd_service
    start_service

    echo ""
    print_success "Agent installation completed."
    echo "Config: $INSTALL_DIR/config.env"
    echo "Binary: $INSTALL_DIR/mtpo-backend"
    echo "Logs: /var/log/mytonprovider-agent/agent.log"
    echo "Status: systemctl status $SERVICE_NAME"
    echo "Restart: systemctl restart $SERVICE_NAME"
}

main "$@"
