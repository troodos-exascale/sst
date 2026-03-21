#!/usr/bin/env bash
set -euo pipefail

# Setup absolute paths
HOST_SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)
HOST_REPO_ROOT=$(cd -- "${HOST_SCRIPT_DIR}/../" &>/dev/null && pwd)

# Load the SST Shell Framework
source "${HOST_REPO_ROOT}/scripts/framework.sh"

if [[ $# -lt 1 ]]; then
    die "Usage: $0 <dockerfile> [build_host] [context_dir]"
fi

HOST_DOCKERFILE="$1"
VM_BUILD_HOST="${2:-ub}"
HOST_CONTEXT_DIR="${3:-.}"

# Resolve absolute path for the host side
HOST_CONTEXT_ABS="$(cd "$HOST_CONTEXT_DIR" && pwd)"

# Define VM-side mapped directories
VM_BUILD_DIR="/runtimes"
VM_WORKSPACE_DIR="/workspace"

IMAGE_NAME=$(awk -F'[: \t]+' 'tolower($1) == "name" {print $2}' "$HOST_DOCKERFILE" | tr -d '"\r\n' | head -n 1 || true)
if [[ -z "$IMAGE_NAME" ]]; then
    IMAGE_NAME=$(basename "$HOST_DOCKERFILE" .dockerfile)
fi

INSTANCE_NAME="test-run-${IMAGE_NAME}"
HOST_VM_PID=""

# --- Teardown Hook ---
cleanup() {
    local rc=$?
    if [[ -n "$HOST_VM_PID" ]]; then
        logx info "teardown.start" instance "$INSTANCE_NAME"
        # Execute from context dir to ensure local resolution
        (cd "$HOST_CONTEXT_ABS" && runctl stop "$INSTANCE_NAME" >/dev/null 2>&1 || true)
        wait $HOST_VM_PID 2>/dev/null || true
        logx info "teardown.ok"
    fi
    if [[ $rc -ne 0 ]]; then
        logx error "roundtrip.failed" exit_code "$rc"
    fi
    exit $rc
}
trap cleanup EXIT

logx info "roundtrip.start" image "$IMAGE_NAME" instance "$INSTANCE_NAME" host "$VM_BUILD_HOST"

# ---------------------------------------------------------
# Phase 1: Build (Delegates to VM using ZFS disk)
# ---------------------------------------------------------
step_enter "build_image"
# -b instructs the VM to use the fast ZFS pool for intermediate extraction
# -d instructs the VM to emit final artifacts to virtiofs (mapped to HOST_CONTEXT_ABS)
run mkimg build -b "$VM_BUILD_DIR" -d "$VM_WORKSPACE_DIR" -f "$HOST_DOCKERFILE" "$HOST_CONTEXT_DIR" 
step_ok

# ---------------------------------------------------------
# Phase 2: Instantiate
# ---------------------------------------------------------
step_enter "instantiate_vm"

# NEW: Read the authoritative identity directly from the stamped blueprint
CANONICAL_IMAGE_NAME=$(awk '/^# CANONICAL_NAME:/ {print $3}' "$HOST_DOCKERFILE" | tr -d '\r')

if [[ -z "$CANONICAL_IMAGE_NAME" ]]; then
    die "Failed to read # CANONICAL_NAME from $HOST_DOCKERFILE. Did the build stamp it?"
fi

INSTANCE_NAME="test-run-${CANONICAL_IMAGE_NAME}"
logx info "resolved canonical image from blueprint" image "$CANONICAL_IMAGE_NAME" instance "$INSTANCE_NAME"

run mkimg instance -b "$VM_BUILD_DIR" -d "$VM_WORKSPACE_DIR" "$CANONICAL_IMAGE_NAME" "$INSTANCE_NAME" --force
step_ok

# ---------------------------------------------------------
# Phase 3: Start VM natively on macOS
# ---------------------------------------------------------
step_enter "start_vm"
logx info "starting vm natively on host" instance "$INSTANCE_NAME"

# Execute in context dir so runctl resolves it locally, but keep it in the main shell
pushd "$HOST_CONTEXT_ABS" >/dev/null
run runctl start "$INSTANCE_NAME" &
HOST_VM_PID=$!
popd >/dev/null

logx info "waiting for VM to boot and acquire IP..."
run sleep 5
step_ok

# ---------------------------------------------------------
# Phase 4: Verify SSH
# ---------------------------------------------------------
step_enter "verify_ssh"
logx info "executing 'uname -a' inside the VM"

ssh_success=false
for i in {1..3}; do
    # Execute from context dir to ensure local resolution
    if (cd "$HOST_CONTEXT_ABS" && runctl ssh "$INSTANCE_NAME" "sync && uname -a"); then
        ssh_success=true
        break
    fi
    logx warn "SSH failed, retrying in 5 seconds..."
    sleep 5
done

if [ "$ssh_success" = false ]; then
    die "Failed to connect to the VM via SSH after 3 attempts."
fi
step_ok

logx info "roundtrip.success"
