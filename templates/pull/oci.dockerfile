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
  
  find "${ROOTFS}" -xdev -exec chown -h 0:0 {} +
  find "${ROOTFS}" -xdev -exec touch -h -d @0 {} +

  echo "--> Injecting SST Networking Sidecar..."
  mkdir -p "${ROOTFS}/.sst"
  cat << 'EOF' > "${ROOTFS}/.sst/init"
  #!/bin/sh
  # Provide basic networking for daemonless OCI containers
  if command -v u-root >/dev/null 2>&1; then
      u-root dhclient eth0 &
  elif command -v udhcpc >/dev/null 2>&1; then
      udhcpc -i eth0 -q &
  elif command -v dhclient >/dev/null 2>&1; then
      dhclient eth0 &
  fi
  # Hand over execution to the container's primary CMD
  exec "$@"
  EOF
  chmod +x "${ROOTFS}/.sst/init"
