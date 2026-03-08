#!/usr/bin/env bash
# File: images/scripts/env.sh
# Purpose: Central configuration variables for SST image build scripts.
#          Source this file in other scripts.
# Version 3: Uses git for repo root, enables debug tracing (set -x) via DEBUG_SCRIPTS.

# --- Safety & Debugging ---
# Exit immediately if a command exits with a non-zero status.
set -o errexit
# Treat unset variables as an error when substituting.
set -o nounset
# Pipelines return the exit status of the last command to exit non-zero.
set -o pipefail

# --- Debug Control ---
# Set DEBUG_SCRIPTS to 1 or true to enable xtrace (command echoing).
# Set DEBUG_SCRIPTS to 0 or false to disable xtrace for production.
# Defaulting to "1" for the current debugging phase.
export DEBUG_SCRIPTS="${DEBUG_SCRIPTS:-1}"

# Enable xtrace if DEBUG_SCRIPTS is set to 1 or true
# The '+ ' prefix is added to each traced command line.
if [[ "${DEBUG_SCRIPTS}" == "1" || "${DEBUG_SCRIPTS}" == "true" ]]; then
    echo "[env.sh] Debug tracing (xtrace) is ENABLED." >&2
    PS4='+(${BASH_SOURCE}:${LINENO}): ${FUNCNAME[0]:+${FUNCNAME[0]}(): }' # Show file/line/function
    set -o xtrace
fi

# NOTE: errexit (set -e) is already active. During debugging (DEBUG_SCRIPTS=1),
# cleanup traps should generally be avoided to see the exact point of failure.
# Production scripts might add traps conditionally if DEBUG_SCRIPTS is not "1".

# --- Directory Context ---

# Use git to find the repository root
_repo_root_cmd_output=$(git rev-parse --show-toplevel 2>/dev/null)
if [[ $? -ne 0 || -z "$_repo_root_cmd_output" ]]; then
  echo "Error: Could not find Git repository root. Make sure you are inside the repository." >&2
  exit 1 # Exit immediately if git command fails
else
  export REPO_ROOT="$_repo_root_cmd_output"
fi

# Define the location of this script directory relative to the repo root
# Assuming env.sh is always at images/scripts/env.sh
export SCRIPTS_DIR="${REPO_ROOT}/images/scripts"

# Define the Current Working Directory from where the calling script was invoked
export CWD="$(pwd)"

# --- Configuration (from images/Makefile context) ---

# Local Registry Configuration
export REGISTRY_NAME="${REGISTRY_NAME:-sst-local-registry}" # Allow override
export REGISTRY_PORT="${REGISTRY_PORT:-5000}"             # Allow override
export LOCAL_REGISTRY="${LOCAL_REGISTRY:-localhost:${REGISTRY_PORT}}" # Allow override

# Secrets Directory (relative to REPO_ROOT)
export SECRETS_DIR="${SECRETS_DIR:-${REPO_ROOT}/secrets}" # Allow override

# SSH Key Paths (relative to SECRETS_DIR)
export SSH_KEY_PRIVATE_NAME="${SSH_KEY_PRIVATE_NAME:-id_ed25519}"
export SSH_KEY_PUBLIC_NAME="${SSH_KEY_PUBLIC_NAME:-id_ed25519.pub}"
export SSH_KEY_PRIVATE="${SSH_KEY_PRIVATE:-${SECRETS_DIR}/${SSH_KEY_PRIVATE_NAME}}"
export SSH_KEY_PUBLIC="${SSH_KEY_PUBLIC:-${SECRETS_DIR}/${SSH_KEY_PUBLIC_NAME}}"

# Architecture Detection (portable)
_uname_arch="$(uname -m)"
case "$_uname_arch" in
    x86_64)  _default_arch="amd64" ;;
    aarch64) _default_arch="arm64" ;;
    arm64)   _default_arch="arm64" ;;
    *)
        echo "Error: Could not determine architecture from 'uname -m' ($_uname_arch)." >&2
        echo "Please set ARCH environment variable manually (e.g., export ARCH=amd64)." >&2
        exit 1
        ;;
esac
# Allow ARCH override from environment, otherwise use detected default
export ARCH="${ARCH:-$_default_arch}"
# Allow DEBARCH override, otherwise default to ARCH
export DEBARCH="${DEBARCH:-${ARCH}}"

# Builder Detection
_detected_builder=""
_detected_builder_cmd=""
if command -v nerdctl >/dev/null 2>&1; then
    _detected_builder="nerdctl"
    _detected_builder_cmd="nerdctl"
elif command -v docker >/dev/null 2>&1; then
    _detected_builder="docker"
    _detected_builder_cmd="docker"
else
    echo "Error: No container builder (nerdctl or docker) found in PATH." >&2
    exit 1
fi
# Allow BUILDER override, otherwise use detected default
export BUILDER="${BUILDER:-$_detected_builder}"
export BUILDER_CMD="${BUILDER_CMD:-$_detected_builder_cmd}"

# VM Instance Configuration (Defaults from Makefile)
export ID="${ID:-0}"
export BASE_SSH_PORT="${BASE_SSH_PORT:-2222}"
# Calculate SSH_PORT based on BASE_SSH_PORT and ID
export SSH_PORT=$((${BASE_SSH_PORT} + ${ID}))

# Function to get instance name (mirrors makefile logic)
# Usage: local name; name=$(instance_name "base-dev")
instance_name() {
    local base_name="$1"
    # Ensure ID and ARCH are available within the function's scope
    local current_id="${ID:-0}"
    local current_arch="${ARCH:-unknown}"
    if [[ "${current_id}" == "0" ]]; then
        echo "sst-${base_name}-instance-${current_arch}"
    else
        echo "sst-${base_name}-instance-${current_id}-${current_arch}"
    fi
}

# Function to get instance name (mirrors makefile logic)
# Usage: local name; name=$(instance_name "base-dev")
instance_name() {
    local base_name="$1"
    # Ensure ID and ARCH are available within the function's scope
    local current_id="${ID:-0}"
    local current_arch="${ARCH:-unknown}"
    if [[ "${current_id}" == "0" ]]; then
        echo "sst-${base_name}-instance-${current_arch}"
    else
        echo "sst-${base_name}-instance-${current_id}-${current_arch}"
    fi
}

# --- End Configuration ---

# Informational message (only prints if not tracing, otherwise xtrace shows sourcing)
if [[ "${DEBUG_SCRIPTS}" != "1" && "${DEBUG_SCRIPTS}" != "true" ]]; then
    echo "[INFO] Sourced common environment from env.sh" >&2
fi