#!/usr/bin/env bash
# Copyright 2026 Peter J. Braam
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


# This script must be run with sudo because it interacts with system storage tools.
if [ "$(id -u)" -ne 0 ]; then
  echo "This script must be run with sudo: sudo $0" >&2
  exit 1
fi

# Use the existing test framework
SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)
REPO_ROOT=$(cd -- "${SCRIPT_DIR}/../../../../../../" &>/dev/null && pwd)

source "${REPO_ROOT}/scripts/lib/test-debug-framework.sh"

# --- Configuration ---
SST_BIN="${REPO_ROOT}/go/cmd/sst/build/linux_amd64/bin/sst"
TEST_DIR=""
HARDWARE_YAML=""
SECRETS_DIR=""
DISK1_IMG=""
DISK2_IMG=""
DISK1_LOOP=""
DISK2_LOOP=""

# --- Script-specific help ---
usage() {
    cat <<EOF
Usage: $(basename "$0") [-c] [-d] [-C] [-p] [-s <step>] [-u <step>] [-f <step>] [-h]
A comprehensive integration test for the 'sst' storage command suite.
(See framework for all options)
EOF
}

# --- Step Definitions ---
STEPS=(
    step_0_build_sst
    step_1_setup_environment
    step_2_zfs_workflow
    step_3_ext4_luks_workflow
)

cleanup() {
    info "--- Running Cleanup ---"
    # The cleanup function is robust to partial failures.
    if [ -n "$TEST_DIR" ] && [ -d "$TEST_DIR" ]; then
        # Unmount any filesystems in the test directory
        mount | grep "$TEST_DIR" | awk '{print $3}' | xargs -r umount -l || true
        # Destroy ZFS pools
        if zpool status zfs-test-pool &>/dev/null; then zpool destroy -f zfs-test-pool; fi
        # Close LUKS devices
        if ls /dev/mapper/ext4-luks-disk &>/dev/null; then cryptsetup close ext4-luks-disk; fi
        # Detach loop devices
        if [ -n "$DISK1_LOOP" ] && losetup "$DISK1_LOOP" &>/dev/null; then losetup -d "$DISK1_LOOP"; fi
        if [ -n "$DISK2_LOOP" ] && losetup "$DISK2_LOOP" &>/dev/null; then losetup -d "$DISK2_LOOP"; fi
        info "Removing test directory '${TEST_DIR}'..."
        rm -rf "${TEST_DIR}"
    fi
    success "Cleanup complete."
}

step_0_build_sst() {
    info "Ensuring 'sst' binary is built..."
    (cd "${REPO_ROOT}" && make build)
    if [ ! -x "${SST_BIN}" ]; then
        error "'sst' binary not found at ${SST_BIN} after running make build."
        return 1
    fi
    success "'sst' binary is ready."
}

