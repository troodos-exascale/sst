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
  
  # Install EFI bootloader and disk utilities
  apk add grub-efi dosfstools parted gptfdisk

  # Configure GRUB to route kernel output to our terminal (hvc0)
  mkdir -p /etc/default
  echo 'GRUB_TIMEOUT=5' > /etc/default/grub
  echo 'GRUB_TIMEOUT_STYLE="menu"' >> /etc/default/grub
  echo 'GRUB_CMDLINE_LINUX_DEFAULT="console=hvc0 oops=panic panic=10 loglevel=4 rootfstype=ext4 rw rootwait"' >> /etc/default/grub
  echo 'GRUB_TERMINAL="console"' >> /etc/default/grub

# CHANGED: Call the actual path to your OS-agnostic disk assembler
INCLUDE common/boot-efi.dockerfile
