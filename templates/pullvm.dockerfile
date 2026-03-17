FROM scratch
ARG URL
ARG PULL_NAME
NAME: ${PULL_NAME}

RUN [host]: |
  set -e
  echo "--> Pulling upstream image: ${URL}"
  mkdir -p "${OUTDIR}"
  
  DL_PATH="/tmp/upstream_dl_${PULL_NAME}"
  if [[ "${URL}" == http://* ]] || [[ "${URL}" == https://* ]]; then
    curl -sSL "${URL}" -o "${DL_PATH}"
  else
    cp "${URL}" "${DL_PATH}"
  fi
  
  RAW_IMG="${OUTDIR}/${NAME}.raw"
  echo "--> Converting to raw..."
  qemu-img convert -O raw "${DL_PATH}" "${RAW_IMG}"
  rm -f "${DL_PATH}"
  
  echo "--> Resizing root to 10G..."
  truncate -s 10G "${RAW_IMG}"
  LOOP=$(losetup -f -P --show "${RAW_IMG}")
  growpart "$LOOP" 1 || true
  partprobe "$LOOP" || true
  udevadm settle || true
  e2fsck -f -p "${LOOP}p1" || true
  resize2fs "${LOOP}p1" || true
  losetup -d "$LOOP" || true

EMIT: raw
