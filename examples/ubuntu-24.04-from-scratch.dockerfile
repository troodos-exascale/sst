# CANONICAL_NAME: built-from-scratch
# LAST_BUILD: 20260321-134302
FROM scratch
NAME built-from-scratch

# REQUIRED: Declare the argument so the Go engine is allowed to inject it!
ARG DEB_ARCH

# 1. Bootstrap the root filesystem from Canonical's Ubuntu Base tarball
GET http://cdimage.ubuntu.com/ubuntu-base/releases/24.04/release/ubuntu-base-24.04.4-base-${DEB_ARCH}.tar.gz /ubuntu-base.tar.gz

RUN [host]: |
  set -e
  echo "--> Extracting cached Ubuntu Noble (24.04) into ${ROOTFS}..."
  
  # Extract the payload injected by the GET instruction
  tar -xzf "${ROOTFS}/ubuntu-base.tar.gz" -C "${ROOTFS}"
  rm -f "${ROOTFS}/ubuntu-base.tar.gz"

# 2. Hydrate the OS for NoCloud VM environments
INCLUDE ubuntu-nocloud-bootstrap.dockerfile

# 3. Assemble the physical disk and install GRUB
INCLUDE boot-efi.dockerfile

# 4. Trigger the desugared emit templates to extract the kernel and build the QCOW2
EMIT: raw, kernel, qcow2
