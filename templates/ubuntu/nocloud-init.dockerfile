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
  export DEBIAN_FRONTEND=noninteractive
  echo "--> Hydrating Ubuntu Base with VM kernel and cloud-init..."

  rm -f /etc/apt/apt.conf.d/docker-clean
  echo 'Binary::apt::APT::Keep-Downloaded-Packages "true";' > /etc/apt/apt.conf.d/01keep-cache
  
  apt-get update
  apt-get install -y --no-install-recommends \
      linux-image-virtual systemd-sysv cloud-init initramfs-tools dosfstools \
      e2fsprogs fdisk udev sudo openssh-server iproute2 iputils-ping netplan.io
      
  ARCH=$(uname -m)
  if [ "$ARCH" = "aarch64" ]; then
      apt-get install -y --no-install-recommends grub-efi-arm64-bin grub-efi-arm64
  else
      apt-get install -y --no-install-recommends grub-efi-amd64-bin grub-efi-amd64
  fi

  echo "virtio_pci" >> /etc/initramfs-tools/modules
  echo "virtio_blk" >> /etc/initramfs-tools/modules
  echo "virtio_console" >> /etc/initramfs-tools/modules
  echo "virtio_net" >> /etc/initramfs-tools/modules
  echo "virtio_fs" >> /etc/initramfs-tools/modules
  update-initramfs -u

  # Neuter cloud-init: ONLY allow local NoCloud (cidata.iso)
  echo "datasource_list: [ NoCloud, None ]" > /etc/cloud/cloud.cfg.d/90_dpkg.cfg

  # Wire up serial consoles (hvc0 for crosvm)
  systemctl enable serial-getty@hvc0.service

  # Configure GRUB
  mkdir -p /etc/default
  echo 'GRUB_TIMEOUT=5' > /etc/default/grub
  echo 'GRUB_TIMEOUT_STYLE="menu"' >> /etc/default/grub
  echo 'GRUB_CMDLINE_LINUX_DEFAULT="console=hvc0 oops=panic panic=10 loglevel=4 rw"' >> /etc/default/grub
  echo 'GRUB_TERMINAL="console"' >> /etc/default/grub

