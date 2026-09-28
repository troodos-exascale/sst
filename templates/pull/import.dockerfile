FROM scratch
NAME: ${IMPORT_NAME}

# 1. Ingest the payload (ADD inherently supports both HTTP URLs and Local absolute paths)
ADD ${IMPORT_SOURCE} /source_payload

# 2. Dynamically normalize the payload into standard RAW format
RUN [host]: |
  set -e
  echo "--> Analyzing imported payload..."
  
  MIME=$(file -b --mime-type "${ROOTFS}/source_payload" || true)
  INFO=$(qemu-img info "${ROOTFS}/source_payload" | grep "file format" || true)
  
  if echo "$INFO" | grep -q "qcow2"; then
      echo "--> QCOW2 detected. Converting to sparse RAW..."
      qemu-img convert -f qcow2 -O raw "${ROOTFS}/source_payload" "${ROOTFS}/${IMPORT_NAME}.raw"
      rm -f "${ROOTFS}/source_payload"
      
  elif echo "$MIME" | grep -q "application/x-tar" || echo "$MIME" | grep -q "gzip"; then
      echo "--> Archive detected. Extracting..."
      mkdir -p "${ROOTFS}/extracted"
      tar -xf "${ROOTFS}/source_payload" -C "${ROOTFS}/extracted"
      rm -f "${ROOTFS}/source_payload"
      
      # Heuristic: Find the largest file inside the tar and promote it to the RAW disk
      LARGEST=$(find "${ROOTFS}/extracted" -type f -exec ls -s {} + | sort -n | tail -1 | awk '{print $2}')
      if [ -n "$LARGEST" ]; then
          mv "$LARGEST" "${ROOTFS}/${IMPORT_NAME}.raw"
      else
          echo "FATAL: No files found in archive."
          exit 1
      fi
      rm -rf "${ROOTFS}/extracted"
      
  else
      echo "--> Assuming RAW disk format..."
      mv "${ROOTFS}/source_payload" "${ROOTFS}/${IMPORT_NAME}.raw"
  fi

# 3. Emit triggers FinalizeVM which natively runs AnalyseImg + ExtractArtifacts on the resulting disk!
EMIT: raw
