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

RUN [host]: |
  set -e
  echo "--> Configuring mDNS (Avahi) for ${INST_HOSTNAME}.local ..."
  
  # Inject avahi if the base image lacks it
  if ! chroot "${ROOTFS}" command -v avahi-daemon >/dev/null; then
      export DEBIAN_FRONTEND=noninteractive
      chroot "${ROOTFS}" apt-get update
      chroot "${ROOTFS}" apt-get install -y avahi-daemon libnss-mdns
  fi
  
  AVAHI_CONF="${ROOTFS}/etc/avahi/avahi-daemon.conf"
  if [ -f "$AVAHI_CONF" ]; then
      sed -i "s/.*host-name=.*/host-name=${INST_HOSTNAME}/" "$AVAHI_CONF"
  fi
  
  if chroot "${ROOTFS}" command -v systemctl >/dev/null; then
      chroot "${ROOTFS}" systemctl enable avahi-daemon || true
  fi
