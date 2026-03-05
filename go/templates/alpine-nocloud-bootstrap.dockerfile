RUN: |
  set -e
  
  # 1. Install tiny-cloud and base packages (No python, no cloud-init)
  apk add alpine-base linux-virt grub-efi e2fsprogs e2fsprogs-extra \
      bash shadow tzdata sudo openssh dosfstools \
      tiny-cloud tiny-cloud-openrc tiny-cloud-nocloud \
      util-linux parted blkid sfdisk partx gptfdisk \
      busybox-mdev-openrc dhcpcd dhcpcd-openrc chrony chrony-openrc kmod

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

  # 4. Setup basic networking
  echo "auto lo" > /etc/network/interfaces
  echo "iface lo inet loopback" >> /etc/network/interfaces
  echo "" >> /etc/network/interfaces
  echo "auto eth0" >> /etc/network/interfaces
  echo "iface eth0 inet dhcp" >> /etc/network/interfaces

  # 5. Align Runlevels for tiny-cloud
  for svc in devfs dmesg mdev hwdrivers modules; do rc-update add $svc sysinit 2>/dev/null || true; done
  for svc in sysctl hostname bootmisc syslog networking chronyd dhcpcd tiny-cloud-early acpid; do rc-update add $svc boot 2>/dev/null || true; done
  for svc in sshd tiny-cloud-main tiny-cloud-final; do rc-update add $svc default 2>/dev/null || true; done

  # 6. Explicitly configure tiny-cloud for NoCloud (cidata)
  echo 'DATASOURCES="nocloud"' > /etc/tiny-cloud.conf
  echo 'SKIP_PACKAGE_UPDATE="yes"' >> /etc/tiny-cloud.conf
  echo 'SKIP_PACKAGE_UPGRADE="yes"' >> /etc/tiny-cloud.conf

  # 7. Wire up serial consoles
  echo "ttyS0::respawn:/sbin/getty -L ttyS0 115200 vt100" >> /etc/inittab
  echo "hvc0::respawn:/sbin/getty -L hvc0 115200 vt100" >> /etc/inittab
  echo "ttyS0" >> /etc/securetty
  echo "hvc0" >> /etc/securetty
