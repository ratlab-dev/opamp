#!/usr/bin/env bash
# ==============================================================================
# run-supervisor.sh - OpAMP OpenTelemetry Collector Supervisor Runner
# ==============================================================================
# Easily launch the OpAMP Supervisor Agent to manage and hot-reload an
# OpenTelemetry Collector binary with centralized configuration overlays.
# ==============================================================================

set -eo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${SCRIPT_DIR}"

# ------------------------------------------------------------------------------
# Default Settings
# ------------------------------------------------------------------------------
DEFAULT_SERVER_URL="ws://127.0.0.1:4320/v1/opamp"
DEFAULT_ROLE="mac-dev"
DEFAULT_ENV="dev"
DEFAULT_CLUSTER="local"
DEFAULT_SUBDOMAIN=""
DEFAULT_CONFIG_PATH="${HOME}/.opamp-agent/config.yaml"
DEFAULT_SUPERVISOR_BIN="${SCRIPT_DIR}/bin/opamp-agent"

SERVER_URL="${OPAMP_SERVER_URL:-${DEFAULT_SERVER_URL}}"
ROLE="${ROLE:-${DEFAULT_ROLE}}"
ENV="${ENV:-${DEFAULT_ENV}}"
CLUSTER="${CLUSTER:-${DEFAULT_CLUSTER}}"
SUBDOMAIN="${SUBDOMAIN:-${DEFAULT_SUBDOMAIN}}"
CONFIG_PATH="${CONFIG_PATH:-${DEFAULT_CONFIG_PATH}}"
SUPERVISOR_BIN="${SUPERVISOR_BIN:-${DEFAULT_SUPERVISOR_BIN}}"
OTEL_BIN="${OTEL_BIN:-}"
DOWNLOAD_COLLECTOR=false

# ------------------------------------------------------------------------------
# Colors for Terminal Output
# ------------------------------------------------------------------------------
BOLD="\033[1m"
GREEN="\033[0;32m"
YELLOW="\033[0;33m"
CYAN="\033[0;36m"
RED="\033[0;31m"
RESET="\033[0m"

# ------------------------------------------------------------------------------
# Usage / Help
# ------------------------------------------------------------------------------
print_usage() {
    echo -e "${BOLD}Usage:${RESET}"
    echo -e "  $0 [options] [collector-binary-path]"
    echo -e ""
    echo -e "${BOLD}Quick Start:${RESET}"
    echo -e "  # Run supervisor giving OTEL collector binary as positional argument:"
    echo -e "  $0 /usr/local/bin/otelcol-contrib"
    echo -e ""
    echo -e "  # Or using -b / --bin:"
    echo -e "  $0 -b ./bin/collector/otelcol-contrib -r mac-dev"
    echo -e ""
    echo -e "  # Run with automatic collector download (Darwin/Linux arm64/amd64):"
    echo -e "  $0 --download"
    echo -e ""
    echo -e "  # Run in simulation mode (config applied to disk without running otelcol):"
    echo -e "  $0"
    echo -e ""
    echo -e "${BOLD}Options:${RESET}"
    echo -e "  -b, --bin, --collector <path>   Path to OpenTelemetry Collector binary (or env: OTEL_BIN)"
    echo -e "  -s, --server <url>              OpAMP WebSocket server URL (default: ${DEFAULT_SERVER_URL})"
    echo -e "  -r, --role <role>               Topology role overlay (default: ${DEFAULT_ROLE})"
    echo -e "                                  Available in examples: mac-dev, mac-work, node-exporter, database, clickhouse"
    echo -e "  -e, --env <env>                 Deployment environment (default: ${DEFAULT_ENV})"
    echo -e "  -c, --cluster <name>            Cluster identifier (default: ${DEFAULT_CLUSTER})"
    echo -e "  -d, --subdomain <name>          Subdomain scope (optional)"
    echo -e "  -n, --hostname <name>           Host identifier (default: system hostname)"
    echo -e "  -p, --config <path>             Path to write managed config (default: ~/.opamp-agent/config.yaml)"
    echo -e "      --supervisor-bin <path>     Path to supervisor agent binary (default: ./bin/opamp-agent)"
    echo -e "      --download                  Download latest otelcol-contrib for current platform into ./bin/collector/"
    echo -e "  -h, --help                      Show this help message"
    echo -e ""
    echo -e "${BOLD}Environment Variables:${RESET}"
    echo -e "  OTEL_BIN            OpenTelemetry collector binary path"
    echo -e "  OPAMP_SERVER_URL    OpAMP server WebSocket URL"
    echo -e "  ROLE                Agent role overlay name"
    echo -e "  ENV                 Deployment environment name"
    echo -e "  CLUSTER             Cluster name"
    echo -e "  CONFIG_PATH         Destination path for rendered YAML configuration"
    echo -e "  SUPERVISOR_BIN      Custom path to supervisor agent binary"
    echo ""
}

