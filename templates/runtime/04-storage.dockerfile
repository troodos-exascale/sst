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
  echo "--> Provisioning storage mounts..."
  if [ -n "${INST_SHARE_HOST_DIRS}" ]; then
      # Split by comma
      IFS=',' read -r -a shares <<< "${INST_SHARE_HOST_DIRS}"
      for share in "${shares[@]}"; do
          # Split by colon
          IFS=':' read -r host_path guest_path <<< "$share"
          if [ -n "$guest_path" ]; then
              mkdir -p "${ROOTFS}${guest_path}"
              echo "${host_path} ${guest_path} virtiofs defaults 0 0" >> "${ROOTFS}/etc/fstab"
          fi
      done
  fi
