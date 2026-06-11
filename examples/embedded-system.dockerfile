# ==========================================
# 1. The Kernel Forge (The Getaway Engine + ZFS)
# ==========================================
FROM ubuntu:24.04 AS kernel-builder

RUN [$TARGET_STAGE]: |
  set -e
  
  # SURGICAL FIX 1: Ubuntu's minimal OCI image ships with a broken statoverride for messagebus.
  if [ -f /var/lib/dpkg/statoverride ]; then
      sed -i '/messagebus/d' /var/lib/dpkg/statoverride || true
  fi

  # SURGICAL FIX 2: Obliterate Ubuntu's OCI anti-caching rules so our ZFS mount actually works!
  rm -f /etc/apt/apt.conf.d/docker-clean
  echo 'Binary::apt::APT::Keep-Downloaded-Packages "true";' > /etc/apt/apt.conf.d/01keep-cache

  apt-get update && apt-get install -y build-essential bc flex bison libelf-dev \
    libssl-dev curl tar autoconf automake libtool gawk alien fakeroot dkms \
    libblkid-dev uuid-dev libudev-dev libattr1-dev zlib1g-dev libselinux-dev

  mkdir -p /tmp/src && cd /tmp/src
  
  echo "--> Fetching Kernel and ZFS sources..."
  curl -fsSL https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.8.12.tar.xz | tar -xJ
  curl -fsSL https://github.com/openzfs/zfs/releases/download/zfs-2.2.3/zfs-2.2.3.tar.gz | tar -xz

  # A. Build the Micro-Kernel
  echo "--> Compiling Micro-Kernel..."
  cd /tmp/src/linux-6.8.12
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
  CONFIG_MODULES=y
  CONFIG_KMOD=y
  CONFIG_BINFMT_ELF=y
  CONFIG_BINFMT_SCRIPT=y
  CONFIG_TMPFS=y
  CONFIG_SYSFS=y
  CONFIG_PROC_FS=y
  EOF
  scripts/kconfig/merge_config.sh .config microvm.config
  make -j$(nproc) vmlinux modules_prepare

  # B. Build the out-of-tree ZFS Kernel Modules against our Micro-Kernel
  echo "--> Compiling ZFS Modules..."
  cd /tmp/src/zfs-2.2.3
  ./configure --with-linux=/tmp/src/linux-6.8.12 --with-linux-obj=/tmp/src/linux-6.8.12
  make -j$(nproc) modules


# ==========================================
# 2. The u-root Forge (Go Userland)
# ==========================================
FROM golang:1.25 AS uroot-builder

RUN [$TARGET_STAGE]: |
  set -e
  apt-get update && apt-get install -y cpio git
  
  export GOPATH=/go
  export PATH=/usr/local/go/bin:$GOPATH/bin:$PATH
  mkdir -p /go/bin /go/src
  
  echo "--> Preparing u-root source..."
  if [ -d "/go/src/github.com/u-root/u-root/.git" ]; then
      echo "--> u-root already cloned, pulling latest..."
      cd /go/src/github.com/u-root/u-root
      git pull
  else
      echo "--> Cloning u-root source..."
      rm -rf /go/src/github.com/u-root/u-root
      git clone https://github.com/u-root/u-root /go/src/github.com/u-root/u-root
      cd /go/src/github.com/u-root/u-root
  fi
  
  echo "--> Installing u-root builder..."
  go install .
  
  echo "--> Building Go userland (bb format)..."
  # Running inside the repo guarantees Go module resolution works flawlessly
  /go/bin/u-root -build=bb -o /tmp/initramfs.cpio core
  

# ==========================================
# 3. The Parts Bin (The Vault)
# ==========================================
FROM ubuntu:24.04 AS ubuntu-massive

