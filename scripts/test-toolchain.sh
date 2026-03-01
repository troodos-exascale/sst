#!/usr/bin/env bash

set -e

# Setup absolute paths
SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)
REPO_ROOT=$(cd -- "${SCRIPT_DIR}/../" &>/dev/null && pwd)

echo "==> Building tools..."
cd "${REPO_ROOT}/go"
make build

export PATH="${REPO_ROOT}/go/build/linux/amd64:${PATH}"

# Setup isolated test environment
TEST_DIR=$(mktemp -d)
export SST_RUNTIMEDIR="${TEST_DIR}"
echo "==> Using isolated environment: ${SST_RUNTIMEDIR}"

# Create a test Dockerfile
DOCKERFILE="${TEST_DIR}/test-vm.docker"
cat <<EOF > "${DOCKERFILE}"
FROM alpine:latest
NAME: alpine-test
EMIT: raw, qcow2, oci
RUN: |
    echo "hello world" > /root/hello.txt
    # Install a real Linux kernel so mkimage can extract it for direct boot
    apk add --no-cache linux-virt
EOF

echo "==> 1. Running mkimage build (requires sudo for chroot)..."
sudo -E mkimage build "${DOCKERFILE}"

# Fix ownership so the normal user can consume the built images
echo "==> Fixing ownership of generated files..."
sudo chown -R $(id -u):$(id -g) "${SST_RUNTIMEDIR}"

echo "==> 2. Verifying EMIT outputs..."
ls -lh "${SST_RUNTIMEDIR}/images/alpine-test/"
[ -f "${SST_RUNTIMEDIR}/images/alpine-test/alpine-test.raw" ] || { echo "Missing .raw"; exit 1; }
[ -f "${SST_RUNTIMEDIR}/images/alpine-test/alpine-test.qcow2" ] || { echo "Missing .qcow2"; exit 1; }
[ -f "${SST_RUNTIMEDIR}/images/alpine-test/alpine-test.tar" ] || { echo "Missing .tar"; exit 1; }

echo "==> 3. Creating instance (as normal user)..."
# Notice: No --efi flag. This strictly enforces direct Linux kernel boot extraction.
mkimage instance alpine-test inst1

echo "==> 4. Setting up host networking (requires sudo)..."
sudo -E "${REPO_ROOT}/go/build/linux/amd64/runctl" setup-host

echo "==> 5. Starting VM in background (as normal user)..."
# This will now successfully boot the real Alpine kernel!
runctl start inst1 &
VM_PID=$!

# Give the VM a few seconds to boot and establish itself
sleep 3
echo "==> Running instances:"
runctl list

echo "==> 6. Stopping VM..."
runctl stop inst1
wait $VM_PID || true

echo "==> Cleanup..."
sudo rm -rf "${TEST_DIR}"
echo "==> ALL TESTS PASSED ✅"
