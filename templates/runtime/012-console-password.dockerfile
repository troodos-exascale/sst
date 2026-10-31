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
  if [ -z "${INST_USER}" ] || [ -z "${INST_CONSOLE_PASS_HOSTFILE}" ]; then exit 0; fi
  echo "--> Setting console password for '${INST_USER}'..."
  
  PASS_FILE=$(eval echo "${INST_CONSOLE_PASS_HOSTFILE}")
  if [ ! -f "$PASS_FILE" ]; then PASS_FILE="${CONTEXT_DIR}/${INST_CONSOLE_PASS_HOSTFILE}"; fi
  
  if [ -f "$PASS_FILE" ]; then
      PASS=$(cat "$PASS_FILE")
      echo "${INST_USER}:${PASS}" | chroot "${ROOTFS}" chpasswd
  else
      echo "ERROR: Console password file not found at evaluated path: ${PASS_FILE}" >&2
      exit 1
  fi
