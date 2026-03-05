RUN: |
  set -e
  
  # 1. Install prerequisites (apk automatically registers acpid and cloud-init to OpenRC!)
  apk add openrc alpine-base linux-virt grub-efi e2fsprogs e2fsprogs-extra bash tzdata sudo openssh dosfstools cloud-init eudev eudev-openrc util-linux blkid cloud-utils-growpart parted gptfdisk kmod acpid
  
  # 2. Force CD-ROM/ISO and ACPI/Input kernel modules for crosvm
  echo "isofs" >> /etc/modules
  echo "cdrom" >> /etc/modules
  echo "button" >> /etc/modules
  echo "evdev" >> /etc/modules
  
  # 3. Wire ACPI power button event to shutdown
  mkdir -p /etc/acpi/events
  echo "event=button/power.*" > /etc/acpi/events/power
  echo "action=/sbin/poweroff" >> /etc/acpi/events/power
  
  # 4. FIX ISO MOUNTING: Force modules to load BEFORE udev
  sed -i '/depend() {/a \    before udev' /etc/init.d/modules || true
  
  # 5. FIX CLOCK HANG: Lobotomize hwclock entirely (Verified working)
  echo '#!/sbin/openrc-run' > /etc/init.d/hwclock
  echo 'name="hwclock dummy"' >> /etc/init.d/hwclock
  echo 'start() { return 0; }' >> /etc/init.d/hwclock
  echo 'stop() { return 0; }' >> /etc/init.d/hwclock
  chmod +x /etc/init.d/hwclock
  
  # 6. FIX /DEV/LOG: Strip broken clock dependency from syslog (Verified working)
  sed -i 's/need clock hostname/need hostname/' /etc/init.d/syslog || true
  sed -i '/depend() {/a \    before cloud-init-local' /etc/init.d/syslog || true
  
  # 7. Swap mdev for proper udev
  rc-update del mdev sysinit || true
  rc-update add udev sysinit
  rc-update add udev-trigger sysinit
  rc-update add udev-settle sysinit
  rc-update add udev-postmount default
  
  # 8. Configure Networking
  echo "auto lo" > /etc/network/interfaces
  echo "iface lo inet loopback" >> /etc/network/interfaces
  echo "" >> /etc/network/interfaces
  echo "auto eth0" >> /etc/network/interfaces
  echo "iface eth0 inet dhcp" >> /etc/network/interfaces
  
  # 9. Base system services (NO manual acpid or cloud-init additions here)
  for svc in devfs dmesg hwdrivers modules syslog; do rc-update add $svc sysinit 2>/dev/null || true; done
  for svc in sysctl hostname bootmisc sshd networking; do rc-update add $svc boot 2>/dev/null || true; done
  
  # 10. Cloud-Init Strict Config
  mkdir -p /etc/cloud/cloud.cfg.d
  echo "datasource_list: [ NoCloud, None ]" > /etc/cloud/cloud.cfg.d/99_nocloud.cfg
  
  echo "package_update: false" > /etc/cloud/cloud.cfg.d/99_no_upgrade.cfg
  echo "package_upgrade: false" >> /etc/cloud/cloud.cfg.d/99_no_upgrade.cfg
  
  # Tell cloud-init the ISO is exactly where crosvm puts it
  echo "datasource:" > /etc/cloud/cloud.cfg.d/99_seed.cfg
  echo "  NoCloud:" >> /etc/cloud/cloud.cfg.d/99_seed.cfg
  echo "    seedfrom: /dev/vdc" >> /etc/cloud/cloud.cfg.d/99_seed.cfg
  
  # 11. Wire up serial consoles
  echo "ttyS0::respawn:/sbin/getty -L ttyS0 115200 vt100" >> /etc/inittab
  echo "hvc0::respawn:/sbin/getty -L hvc0 115200 vt100" >> /etc/inittab
  echo "ttyS0" >> /etc/securetty
  echo "hvc0" >> /etc/securetty
