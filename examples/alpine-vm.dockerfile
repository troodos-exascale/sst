# CANONICAL_NAME: alpine-3.22.3-alpine-vm
# LAST_BUILD: 20260322-103520
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
