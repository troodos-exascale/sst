RUN [base, host, qcow2]: |
  set -e
  IMG="${OUTDIR}/${NAME}.raw"
  QCOW="${OUTDIR}/${NAME}.qcow2"
  mkdir -p "${OUTDIR}"
  echo "--> Converting to QCOW2..."
  qemu-img convert -f raw -O qcow2 "$IMG" "$QCOW"
