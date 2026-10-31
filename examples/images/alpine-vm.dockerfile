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

# CANONICAL_NAME: alpine-3.22.4-alpine-vm
# LAST_BUILD: 20260604-070740
FROM alpine:3.22
NAME: alpine-vm

RUN: |
  apk update

# The Modular OS Stack
# Hydrate the OS for NoCloud environments (LXC/Base)
INCLUDE alpine/nocloud-init.dockerfile

# Install hardware kernel (for direct VZ/QEMU boot)
INCLUDE alpine/kernel.dockerfile

# Assemble the physical disk and install GRUB (which internally calls common/boot-efi)
INCLUDE alpine/efiboot.dockerfile

# Trigger the desugared emit templates
EMIT: raw, kernel, qcow2
