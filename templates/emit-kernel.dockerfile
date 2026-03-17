RUN [host]: |
  set -e
  echo "--> Extracting kernel and initrd from rootfs for direct boot..."
  
  KFILE=$(find "${ROOTFS}/boot" -maxdepth 1 -name "vmlinu*" | head -n 1 || true)
  IFILE=$(find "${ROOTFS}/boot" -maxdepth 1 -name "initramfs*" | head -n 1 || true)
  
  if [ -n "$KFILE" ] && [ -f "$KFILE" ]; then
    echo "    Uncompressing kernel using mkimg extract_kernel..."
    mkimg extract_kernel "$KFILE" "${OUTDIR}/kernel"
  else
    echo "    No kernel found in ${ROOTFS}/boot. Skipping kernel extraction."
  fi
  
  if [ -n "$IFILE" ] && [ -f "$IFILE" ]; then
    echo "    Copying initrd..."
    cp "$IFILE" "${OUTDIR}/initrd"
  fi
