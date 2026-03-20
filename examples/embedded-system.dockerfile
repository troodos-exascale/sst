# ==========================================
# 1. The Kernel Forge (The Getaway Engine + ZFS)
# ==========================================
FROM ubuntu:24.04 AS kernel-builder

# Cache these massive static tarballs permanently!
GET https://cdn.kernel.org/pub/linux/kernel/v6.8/linux-6.8.tar.xz /tmp/linux.tar.xz
GET https://github.com/openzfs/zfs/releases/download/zfs-2.2.3/zfs-2.2.3.tar.gz /tmp/zfs.tar.gz

RUN: |
  # Install kernel AND OpenZFS build dependencies
  apt-get update && apt-get install -y build-essential bc flex bison libelf-dev \
    libssl-dev wget tar autoconf automake libtool gawk alien fakeroot dkms \
    libblkid-dev uuid-dev libudev-dev libattr1-dev zlib1g-dev libselinux-dev

  # A. Build the Micro-Kernel
  # The source files were injected into /tmp by the GET instructions
  tar -xf /tmp/linux.tar.xz -C /usr/src/
  cd /usr/src/linux-6.8

  make tinyconfig
  cat << 'EOF' > microvm.config
  CONFIG_64BIT=y
  CONFIG_PCI=y
  CONFIG_VIRTIO=y
  CONFIG_VIRTIO_PCI=y
  CONFIG_VIRTIO_BLK=y
  CONFIG_VIRTIO_CONSOLE=y
  CONFIG_VIRTIO_FS=y
  CONFIG_VIRTIO_NET=y
  CONFIG_FUSE_FS=y
  CONFIG_EXT4_FS=y
  CONFIG_PRINTK=y
  CONFIG_TTY=y
  CONFIG_HVC_DRIVER=y
  CONFIG_HVC_CONSOLE=y
  CONFIG_MODULES=y      # <-- REQUIRED: Enable loadable module support for ZFS
  CONFIG_KMOD=y
  EOF

  scripts/kconfig/merge_config.sh .config microvm.config
  make -j$(nproc) vmlinux modules_prepare

  # B. Build the out-of-tree ZFS Kernel Modules against our Micro-Kernel
  cd /usr/src
  tar -xf /tmp/zfs.tar.gz
  cd zfs-2.2.3

  # Configure ZFS to strictly build ONLY the kernel modules using our custom kernel path
  ./configure --with-linux=/usr/src/linux-6.8 --with-linux-obj=/usr/src/linux-6.8
  make -j$(nproc) modules

# ==========================================
# 2. The Parts Bin (The Vault)
# ==========================================
FROM ubuntu:24.04 AS ubuntu-massive
RUN: apt-get update && apt-get install -y qemu-utils zfsutils-linux e2fsprogs iproute2

ARG CORE_UTILS: |
  /usr/bin/qemu-nbd
  /sbin/mkfs.ext4
  /sbin/zfs
  /sbin/zpool
  /sbin/ip

# (Assuming 'go-collection' stage exists prior to this)
ARG GO_TOOLS: |
  /src/sst/cmd/runctl
  /src/sst/cmd/gitpm
  /src/sst/cmd/sst

# ==========================================
# 3. Assemble the Runtime (The Heist)
# ==========================================
FROM scratch
NAME: ultra-lean-runtime

# Extract the custom Micro-Kernel
COPY: --from=kernel-builder /usr/src/linux-6.8/vmlinux ${ROOTFS}/kernel

# Extract the compiled ZFS kernel modules (*.ko)
COPY: --from=kernel-builder /usr/src/zfs-2.2.3/module/*.ko ${ROOTFS}/lib/modules/

# Extract the C binaries + exactly the micro-libraries they need
COPY [mklibs]: --from=ubuntu-massive $CORE_UTILS ${ROOTFS}/

# Extract the Go tools, compile them into a single 'bb' multi-call binary via u-root
COPY [uroot]: --from=go-collection $GO_TOOLS ${ROOTFS}/init
