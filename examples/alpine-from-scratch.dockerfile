FROM scratch
NAME: alpine-scratch

# 1. Bootstrap the root filesystem from scratch using the host context
RUN [host]: |
  set -e
  echo "--> Bootstrapping Alpine Linux from scratch into ${ROOTFS}..."
  
  # Download the official Alpine mini rootfs (aarch64 to match your Mac/ARM64 Linux host)
  wget -qO /tmp/alpine-rootfs.tar.gz "https://dl-cdn.alpinelinux.org/alpine/v3.22/releases/aarch64/alpine-minirootfs-3.22.0-aarch64.tar.gz"
  
  # Extract directly into the builder's empty ROOTFS target
  tar -xzf /tmp/alpine-rootfs.tar.gz -C "${ROOTFS}"
  rm -f /tmp/alpine-rootfs.tar.gz

# 2. Hydrate the OS for NoCloud environments
# Because the ROOTFS is now populated, standard [chroot] commands will work normally
INCLUDE alpine-nocloud-bootstrap.dockerfile

# 3. Assemble the physical disk and install GRUB
INCLUDE boot-efi.dockerfile

# 4. Trigger the desugared emit templates to extract the kernel and build the QCOW2
EMIT: raw, kernel, qcow2
