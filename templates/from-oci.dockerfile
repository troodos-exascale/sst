RUN [host]: |
  set -e
  if [ "${BASE_IMAGE}" = "scratch" ] || [ -z "${BASE_IMAGE}" ]; then
    echo "--> Base image is scratch, skipping OCI fetch."
    exit 0
  fi
  
  # Determine host architecture
  ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
  
  echo "--> Resolving strict OCI digest for linux/${ARCH}..."
  DIGEST=$(crane digest --platform "linux/${ARCH}" "${BASE_IMAGE}")
  
  echo "--> Fetching OCI base image via immutable digest: ${DIGEST}"
  mkdir -p "${ROOTFS}"
  
  # Export the flattened rootfs using the exact architecture digest!
  # (crane safely ignores the buggy platform flags when given a strict digest)
  crane export "${BASE_IMAGE}@${DIGEST}" "/tmp/base_flat_${NAME}.tar"
  
  echo "--> Extracting filesystem and enforcing deterministic metadata..."
  tar --numeric-owner -C "${ROOTFS}" -xf "/tmp/base_flat_${NAME}.tar"
  
  # MATHEMATICAL REPRODUCIBILITY CLAMPS
  # Clamp all ownership to root:root (UID 0 / GID 0) without following symlinks (-h)
  find "${ROOTFS}" -exec chown -h 0:0 {} +
  
  # Clamp all timestamps to UNIX Epoch 0 (1970-01-01 00:00:00 UTC) without following symlinks (-h)
  find "${ROOTFS}" -exec touch -h -d @0 {} +
  
  # Cleanup
  rm -f "/tmp/base_flat_${NAME}.tar"
