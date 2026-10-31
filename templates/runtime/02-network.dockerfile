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
  echo "--> Configuring networking..."
  echo "${INST_HOSTNAME}" > "${ROOTFS}/etc/hostname"
  
  if [ -n "${INST_DOMAIN}" ]; then
      echo "127.0.1.1 ${INST_HOSTNAME}.${INST_DOMAIN} ${INST_HOSTNAME}" >> "${ROOTFS}/etc/hosts"
  fi

  mkdir -p "${ROOTFS}/etc/systemd/network"
  NET_CONF="${ROOTFS}/etc/systemd/network/10-eth0.network"
  
  cat << NET > "$NET_CONF"
[Match]
MACAddress=${INST_MAC}

[Network]
NET

  # Dynamic routing based on IPV4 definition
  if [ -n "${INST_IPV4}" ]; then
      echo "Address=${INST_IPV4}" >> "$NET_CONF"
      if [ -n "${INST_GATEWAY}" ]; then
          echo "Gateway=${INST_GATEWAY}" >> "$NET_CONF"
      fi
      echo "DNS=8.8.8.8" >> "$NET_CONF"
  else
      echo "DHCP=ipv4" >> "$NET_CONF"
      echo "DHCPClientIdentifier=duid" >> "$NET_CONF"
      echo "DUIDType=link-layer-time" >> "$NET_CONF"
      echo "DUIDRawData=${INST_DUID}" >> "$NET_CONF"
  fi
  
  # Enable networkd if the guest supports it
  if chroot "${ROOTFS}" command -v systemctl >/dev/null; then
      chroot "${ROOTFS}" systemctl enable systemd-networkd || true
  fi
