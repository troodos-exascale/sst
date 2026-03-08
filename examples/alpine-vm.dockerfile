FROM alpine:3.22
NAME: alpine-vm
EMIT: raw, qcow2

RUN: |
  apk update

# Hydrate the OS for NoCloud environments (Networking, user access, resizing, acpid)
INCLUDE alpine-nocloud-bootstrap.dockerfile

# Assemble the physical disk and emit the manifests
INCLUDE boot-efi.dockerfile

# Extract kernel natively via Go and append to manifest
INCLUDE emit-kernel.dockerfile

# Convert the raw disk to qcow2 format
INCLUDE emit-qcow2.dockerfile