step_1_setup_environment() {
    TEST_DIR=$(mktemp -d)
    SECRETS_DIR="${TEST_DIR}/secrets"
    HARDWARE_YAML="${TEST_DIR}/hardware.yaml"
    DISK1_IMG="${TEST_DIR}/disk1.img"
    DISK2_IMG="${TEST_DIR}/disk2.img"
    info "Test environment created at: ${TEST_DIR}"
    mkdir -p "${SECRETS_DIR}"

    info "Creating two 1G sparse files for mock block devices..."
    truncate -s 1G "$DISK1_IMG"
    truncate -s 1G "$DISK2_IMG"
    DISK1_LOOP=$(losetup -f --show "$DISK1_IMG")
    DISK2_LOOP=$(losetup -f --show "$DISK2_IMG")
    success "Mock devices created at ${DISK1_LOOP} and ${DISK2_LOOP}"

    info "Generating mock hardware.yaml probe file..."
    DISK1_ID_PATH=$(find /dev/disk/by-id -lname "*${DISK1_LOOP##*/}" -print -quit)
    DISK2_ID_PATH=$(find /dev/disk/by-id -lname "*${DISK2_LOOP##*/}" -print -quit)

    cat > "$HARDWARE_YAML" <<EOF
apiVersion: sst.io/v1
kind: HardwareProbe
spec:
  blockDevices:
    - name: ${DISK1_LOOP##*/}
      hostPath: ${DISK1_ID_PATH}
      serial: mock-serial-1
      sizeBytes: 1073741824
    - name: ${DISK2_LOOP##*/}
      hostPath: ${DISK2_ID_PATH}
      serial: mock-serial-2
      sizeBytes: 1073741824
EOF
    success "Mock hardware inventory created at ${HARDWARE_YAML}"
}

step_2_zfs_workflow() {
    info "--- Starting Test Case 1: ZFS Mirror Workflow ---"
    local ZFS_LAYOUT_YAML="${TEST_DIR}/storage-layout-zfs.yaml"
    cd "$TEST_DIR"

    info "Planning ZFS layout..."
    "$SST_BIN" compose-storageplan --node "$HARDWARE_YAML" --remainder zfs-test-pool,mirror --filesets postgres,minio -o "$ZFS_LAYOUT_YAML"
    yq -e '.spec.pools[0].status.state == "plan-recorded"' "$ZFS_LAYOUT_YAML" >/dev/null
    success "ZFS plan generated with 'plan-recorded' state."

    info "Approving plan..."
    yq -i '(.. | select(has("state"))).state = "ready-for-implementation"' "$ZFS_LAYOUT_YAML"
    success "Plan approved to 'ready-for-implementation' state."

    info "Executing reconciliation..."
    # UPDATED: Using flat commands
    "$SST_BIN" mkpool --manifest "$ZFS_LAYOUT_YAML"
    "$SST_BIN" mkfset --manifest "$ZFS_LAYOUT_YAML"
    "$SST_BIN" mount --manifest "$ZFS_LAYOUT_YAML"
    
    info "Verifying system state..."
    zpool status zfs-test-pool >/dev/null || { error "ZFS pool 'zfs-test-pool' does not exist."; return 1; }
    zfs list zfs-test-pool/postgres-data >/dev/null || { error "ZFS fileset 'postgres-data' does not exist."; return 1; }
    mount | grep -q "${TEST_DIR}/mnt/data/minio" || { error "Minio fileset is not mounted."; return 1; }
    yq -e '.spec.pools[0].status.state == "deployed"' "$ZFS_LAYOUT_YAML" >/dev/null || { error "Pool status was not updated to 'deployed' in manifest."; return 1; }
    success "ZFS layout successfully reconciled and verified."

    info "Tearing down ZFS layout..."
    # UPDATED: Using flat commands
    "$SST_BIN" umount --manifest "$ZFS_LAYOUT_YAML"
    "$SST_BIN" destroyfset --manifest "$ZFS_LAYOUT_YAML"
    "$SST_BIN" destroypool --manifest "$ZFS_LAYOUT_YAML"
    ! zpool status zfs-test-pool &>/dev/null || { error "ZFS pool was not destroyed."; return 1; }
    success "ZFS teardown complete."
    cd - >/dev/null
}

step_3_ext4_luks_workflow() {
    info "--- Starting Test Case 2: Encrypted ext4 Workflow ---"
    local EXT4_LAYOUT_YAML="${TEST_DIR}/storage-layout-ext4.yaml"
    cd "$TEST_DIR"

    info "Planning ext4 layout..."
    yq -n '{apiVersion: "sst.io/v1", kind: "StorageLayout", spec: {devices: [{name: "ext4-luks-disk", kind: "raw", directory: ".", size: "512M", encryption: {enable: true, keyFile: "ext4-luks-disk.key"}, status: {state: "plan-recorded"}}], filesystems: [{name: "app-data-fs", kind: "ext4", deviceRef: "ext4-luks-disk", mountpoint: "/mnt/app-data", subdirectories: ["gitea", "harbor"], status: {state: "plan-recorded"}}]}}' > "$EXT4_LAYOUT_YAML"
    success "ext4 plan generated."

    info "Approving plan..."
    yq -i '(.. | select(has("state"))).state = "ready-for-implementation"' "$EXT4_LAYOUT_YAML"
    success "Plan approved."

    info "Executing reconciliation..."
    # UPDATED: Using flat commands
    "$SST_BIN" mkdev --manifest "$EXT4_LAYOUT_YAML"
    "$SST_BIN" format --manifest "$EXT4_LAYOUT_YAML"
    "$SST_BIN" mount --manifest "$EXT4_LAYOUT_YAML"

    info "Verifying system state..."
    [ -f "${SECRETS_DIR}/ext4-luks-disk.key" ] || { error "Encryption key file was not created."; return 1; }
    ls /dev/mapper/ext4-luks-disk >/dev/null || { error "LUKS device mapping was not created."; return 1; }
    lsblk -f | grep -q "/dev/mapper/ext4-luks-disk.*ext4" || { error "Device not formatted with ext4."; return 1; }
    mount | grep -q "${TEST_DIR}/mnt/app-data" || { error "Filesystem not mounted."; return 1; }
    [ -d "${TEST_DIR}/mnt/app-data/gitea" ] || { error "Subdirectory 'gitea' was not created."; return 1; }
    success "Encrypted ext4 layout successfully reconciled and verified."
    
    info "Tearing down ext4 layout..."
    # UPDATED: Using flat commands
    "$SST_BIN" umount --manifest "$EXT4_LAYOUT_YAML"
    "$SST_BIN" destroydev --manifest "$EXT4_LAYOUT_YAML"
    ! ls /dev/mapper/ext4-luks-disk &>/dev/null || { error "LUKS mapping was not closed."; return 1; }
    success "Encrypted ext4 teardown complete."
    cd - >/dev/null
}

run_main "$@"