RUN [$TARGET_STAGE]: |
  set -e
  
  if [ -f /var/lib/dpkg/statoverride ]; then
      sed -i '/messagebus/d' /var/lib/dpkg/statoverride || true
  fi

  rm -f /etc/apt/apt.conf.d/docker-clean
  echo 'Binary::apt::APT::Keep-Downloaded-Packages "true";' > /etc/apt/apt.conf.d/01keep-cache

  apt-get update && apt-get install -y qemu-utils zfsutils-linux e2fsprogs iproute2


# ==========================================
# 4. Assemble the Runtime (The Heist)
# ==========================================
FROM scratch
NAME: ultra-lean-runtime

# Using RUN [host] to surgically orchestrate the assembly without relying on COPY variable expansion
RUN [host]: |
  set -e
  echo "--> Assembling ultra-lean runtime..."
  
  # Setup basic filesystem hierarchy
  mkdir -p "${ROOTFS}/bin" "${ROOTFS}/lib/modules" "${ROOTFS}/dev" "${ROOTFS}/proc" "${ROOTFS}/sys" "${ROOTFS}/tmp"
  
  # 1. Extract the custom Micro-Kernel - We don't need this because we're using the custom kernel built in the kernel-builder stage
  # cp -a "${BASEDIR}/as-roots/kernel-builder/tmp/src/linux-6.8.12/vmlinux" "${ROOTFS}/kernel"
  
  # 2. Extract the compiled ZFS kernel modules
  find "${BASEDIR}/as-roots/kernel-builder/tmp/src/zfs-2.2.3/module" -name "*.ko" -exec cp -a {} "${ROOTFS}/lib/modules/" \;
  
  # 3. Extract the u-root userland from its cpio archive
  cd "${ROOTFS}"
  cat "${BASEDIR}/as-roots/uroot-builder/tmp/initramfs.cpio" | cpio -idm --quiet
  
  # 4. Extract specific C binaries from ubuntu-massive
  # (Note: Standard C binaries require their linked .so libraries to function. Since u-root gives 
  # us a working native Go shell, we'll just drop these binaries in as placeholders for the demo).
  cp -a "${BASEDIR}/as-roots/ubuntu-massive/usr/bin/qemu-nbd" "${ROOTFS}/bin/" || true
  cp -a "${BASEDIR}/as-roots/ubuntu-massive/sbin/mkfs.ext4" "${ROOTFS}/bin/" || true
  cp -a "${BASEDIR}/as-roots/ubuntu-massive/sbin/zfs" "${ROOTFS}/bin/" || true
  
  # 5. Create the init script that u-root / kernel expects to run as PID 1
  cat << 'EOF' > "${ROOTFS}/init"
  #!/bbin/elvish
  # u-root uses the Elvish shell by default
  mount -t proc none /proc
  mount -t sysfs none /sys
  mount -t devtmpfs none /dev

  echo "======================================"
  echo " Booting SST Ultra-Lean Micro-Kernel! "
  echo "======================================"
  
  # Load the ZFS module we compiled
  insmod /lib/modules/zfs.ko || echo "Failed to load ZFS module"

  # Drop into the interactive u-root shell
  exec /bbin/elvish
  EOF
  
  chmod +x "${ROOTFS}/init"

# 5. Emit the final artifacts
# Instead of building a raw disk, we pack the ROOTFS directly into an initramfs 
# so you can boot it instantly with `crosvm run --initrd ...`
RUN [host]: |
  set -e
  mkdir -p "${OUTDIR}"
  cd "${ROOTFS}"
  
  echo "--> Packing initramfs (now 100% kernel-free!)..."
  find . | LC_ALL=C sort | cpio --reproducible --owner=0:0 -H newc -o | gzip -9 > "${OUTDIR}/initrd"
  
  echo "--> Promoting Kernel directly to output..."
  cp -a "${BASEDIR}/as-roots/kernel-builder/tmp/src/linux-6.8.12/vmlinux" "${OUTDIR}/kernel"
   
  echo "✅ Bootable MicroVM ready: ${OUTDIR}/kernel and ${OUTDIR}/initrd"
