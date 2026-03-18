FROM scratch
NAME: ubuntu-scratch

# 1. Bootstrap the root filesystem from Canonical's Ubuntu Base tarball
RUN [host]: |
  set -e
  echo "--> Bootstrapping Ubuntu Noble (24.04) from scratch into ${ROOTFS}..."
  
  # Detect architecture to pull the right tarball (aarch64 -> arm64)
  ARCH=$(uname -m)
  if [ "$ARCH" = "aarch64" ]; then UBUNTU_ARCH="arm64"; else UBUNTU_ARCH="amd64"; fi
  
  TARBALL="ubuntu-base-24.04.4-base-${UBUNTU_ARCH}.tar.gz"
  URL="http://cdimage.ubuntu.com/ubuntu-base/releases/24.04/release/${TARBALL}"
  
  wget -qO "/tmp/${TARBALL}" "$URL"
  tar -xzf "/tmp/${TARBALL}" -C "${ROOTFS}"
  rm -f "/tmp/${TARBALL}"

# 2. Hydrate the OS for NoCloud VM environments
INCLUDE ubuntu-nocloud-bootstrap.dockerfile

# 3. Assemble the physical disk and install GRUB
INCLUDE boot-efi.dockerfile

# 4. Trigger the desugared emit templates to extract the kernel and build the QCOW2
EMIT: raw, kernel, qcow2
