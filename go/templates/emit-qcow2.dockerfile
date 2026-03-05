RUN [base, host, qcow2]: |
  set -e
  IMG="${BASEDIR}/images/${NAME}/${NAME}.raw"
  QCOW="${BASEDIR}/images/${NAME}/${NAME}.qcow2"
  echo "--> Converting to QCOW2..."
  qemu-img convert -f raw -O qcow2 "$IMG" "$QCOW"
