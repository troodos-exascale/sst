RUN: |
  set -e
  
  # 1. Install tiny-cloud and base packages (Aggressively Optimized)
  apk add alpine-base linux-virt grub-efi e2fsprogs \
      tzdata doas dropbear dosfstools \
      tiny-cloud tiny-cloud-openrc tiny-cloud-nocloud \
      util-linux parted blkid gptfdisk \
      busybox-mdev-openrc kmod

  # Conditionally install acpid if the architecture is not ARM
  case "$(uname -m)" in
    aarch64|arm*) ;;
    *) apk add acpid ;;
  esac

  # 2. Force CD-ROM/ISO kernel modules for crosvm early
  echo "isofs" >> /etc/modules
  echo "cdrom" >> /etc/modules

  # 3. Bypass hwclock timeout on crosvm
  echo '#!/sbin/openrc-run' > /etc/init.d/hwclock
  echo 'name="hwclock dummy"' >> /etc/init.d/hwclock
  echo 'start() { return 0; }' >> /etc/init.d/hwclock
  echo 'stop() { return 0; }' >> /etc/init.d/hwclock
  chmod +x /etc/init.d/hwclock
  sed -i 's/need clock hostname/need hostname/' /etc/init.d/syslog || true

  # 4. Setup basic networking (relies on BusyBox udhcpc)
  echo "auto lo" > /etc/network/interfaces
  echo "iface lo inet loopback" >> /etc/network/interfaces
  echo "" >> /etc/network/interfaces
  echo "auto eth0" >> /etc/network/interfaces
  echo "iface eth0 inet dhcp" >> /etc/network/interfaces

  # 5. Align Runlevels for tiny-cloud
  for svc in devfs dmesg mdev hwdrivers modules; do rc-update add $svc sysinit 2>/dev/null || true; done
  for svc in sysctl hostname bootmisc syslog networking ntpd tiny-cloud-early acpid; do rc-update add $svc boot 2>/dev/null || true; done
  for svc in dropbear tiny-cloud-main tiny-cloud-final; do rc-update add $svc default 2>/dev/null || true; done

  # 6. Explicitly configure tiny-cloud for NoCloud (cidata)
  echo 'DATASOURCES="nocloud"' > /etc/tiny-cloud.conf
  echo 'SKIP_PACKAGE_UPDATE="yes"' >> /etc/tiny-cloud.conf
  echo 'SKIP_PACKAGE_UPGRADE="yes"' >> /etc/tiny-cloud.conf

  # 7. Wire up serial consoles
  echo "ttyS0::respawn:/sbin/getty -L ttyS0 115200 vt100" >> /etc/inittab
  echo "hvc0::respawn:/sbin/getty -L hvc0 115200 vt100" >> /etc/inittab
  echo "ttyS0" >> /etc/securetty
  echo "hvc0" >> /etc/securetty

  # 8. Configure GRUB to route kernel output to our terminal (hvc0)
# 8. Configure GRUB to display the menu using standard EFI console mapping
  mkdir -p /etc/default
  echo 'GRUB_TIMEOUT=5' > /etc/default/grub
  echo 'GRUB_TIMEOUT_STYLE="menu"' >> /etc/default/grub
  echo 'GRUB_CMDLINE_LINUX_DEFAULT="console=hvc0 oops=panic panic=10 loglevel=4 rootfstype=ext4 rw rootwait"' >> /etc/default/grub
  echo 'GRUB_TERMINAL="console"' >> /etc/default/grub
  
  # 9. Clean up apk cache to reduce image size
  rm -rf /var/cache/apk/*
