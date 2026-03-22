# CANONICAL_NAME: alpine-3.23.3-test-alpine-net
# LAST_BUILD: 20260321-192338
FROM alpine:latest
NAME: test-alpine-net

RUN: |
  apk update && apk add curl bind-tools

# EMIT: container prevents the generation of a .raw disk, 
# triggering the physical ZFS rootfs promotion in FinalizeVM
EMIT: container