# ------------------------------------------------------------------------------
# Helper: Download OTEL Collector Contrib
# ------------------------------------------------------------------------------
download_otel_collector() {
    echo -e "${CYAN}==> Detecting platform for OpenTelemetry Collector download...${RESET}"
    local os_type arch_type
    os_type="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch_type="$(uname -m)"

    case "${arch_type}" in
        x86_64|amd64) arch_type="amd64" ;;
        arm64|aarch64) arch_type="arm64" ;;
        *)
            echo -e "${RED}Unsupported architecture: ${arch_type}${RESET}" >&2
            exit 1
            ;;
    esac

    local target_dir="${SCRIPT_DIR}/bin/collector"
    mkdir -p "${target_dir}"

    echo -e "${CYAN}==> Fetching latest otelcol-contrib release tag from GitHub...${RESET}"
    local latest_tag
    latest_tag="$(curl -sSL -H "Accept: application/vnd.github.v3+json" https://api.github.com/repos/open-telemetry/opentelemetry-collector-releases/releases/latest | grep '"tag_name":' | head -n 1 | sed -E 's/.*"([^"]+)".*/\1/')"

    if [[ -z "${latest_tag}" ]]; then
        latest_tag="v0.111.0"
        echo -e "${YELLOW}Could not resolve latest tag from GitHub API, falling back to ${latest_tag}${RESET}"
    fi

    local version="${latest_tag#v}"
    local archive_name="otelcol-contrib_${version}_${os_type}_${arch_type}.tar.gz"
    local download_url="https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/${latest_tag}/${archive_name}"

    echo -e "${GREEN}==> Downloading ${archive_name} from GitHub...${RESET}"
    echo -e "    ${download_url}"
    local tmp_dir
    tmp_dir="$(mktemp -d)"
    trap 'rm -rf "${tmp_dir}"' EXIT

    curl -sSL -o "${tmp_dir}/${archive_name}" "${download_url}"
    tar -xzf "${tmp_dir}/${archive_name}" -C "${tmp_dir}"

    if [[ -f "${tmp_dir}/otelcol-contrib" ]]; then
        mv "${tmp_dir}/otelcol-contrib" "${target_dir}/otelcol-contrib"
        chmod +x "${target_dir}/otelcol-contrib"
        OTEL_BIN="${target_dir}/otelcol-contrib"
        echo -e "${GREEN}==> Successfully installed OpenTelemetry Collector at: ${OTEL_BIN}${RESET}"
        "${OTEL_BIN}" --version || true
    else
        echo -e "${RED}Failed to locate otelcol-contrib binary in extracted archive.${RESET}" >&2
        exit 1
    fi
}

# ------------------------------------------------------------------------------
# Argument Parsing
# ------------------------------------------------------------------------------
HOSTNAME_ARG=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help)
            print_usage
            exit 0
            ;;
        --download)
            DOWNLOAD_COLLECTOR=true
            shift
            ;;
        -b|--bin|--collector)
            OTEL_BIN="$2"
            shift 2
            ;;
        -s|--server)
            SERVER_URL="$2"
            shift 2
            ;;
        -r|--role)
            ROLE="$2"
            shift 2
            ;;
        -e|--env)
            ENV="$2"
            shift 2
            ;;
        -c|--cluster)
            CLUSTER="$2"
            shift 2
            ;;
        -d|--subdomain)
            SUBDOMAIN="$2"
            shift 2
            ;;
        -n|--hostname)
            HOSTNAME_ARG="$2"
            shift 2
            ;;
        -p|--config)
            CONFIG_PATH="$2"
            shift 2
            ;;
        --supervisor-bin)
            SUPERVISOR_BIN="$2"
            shift 2
            ;;
        -*)
            echo -e "${RED}Error: Unknown option '$1'${RESET}" >&2
            print_usage
            exit 1
            ;;
        *)
            # Positional argument: OTEL binary path
            if [[ -z "${OTEL_BIN}" ]]; then
                OTEL_BIN="$1"
            else
                echo -e "${YELLOW}Warning: Unrecognized extra argument '$1', ignoring.${RESET}" >&2
            fi
            shift
            ;;
    esac
done

# ------------------------------------------------------------------------------
# Handle --download request
# ------------------------------------------------------------------------------
if [[ "${DOWNLOAD_COLLECTOR}" == true ]]; then
    download_otel_collector
fi

