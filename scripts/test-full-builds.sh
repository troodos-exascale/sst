#!/usr/bin/env bash
set -euo pipefail

# Setup absolute paths
SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)
REPO_ROOT=$(cd -- "${SCRIPT_DIR}/../" &>/dev/null && pwd)

# Load the SST Shell Framework
source "${REPO_ROOT}/scripts/framework.sh"

if [[ $# -lt 1 ]]; then
    die "Usage: $0 <dockerfile> [build_host]"
fi

DOCKERFILE="$1"
# Default to 'ub' if you don't provide a second argument
BUILD_HOST="${2:-ub}" 

UBUNTU_RUNTIMES_DIR="/home/braam/.runtimes"
MAC_RUNTIMES_DIR="${HOME}/.runtimes"

# FIX 1: Tell awk to treat both spaces AND colons as delimiters so "NAME: alpine-vm" parses perfectly
IMAGE_NAME=$(awk -F'[: \t]+' 'tolower($1) == "name" {print $2}' "$DOCKERFILE" | tr -d '"\r\n' | head -n 1 || true)

if [[ -z "$IMAGE_NAME" ]]; then
    IMAGE_NAME=$(basename "$DOCKERFILE" .dockerfile)
    logx warn "NAME directive not found or unparseable, falling back to filename" fallback "$IMAGE_NAME"
fi

INSTANCE_NAME="test-run-${IMAGE_NAME}"

# Ensure absolute path for the dockerfile
DOCKERFILE_ABS="$(cd "$(dirname "$DOCKERFILE")" && pwd)/$(basename "$DOCKERFILE")"
WORK_DIR="$PWD"
VM_PID=""

# --- Teardown Hook ---
cleanup() {
    local rc=$?
    if [[ -n "$VM_PID" ]]; then
        logx info "teardown.start" instance "$INSTANCE_NAME"
        runctl stop "$INSTANCE_NAME" >/dev/null 2>&1 || true
        wait $VM_PID 2>/dev/null || true
        logx info "teardown.ok"
    fi
    if [[ $rc -ne 0 ]]; then
        logx error "roundtrip.failed" exit_code "$rc"
    fi
    exit $rc
}
trap cleanup EXIT

logx info "roundtrip.start" image "$IMAGE_NAME" instance "$INSTANCE_NAME" host "$BUILD_HOST"

# ---------------------------------------------------------
# Phase 1: Build on Linux (via runctl ssh)
# ---------------------------------------------------------
step_enter "build_on_linux"

# FIX 2: We evaluate SST_LOG_FORMAT on the Mac, and hardcode it inline with sudo 
# so neither SSH nor sudo can strip it out!
REMOTE_SCRIPT="
set -euo pipefail
cd '$WORK_DIR'
echo '--> Running mkimg build on linux...'

sudo SST_LOG_FORMAT='${SST_LOG_FORMAT:-text}' mkimg build -d '$UBUNTU_RUNTIMES_DIR' '$DOCKERFILE_ABS'

echo '--> Fixing ownership & transferring to Mac filesystem...'
sudo chown -R \$(whoami) '$UBUNTU_RUNTIMES_DIR'

rm -rf '$MAC_RUNTIMES_DIR/images/$IMAGE_NAME'
mv '$UBUNTU_RUNTIMES_DIR/images/$IMAGE_NAME' '$MAC_RUNTIMES_DIR/images/'
"

run runctl ssh "$BUILD_HOST" "$REMOTE_SCRIPT"
step_ok

# ---------------------------------------------------------
# Phase 2: Instantiate on macOS
# ---------------------------------------------------------
step_enter "instantiate_vm"
run mkimg instance "$IMAGE_NAME" "$INSTANCE_NAME" --force
step_ok

# ---------------------------------------------------------
# Phase 3: Start VM
# ---------------------------------------------------------
step_enter "start_vm"
logx info "starting vm in background" instance "$INSTANCE_NAME"
runctl start "$INSTANCE_NAME" &
VM_PID=$!

logx info "waiting for VM to boot..."
run sleep 10
step_ok

# ---------------------------------------------------------
# Phase 4: Verify via SSH
# ---------------------------------------------------------
step_enter "verify_ssh"
logx info "executing 'uname -a' inside the VM"
run runctl ssh "$INSTANCE_NAME" "uname -a"
step_ok

logx info "roundtrip.success"
