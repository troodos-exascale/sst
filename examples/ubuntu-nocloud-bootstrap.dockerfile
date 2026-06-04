
RUN: |
  set -e
  export DEBIAN_FRONTEND=noninteractive
  
  echo "--> Hydrating Ubuntu Base with VM kernel and cloud-init..."

  # --- ENABLE APT CACHING ---
  # Canonical's base tarballs contain anti-caching configurations designed for Docker.
  # We must obliterate them so our ZFS mount (/var/cache/apt/archives) actually retains the .deb files!
  rm -f /etc/apt/apt.conf.d/docker-clean
  echo 'Binary::apt::APT::Keep-Downloaded-Packages "true";' > /etc/apt/apt.conf.d/01keep-cache
  
  # 1. Update and install core VM components
  apt-get update
  apt-get install -y --no-install-recommends \
      linux-image-virtual \
      systemd-sysv \
      cloud-init \
      initramfs-tools \
      dosfstools \
      e2fsprogs \
      fdisk \
      udev \
      sudo \
      openssh-server \
      iproute2 \
      iputils-ping \
      netplan.io
      
  # Install the correct GRUB binaries so boot-efi.dockerfile doesn't fail
  ARCH=$(uname -m)
  if [ "$ARCH" = "aarch64" ]; then
      apt-get install -y --no-install-recommends grub-efi-arm64-bin grub-efi-arm64
  else
      apt-get install -y --no-install-recommends grub-efi-amd64-bin grub-efi-amd64
  fi

  # 2. Force necessary modules for crosvm into initramfs
  echo "virtio_pci" >> /etc/initramfs-tools/modules
  echo "virtio_blk" >> /etc/initramfs-tools/modules
  echo "virtio_console" >> /etc/initramfs-tools/modules
  echo "virtio_net" >> /etc/initramfs-tools/modules
  echo "virtio_fs" >> /etc/initramfs-tools/modules
  update-initramfs -u

  # 3. Neuter cloud-init: ONLY allow local NoCloud (cidata.iso) to prevent network hangs
  echo "datasource_list: [ NoCloud, None ]" > /etc/cloud/cloud.cfg.d/90_dpkg.cfg

  # 4. Wire up serial consoles (hvc0 for crosvm)
  systemctl enable serial-getty@hvc0.service

  # 5. Configure GRUB (used by standard UEFI boots, bypassed by crosvm direct-boot)
  mkdir -p /etc/default
  echo 'GRUB_TIMEOUT=5' > /etc/default/grub
  echo 'GRUB_TIMEOUT_STYLE="menu"' >> /etc/default/grub
  echo 'GRUB_CMDLINE_LINUX_DEFAULT="console=hvc0 oops=panic panic=10 loglevel=4 rw"' >> /etc/default/grub
  echo 'GRUB_TERMINAL="console"' >> /etc/default/grub

  # 6. Setup netplan to use DHCP on eth0 (crosvm default network)
  printf "network:\n  version: 2\n  ethernets:\n    eth0:\n      dhcp4: true\n" > /etc/netplan/01-netcfg.yaml

  # 7. Grant passwordless sudo to all users (since this is a local dev VM)
  echo "ALL ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/99-nopasswd
  chmod 0440 /etc/sudoers.d/99-nopasswd
