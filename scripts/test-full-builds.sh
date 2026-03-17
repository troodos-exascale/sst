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
BUILD_HOST="${2:-ub}" 

UBUNTU_RUNTIMES_DIR="/home/braam/.runtimes"
MAC_RUNTIMES_DIR="${HOME}/.runtimes"

IMAGE_NAME=$(awk -F'[: \t]+' 'tolower($1) == "name" {print $2}' "$DOCKERFILE" | tr -d '"\r\n' | head -n 1 || true)
if [[ -z "$IMAGE_NAME" ]]; then
    IMAGE_NAME=$(basename "$DOCKERFILE" .dockerfile)
fi

INSTANCE_NAME="test-run-${IMAGE_NAME}"
DOCKERFILE_ABS="$(cd "$(dirname "$DOCKERFILE")" && pwd)/$(basename "$DOCKERFILE")"
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
# Phase 1: Build (Delegates to 'ub' using Linux ZFS disk)
# ---------------------------------------------------------
step_enter "build_image"
# We explicitly pass the Ubuntu directory. mkimg automatically delegates this command to 'ub'.
run mkimg build -d "$UBUNTU_RUNTIMES_DIR" "$DOCKERFILE_ABS"
step_ok

# ---------------------------------------------------------
# Phase 2: Instantiate (Delegates to 'ub' using Linux ZFS disk)
# ---------------------------------------------------------
step_enter "instantiate_vm"

# Ensure genisoimage is installed on Ubuntu so cidata generation doesn't fail with exit code 127
logx info "checking for genisoimage on build host..."
runctl ssh "$BUILD_HOST" "if ! command -v genisoimage >/dev/null 2>&1; then sudo apt-get update && sudo apt-get install -y genisoimage; fi" >/dev/null 2>&1 || true

# Extract semantic name. The extra 'tail -n 1' strips runctl's "Connecting to..." banner.
SEMANTIC_NAME=$(runctl ssh "$BUILD_HOST" "ls -1rt '$UBUNTU_RUNTIMES_DIR/images/' | grep alpine" | tail -n 1 | tr -d '\r')
if [[ -z "$SEMANTIC_NAME" ]]; then
    SEMANTIC_NAME="$IMAGE_NAME"
fi
logx info "detected semantic image name" name "$SEMANTIC_NAME"

# Explicitly instruct mkimg to use the Ubuntu ZFS runtime dir for instance creation
run mkimg instance -d "$UBUNTU_RUNTIMES_DIR" "$SEMANTIC_NAME" "$INSTANCE_NAME" --force
step_ok

# ---------------------------------------------------------
# Phase 3: Sync from ZFS (ub) to macOS
# ---------------------------------------------------------
step_enter "sync_artifacts"
logx info "syncing built artifacts from Linux ZFS back to macOS"
SYNC_SCRIPT="
sudo chown -R \$(whoami) '$UBUNTU_RUNTIMES_DIR/images/' '$UBUNTU_RUNTIMES_DIR/instances/'
mkdir -p '$MAC_RUNTIMES_DIR/images/' '$MAC_RUNTIMES_DIR/instances/'
cp -a '$UBUNTU_RUNTIMES_DIR/images/.' '$MAC_RUNTIMES_DIR/images/'
cp -a '$UBUNTU_RUNTIMES_DIR/instances/.' '$MAC_RUNTIMES_DIR/instances/'
"
run runctl ssh "$BUILD_HOST" "$SYNC_SCRIPT"
step_ok

# ---------------------------------------------------------
# Phase 4: Start VM natively on macOS
# ---------------------------------------------------------
step_enter "start_vm"
logx info "starting vm in background" instance "$INSTANCE_NAME"
# runctl defaults to ~/.runtimes natively on macOS
runctl start "$INSTANCE_NAME" &
VM_PID=$!

logx info "waiting for VM to boot and acquire IP..."
run sleep 15
step_ok

# ---------------------------------------------------------
# Phase 5: Verify SSH
# ---------------------------------------------------------
step_enter "verify_ssh"
logx info "executing 'uname -a' inside the VM"

ssh_success=false
for i in {1..3}; do
    if runctl ssh "$INSTANCE_NAME" "uname -a"; then
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
