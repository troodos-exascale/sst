FROM scratch
NAME: alpine-clone-test

# Pull a small Alpine QCOW2 cloud image
ENV CLONE_SOURCE="https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/aarch64/alpine-virt-3.21.3-aarch64.qcow2"
ENV CLONE_NAME="alpine-clone-test"

# Dispatch to our new declarative template
INCLUDE clone.dockerfile
