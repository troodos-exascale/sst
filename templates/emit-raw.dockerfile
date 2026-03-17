RUN [base, host, raw, qcow2]: |
  set -e
  IMG="${OUTDIR}/${NAME}.raw"
  mkdir -p "${OUTDIR}"
  
  if [ ! -f "$IMG" ]; then
     echo "--> Assembling default RAW Disk: $IMG"
     truncate -s 4G "$IMG"
     # Format with deterministic UUID so NBD extraction can reliably map it
     mke2fs -t ext4 -d "${ROOTFS}" -U "12345678-1234-1234-1234-123456789abc" -E "hash_seed=12345678-1234-1234-1234-123456789abc" -F "$IMG" > /dev/null 2>&1
  else
     echo "--> RAW Disk already assembled by previous template."
  fi
