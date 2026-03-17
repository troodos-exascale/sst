FROM alpine:3.22

RUN: |
  apk update

# Hydrate the OS for NoCloud environments
INCLUDE alpine-nocloud-bootstrap.dockerfile

# Assemble the physical disk
INCLUDE boot-efi.dockerfile

# Trigger the desugared emit templates
EMIT: raw, kernel, qcow2