# ------------------------------------------------------------------------------
# Discover Collector Binary if not explicitly passed
# ------------------------------------------------------------------------------
if [[ -z "${OTEL_BIN}" ]]; then
    # Look in common local and system directories
    SEARCH_CANDIDATES=(
        "${SCRIPT_DIR}/bin/collector/otelcol-contrib"
        "${SCRIPT_DIR}/bin/collector/otelcol"
        "${SCRIPT_DIR}/bin/otelcol-contrib"
        "/usr/local/bin/otelcol-contrib"
        "/opt/homebrew/bin/otelcol-contrib"
        "/usr/bin/otelcol-contrib"
    )

    for candidate in "${SEARCH_CANDIDATES[@]}"; do
        if [[ -x "${candidate}" ]]; then
            OTEL_BIN="${candidate}"
            break
        fi
    done

    # Check on PATH if still not found
    if [[ -z "${OTEL_BIN}" ]]; then
        if command -v otelcol-contrib >/dev/null 2>&1; then
            OTEL_BIN="$(command -v otelcol-contrib)"
        elif command -v otelcol >/dev/null 2>&1; then
            OTEL_BIN="$(command -v otelcol)"
        fi
    fi
fi

# ------------------------------------------------------------------------------
# Validate Collector Binary (if specified or discovered)
# ------------------------------------------------------------------------------
if [[ -n "${OTEL_BIN}" ]]; then
    if [[ ! -f "${OTEL_BIN}" ]]; then
        echo -e "${RED}Error: Specified OpenTelemetry Collector binary does not exist:${RESET} ${OTEL_BIN}" >&2
        exit 1
    fi

    if [[ ! -x "${OTEL_BIN}" ]]; then
        echo -e "${YELLOW}Notice: OpenTelemetry Collector binary is not executable. Adding +x permission...${RESET}"
        chmod +x "${OTEL_BIN}"
    fi

    # Resolve absolute path for reliability
    OTEL_BIN="$(cd "$(dirname "${OTEL_BIN}")" && pwd)/$(basename "${OTEL_BIN}")"
fi

# ------------------------------------------------------------------------------
# Ensure Supervisor Agent Binary is Built
# ------------------------------------------------------------------------------
if [[ ! -f "${SUPERVISOR_BIN}" ]]; then
    echo -e "${CYAN}==> Supervisor binary not found at ${SUPERVISOR_BIN}.${RESET}"
    echo -e "${CYAN}==> Compiling supervisor agent (cmd/agent)...${RESET}"
    mkdir -p "$(dirname "${SUPERVISOR_BIN}")"
    go build -o "${SUPERVISOR_BIN}" ./cmd/agent
    echo -e "${GREEN}==> Supervisor built successfully.${RESET}"
fi

# ------------------------------------------------------------------------------
# Pre-Flight Status Output
# ------------------------------------------------------------------------------
echo -e "${BOLD}${GREEN}==========================================================${RESET}"
echo -e "${BOLD}${GREEN} Starting OpAMP Supervisor for OpenTelemetry Collector    ${RESET}"
echo -e "${BOLD}${GREEN}==========================================================${RESET}"
echo -e "${CYAN}OpAMP Server:${RESET}     ${SERVER_URL}"
echo -e "${CYAN}Role Overlay:${RESET}     ${ROLE}"
echo -e "${CYAN}Environment:${RESET}      ${ENV}"
echo -e "${CYAN}Cluster:${RESET}          ${CLUSTER}"
if [[ -n "${SUBDOMAIN}" ]]; then
    echo -e "${CYAN}Subdomain:${RESET}        ${SUBDOMAIN}"
fi
echo -e "${CYAN}Target Config:${RESET}    ${CONFIG_PATH}"
echo -e "${CYAN}Supervisor Bin:${RESET}   ${SUPERVISOR_BIN}"

if [[ -n "${OTEL_BIN}" ]]; then
    echo -e "${CYAN}Collector Bin:${RESET}    ${BOLD}${GREEN}${OTEL_BIN}${RESET}"
else
    echo -e "${CYAN}Collector Bin:${RESET}    ${YELLOW}None (Running in simulation mode)${RESET}"
    echo -e "${YELLOW}Tip: Pass your collector binary via:${RESET}"
    echo -e "     $0 /path/to/otelcol-contrib"
    echo -e "  or run with: $0 --download"
fi
echo -e "${BOLD}${GREEN}==========================================================${RESET}"

# ------------------------------------------------------------------------------
# Build Supervisor Command Arguments
# ------------------------------------------------------------------------------
CMD_ARGS=(
    "-server-url" "${SERVER_URL}"
    "-role" "${ROLE}"
    "-env" "${ENV}"
    "-cluster" "${CLUSTER}"
    "-config-path" "${CONFIG_PATH}"
)

if [[ -n "${SUBDOMAIN}" ]]; then
    CMD_ARGS+=("-subdomain" "${SUBDOMAIN}")
fi

if [[ -n "${HOSTNAME_ARG}" ]]; then
    CMD_ARGS+=("-hostname" "${HOSTNAME_ARG}")
fi

if [[ -n "${OTEL_BIN}" ]]; then
    CMD_ARGS+=("-otel-bin" "${OTEL_BIN}")
fi

# ------------------------------------------------------------------------------
# Execute Supervisor
# ------------------------------------------------------------------------------
exec "${SUPERVISOR_BIN}" "${CMD_ARGS[@]}"
