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

# CANONICAL_NAME: ubuntu-24.04-built-from-scratch-final
# LAST_BUILD: 20260430-013607
FROM scratch
NAME built-from-scratch-final

# REQUIRED: Declare the argument so the Go engine is allowed to inject it!
ARG DEB_ARCH

# 1. Bootstrap the root filesystem from Canonical's Ubuntu Base tarball
GET http://cdimage.ubuntu.com/ubuntu-base/releases/24.04/release/ubuntu-base-24.04.4-base-${DEB_ARCH}.tar.gz /ubuntu-base.tar.gz

RUN [host]: |
  set -e
  echo "--> Extracting cached Ubuntu Noble (24.04) into ${ROOTFS}..."
  
  # Extract the payload injected by the GET instruction
  tar -xzf "${ROOTFS}/ubuntu-base.tar.gz" -C "${ROOTFS}"
  rm -f "${ROOTFS}/ubuntu-base.tar.gz"

# 2. Hydrate the OS for NoCloud VM environments
INCLUDE ubuntu-nocloud-bootstrap.dockerfile

# 3. Assemble the physical disk and install GRUB
INCLUDE boot-efi.dockerfile

# 4. Trigger the desugared emit templates to extract the kernel and build the QCOW2
EMIT: raw, kernel, qcow2
