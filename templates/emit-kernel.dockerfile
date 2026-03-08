RUN [base, host, raw, qcow2]: |
  set -e
  echo "--> Extracting kernel and initrd from rootfs for direct boot..."
  
  KFILE=$(find "${ROOTFS}/boot" -maxdepth 1 -name "vmlinu*" | head -n 1)
  IFILE=$(find "${ROOTFS}/boot" -maxdepth 1 -name "initramfs*" | head -n 1)
  
  if [ -n "$KFILE" ] && [ -n "$IFILE" ]; then
    echo "    Uncompressing kernel using mkimg extract_kernel..."
    # Call the native Go tool directly from the host environment
    mkimg extract_kernel "$KFILE" "${OUTDIR}/kernel"
    
    echo "    Copying initrd..."
    cp "$IFILE" "${OUTDIR}/initrd"
    
    echo "--> Appending kernel paths to manifest..."
    echo "kernel: kernel" >> "${OUTDIR}/image.yaml"
    echo "initrd: initrd" >> "${OUTDIR}/image.yaml"
    
    echo "    Saved: uncompressed kernel and $(basename "$IFILE")"
  else
    echo "ERROR: Kernel or initrd not found in ${ROOTFS}/boot!"
    exit 1
  fi
