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
