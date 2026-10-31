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

FROM scratch
NAME: alpine-scratch

# REQUIRED: Declare the argument so the Go engine is allowed to inject it!
ARG ALPINE_ARCH

# 1. Bootstrap the root filesystem from scratch using the host context
GET https://dl-cdn.alpinelinux.org/alpine/v3.22/releases/${ALPINE_ARCH}/alpine-minirootfs-3.22.0-${ALPINE_ARCH}.tar.gz /alpine-rootfs.tar.gz

RUN [host]: |
  set -e
  echo "--> Extracting cached Alpine Linux into ${ROOTFS}..."
  
  # Extract the payload injected by the GET instruction
  tar -xzf "${ROOTFS}/alpine-rootfs.tar.gz" -C "${ROOTFS}"
  rm -f "${ROOTFS}/alpine-rootfs.tar.gz"

# 2. The Modular OS Stack
# Hydrate the OS for NoCloud environments (LXC/Base)
INCLUDE alpine/nocloud-init.dockerfile

# Install hardware kernel (for direct VZ/QEMU boot)
INCLUDE alpine/kernel.dockerfile

# Assemble the physical disk and install GRUB (which internally calls common/boot-efi)
INCLUDE alpine/efiboot.dockerfile

# 3. Trigger the desugared emit templates to extract the kernel and build the QCOW2
EMIT: raw, kernel, qcow2
