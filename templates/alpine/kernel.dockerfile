RUN: |
  set -e
  mkdir -p /var/cache/apk
  ln -s /var/cache/apk /etc/apk/cache || true

  # Install the kernel and hardware-level modules
  apk add linux-virt kmod e2fsprogs blkid

  # Conditionally install acpid if the architecture is not ARM
  case "$(uname -m)" in
    aarch64|arm*) ;;
    *) apk add acpid; rc-update add acpid boot 2>/dev/null || true ;;
  esac

  # Force CD-ROM/ISO kernel modules for crosvm early
  echo "isofs" >> /etc/modules
  echo "cdrom" >> /etc/modules

  # Add kernel-specific runlevels
  for svc in hwdrivers modules; do rc-update add $svc sysinit 2>/dev/null || true; done
