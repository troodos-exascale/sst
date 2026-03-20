FROM scratch
ARG URL
ARG PULL_NAME
NAME: ${PULL_NAME}

RUN [host]: |
  set -e
  echo "--> Processing artifact: ${URL}"
  mkdir -p "${OUTDIR}"
  
  # Determine if remote or local
  IS_REMOTE=0
  if [[ "${URL}" == http://* ]] || [[ "${URL}" == https://* ]]; then
    IS_REMOTE=1
    HASH=$(echo -n "${URL}" | sha256sum | awk '{print $1}')
    DL_PATH="${SST_CACHE_GET}/${HASH}"
    
    if [ ! -f "$DL_PATH" ]; then
       echo "--> Cache miss: downloading upstream VM image..."
       curl -fsSL "${URL}" -o "${DL_PATH}"
    else
       echo "--> Cache hit for upstream VM image!"
    fi
  else
    DL_PATH="${URL}"
    if [ ! -f "${DL_PATH}" ]; then
      echo "Error: Local file not found: ${DL_PATH}"
      exit 1
    fi
  fi
  
  # Determine extension to select strategy
  FILENAME=$(basename "${URL}")
  EXT="${FILENAME##*.}"
  if [[ "${FILENAME}" == *.tar.gz ]]; then EXT="tar.gz"; fi
  
  RAW_IMG="${OUTDIR}/${NAME}.raw"
  
  case "${EXT}" in
    tar|tar.gz|tgz|xz)
      echo "--> Detected filesystem archive (${EXT}). Extracting to rootfs..."
      mkdir -p "${ROOTFS}"
      tar -xf "${DL_PATH}" -C "${ROOTFS}"
      # emit-raw.dockerfile will automatically package this ROOTFS into a disk!
      ;;
    qcow2|img|raw)
      echo "--> Detected block device image (${EXT}). Converting to raw..."
      qemu-img convert -O raw "${DL_PATH}" "${RAW_IMG}"
      
      echo "--> Resizing root to 10G..."
      truncate -s 10G "${RAW_IMG}"
      LOOP=$(losetup -f -P --show "${RAW_IMG}")
      growpart "$LOOP" 1 || true
      partprobe "$LOOP" || true
      udevadm settle || true
      e2fsck -f -p "${LOOP}p1" || true
      resize2fs "${LOOP}p1" || true
      losetup -d "$LOOP" || true
      ;;
    *)
      echo "--> Unknown extension '${EXT}'. Attempting block device conversion..."
      qemu-img convert -O raw "${DL_PATH}" "${RAW_IMG}"
      ;;
  esac
  
EMIT: raw, qcow2, kernel
