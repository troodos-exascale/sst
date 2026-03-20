RUN [host]: |
  set -e
  echo "--> Extracting kernel and initrd from rootfs for direct boot..."
  
  KFILE=$(find "${ROOTFS}/boot" -maxdepth 1 \( -name "vmlinu*" -o -name "bzImage" \) | head -n 1 || true)
  IFILE=$(find "${ROOTFS}/boot" -maxdepth 1 \( -name "initramfs*" -o -name "initrd*" \) | head -n 1 || true)
  
  if [ -n "$KFILE" ] && [ -e "$KFILE" ]; then
    echo "    Uncompressing kernel using mkimg extract_kernel..."
    mkimg extract_kernel "$KFILE" "${OUTDIR}/kernel"
  else
    echo "    No kernel found in ${ROOTFS}/boot. Skipping kernel extraction."
  fi
  
  if [ -n "$IFILE" ] && [ -e "$IFILE" ]; then
    echo "    Copying initrd..."
    # Use -L to dereference symlinks (Ubuntu often uses /boot/initrd.img -> initrd.img-X.Y.Z)
    cp -L "$IFILE" "${OUTDIR}/initrd"
  fi
