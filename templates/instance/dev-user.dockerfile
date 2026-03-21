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
  
  INST_DIR="${RUNTIMES_DIR}/instances/${INST_NAME}"
  rm -rf "${INST_DIR}"
  mkdir -p "${INST_DIR}"
  
  # NEW: Parse the image's "superblock" (image.yaml) to get the exact raw image filename
  RAW_IMAGE=$(awk '/raw_image:/ {print $2}' "${SRC_DIR}/image.yaml" | tr -d '"')
  if [ -z "$RAW_IMAGE" ]; then
    echo "Error: Could not determine raw_image from ${SRC_DIR}/image.yaml"
    exit 1
  fi

  echo "--> Copying disk (sparse/clone)..."
  if [ "$(uname)" = "Darwin" ]; then
    cp -c "${SRC_DIR}/${RAW_IMAGE}" "${INST_DIR}/${INST_NAME}.raw" || cp "${SRC_DIR}/${RAW_IMAGE}" "${INST_DIR}/${INST_NAME}.raw"
  else
    cp --sparse=always "${SRC_DIR}/${RAW_IMAGE}" "${INST_DIR}/${INST_NAME}.raw"
  fi
  
  # Resolve symlinks so the VM gets physical kernels instead of broken references
  if [ -f "${SRC_DIR}/kernel" ]; then cp -L "${SRC_DIR}/kernel" "${INST_DIR}/kernel"; fi
  if [ -f "${SRC_DIR}/initrd" ]; then cp -L "${SRC_DIR}/initrd" "${INST_DIR}/initrd"; fi
  
  echo "--> Generating cloud-init (cidata)..."
  mkdir -p "${INST_DIR}/cidata"
  
  (
    echo "#cloud-config"
    echo "hostname: ${INST_NAME}"
    echo "local-hostname: ${INST_NAME}"
  ) > "${INST_DIR}/cidata/user-data"
  
  (
    echo "instance-id: iid-${INST_NAME}"
    echo "local-hostname: ${INST_NAME}"
  ) > "${INST_DIR}/cidata/meta-data"
  
  if command -v hdiutil >/dev/null 2>&1; then
    hdiutil makehybrid -o "${INST_DIR}/cidata.iso" "${INST_DIR}/cidata" -joliet -iso -default-volume-name cidata >/dev/null
  else
    genisoimage -output "${INST_DIR}/cidata.iso" -volid cidata -joliet -rock "${INST_DIR}/cidata" >/dev/null 2>&1
    
    # Pad the ISO to a 4096-byte boundary to appease macOS VZ block device requirements
    SIZE=$(stat -c%s "${INST_DIR}/cidata.iso")
    REM=$((SIZE % 4096))
    if [ $REM -ne 0 ]; then
      truncate -s +$((4096 - REM)) "${INST_DIR}/cidata.iso"
    fi
  fi
