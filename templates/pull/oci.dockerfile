RUN [$TARGET_STAGE, host]: |
  set -e
  if [ "${FROM_SOURCE}" = "scratch" ] || [ -z "${FROM_SOURCE}" ]; then
    echo "--> Base image is scratch, skipping OCI fetch."
    exit 0
  fi
  
  ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
  echo "--> Resolving strict OCI digest for linux/${ARCH}..."
  DIGEST=$(crane digest --platform "linux/${ARCH}" "${FROM_SOURCE}")
  
  SAFE_NAME=$(echo "${FROM_SOURCE}@${DIGEST}" | tr ':/@' '_')
  CACHE_TAR="${SST_CACHE_FROM}/${SAFE_NAME}.tar"
  
  if [ ! -f "$CACHE_TAR" ]; then
    echo "--> FROM cache miss: fetching immutable digest..."
    crane export "${FROM_SOURCE}@${DIGEST}" "$CACHE_TAR"
  else
    echo "--> FROM cache hit: ${CACHE_TAR}"
  fi
  
  echo "--> Extracting filesystem to ${ROOTFS}..."
  mkdir -p "${ROOTFS}"
  tar --numeric-owner -C "${ROOTFS}" -xf "$CACHE_TAR"
  
  # MATHEMATICAL REPRODUCIBILITY CLAMPS
  # Clamp all ownership to root:root (UID 0 / GID 0) without following symlinks (-h)
  # -xdev prevents crawling into active bind-mounts (like /proc or /dev) from previous crashed runs
  find "${ROOTFS}" -xdev -exec chown -h 0:0 {} +
  
  # Clamp all timestamps to UNIX Epoch 0 (1970-01-01 00:00:00 UTC)
  find "${ROOTFS}" -xdev -exec touch -h -d @0 {} +
