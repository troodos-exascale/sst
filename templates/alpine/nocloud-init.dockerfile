# Copyright 2026 Peter J. Braam
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

RUN: |
  set -e
  mkdir -p /var/cache/apk
  ln -s /var/cache/apk /etc/apk/cache || true

  # Install tiny-cloud and base OS userland (No kernel/bootloader here!)
  apk add alpine-base tzdata doas dropbear \
      tiny-cloud tiny-cloud-openrc tiny-cloud-nocloud \
      busybox-mdev-openrc util-linux

  # Bypass hwclock timeout on crosvm
  echo '#!/sbin/openrc-run' > /etc/init.d/hwclock
  echo 'name="hwclock dummy"' >> /etc/init.d/hwclock
  echo 'start() { return 0; }' >> /etc/init.d/hwclock
  echo 'stop() { return 0; }' >> /etc/init.d/hwclock
  chmod +x /etc/init.d/hwclock
  sed -i 's/need clock hostname/need hostname/' /etc/init.d/syslog || true

  # Setup basic networking (relies on BusyBox udhcpc)
  echo "auto lo" > /etc/network/interfaces
  echo "iface lo inet loopback" >> /etc/network/interfaces
  echo "" >> /etc/network/interfaces
  echo "auto eth0" >> /etc/network/interfaces
  echo "iface eth0 inet dhcp" >> /etc/network/interfaces

  # Align Runlevels for tiny-cloud and base system
  for svc in devfs dmesg mdev; do rc-update add $svc sysinit 2>/dev/null || true; done
  for svc in sysctl hostname bootmisc syslog networking ntpd tiny-cloud-early; do rc-update add $svc boot 2>/dev/null || true; done
  for svc in dropbear tiny-cloud-main tiny-cloud-final; do rc-update add $svc default 2>/dev/null || true; done

  # Explicitly configure tiny-cloud for NoCloud (cidata)
  echo 'DATASOURCES="nocloud"' > /etc/tiny-cloud.conf
  echo 'SKIP_PACKAGE_UPDATE="yes"' >> /etc/tiny-cloud.conf
  echo 'SKIP_PACKAGE_UPGRADE="yes"' >> /etc/tiny-cloud.conf

  # Wire up serial consoles
  echo "ttyS0::respawn:/sbin/getty -L ttyS0 115200 vt100" >> /etc/inittab
  echo "hvc0::respawn:/sbin/getty -L hvc0 115200 vt100" >> /etc/inittab
  echo "ttyS0" >> /etc/securetty
  echo "hvc0" >> /etc/securetty
