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
  if [ -z "${INST_USER}" ]; then exit 0; fi
  echo "--> Granting passwordless sudo to '${INST_USER}'..."
  
  chroot "${ROOTFS}" usermod -aG sudo "${INST_USER}" 2>/dev/null || true
  echo "${INST_USER} ALL=(ALL) NOPASSWD:ALL" > "${ROOTFS}/etc/sudoers.d/99-${INST_USER}-nopasswd"
  chmod 0440 "${ROOTFS}/etc/sudoers.d/99-${INST_USER}-nopasswd"
