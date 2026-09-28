FROM scratch

RUN [host]: |
  set -e
  echo "--> Instantiating ${INST_NAME} from ${BASE_IMAGE}"
  
  IMAGES_DIR="$(dirname "${OUTDIR}")"
  RUNTIMES_DIR="$(dirname "${IMAGES_DIR}")"
  SRC_DIR="${IMAGES_DIR}/${BASE_IMAGE}"
  
  if [ ! -d "${SRC_DIR}" ]; then
    echo "Error: Image ${BASE_IMAGE} not found at ${SRC_DIR}"
    exit 1
  fi
  
  # Resolve any global symlinks safely
  SRC_DIR=$(cd "${SRC_DIR}" && pwd -P)
  
  INST_DIR="${RUNTIMES_DIR}/instances/${INST_NAME}"
  rm -rf "${INST_DIR}"
  mkdir -p "${INST_DIR}"
  
  RAW_IMAGE=$(awk '/raw_image:/ {print $2}' "${SRC_DIR}/image.yaml" | tr -d '"' || true)
  
  if [ -z "$RAW_IMAGE" ] || [ "$RAW_IMAGE" = "null" ]; then
      echo "--> Container format detected. Linking ZFS topology..."
      
      if command -v zfs >/dev/null 2>&1 && zfs list -H -o name "${SRC_DIR}/rootfs" >/dev/null 2>&1; then
          SRC_DS=$(zfs list -H -o name "${SRC_DIR}/rootfs")
          SNAP_NAME="${SRC_DS}@inst-${INST_NAME}-$(date +%s)"
          
          echo "    Taking snapshot: ${SNAP_NAME}"
          zfs snapshot "$SNAP_NAME" || true
          
          INST_DS="$(echo "$SRC_DS" | sed "s|/images/|/instances/|")-${INST_NAME}"
          echo "    Cloning dataset to: ${INST_DIR}/rootfs"
          zfs clone "$SNAP_NAME" "$INST_DS" -o mountpoint="${INST_DIR}/rootfs"
      else
          echo "    (ZFS not detected, falling back to copy-on-write clone)"
          mkdir -p "${INST_DIR}/rootfs"
          cp -a --reflink=auto "${SRC_DIR}/rootfs/"* "${INST_DIR}/rootfs/" 2>/dev/null || cp -a "${SRC_DIR}/rootfs/"* "${INST_DIR}/rootfs/"
      fi
  else
      echo "--> VM format detected. Copying block devices..."
  if [ -f "${SRC_DIR}/${RAW_IMAGE}" ]; then
      if [ "$(uname)" = "Darwin" ]; then
        cp -c "${SRC_DIR}/${RAW_IMAGE}" "${INST_DIR}/${INST_NAME}.raw" || cp "${SRC_DIR}/${RAW_IMAGE}" "${INST_DIR}/${INST_NAME}.raw"
      else
        cp --sparse=always "${SRC_DIR}/${RAW_IMAGE}" "${INST_DIR}/${INST_NAME}.raw"
      fi
  else
    echo "    (MicroVM detected: No physical raw image found, skipping block device copy)"
  fi
      
      if [ -f "${SRC_DIR}/kernel" ]; then cp -L "${SRC_DIR}/kernel" "${INST_DIR}/kernel"; fi
      if [ -f "${SRC_DIR}/initrd" ]; then cp -L "${SRC_DIR}/initrd" "${INST_DIR}/initrd"; fi
      
      echo "--> Provisioning 30GB sparse data disk for ZFS..."
      truncate -s 30G "${INST_DIR}/data.raw"
  fi
