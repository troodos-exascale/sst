RUN [base, host, raw, qcow2]: |
  set -e
  IMG="${OUTDIR}/${NAME}.raw"
  MNT="${BOOTDIR}"
  mkdir -p "${OUTDIR}"
  
  echo "--> Assembling 2G EFI Disk: $IMG"
  rm -f "$IMG"
  truncate -s 2G "$IMG"
  sgdisk -Z "$IMG" || true
  sgdisk -n 1:0:+200M -t 1:ef00 "$IMG"
  sgdisk -n 2:0:0 -t 2:8300 "$IMG"
  
  LOOP=$(losetup -f -P --show "$IMG")
  trap 'umount -l "$MNT/dev" "$MNT/proc" "$MNT/sys" 2>/dev/null || true; umount -R "$MNT" 2>/dev/null || true; losetup -d "$LOOP" 2>/dev/null || true' EXIT
  
  echo "--> Formatting partitions..."
  mkfs.vfat -F32 "${LOOP}p1"
  mkfs.ext4 "${LOOP}p2"
  
  echo "--> Extracting Hardware IDs and Sizes..."
  BOOT_PART_UUID=$(blkid -s PARTUUID -o value "${LOOP}p1")
  BOOT_FS_UUID=$(blkid -s UUID -o value "${LOOP}p1")
  ROOT_PART_UUID=$(blkid -s PARTUUID -o value "${LOOP}p2")
  ROOT_FS_UUID=$(blkid -s UUID -o value "${LOOP}p2")
  ROOT_SIZE=$(blockdev --getsize64 "${LOOP}p2")
  
  echo "--> Mounting and syncing rootfs..."
  mkdir -p "$MNT"
  mount "${LOOP}p2" "$MNT"
  mkdir -p "$MNT/boot/efi"
  mount "${LOOP}p1" "$MNT/boot/efi"
  rsync -aHAX "${ROOTFS}/" "$MNT/"
  
  echo "--> Writing /etc/fstab..."
  echo "/dev/vda2 / ext4 rw,relatime 0 1" > "$MNT/etc/fstab"
  echo "/dev/vda1 /boot/efi vfat defaults 0 2" >> "$MNT/etc/fstab"
  
  echo "--> Preparing chroot for bootloader..."
  for m in dev proc sys; do mount --bind /$m "$MNT/$m"; done
  
  echo "--> Installing GRUB EFI for ARM64..."
  chroot "$MNT" /bin/sh -c "grub-install --target=arm64-efi --efi-directory=/boot/efi --removable"
  chroot "$MNT" /bin/sh -c "grub-mkconfig -o /boot/grub/grub.cfg"
  
  echo "--> Emitting strictly compliant manifest..."
  echo "name: ${NAME}" > "${OUTDIR}/image.yaml"
  echo "raw_image: ${NAME}.raw" >> "${OUTDIR}/image.yaml"
  echo "format: raw" >> "${OUTDIR}/image.yaml"
  echo "bootfs_device: /dev/vda1" >> "${OUTDIR}/image.yaml"
  echo "bootfs_is_last: false" >> "${OUTDIR}/image.yaml"
  echo "bootfs_part_uuid: ${BOOT_PART_UUID}" >> "${OUTDIR}/image.yaml"
  echo "bootfs_type: vfat" >> "${OUTDIR}/image.yaml"
  echo "bootfs_uuid: ${BOOT_FS_UUID}" >> "${OUTDIR}/image.yaml"
  echo "rootfs_device: /dev/vda2" >> "${OUTDIR}/image.yaml"
  echo "rootfs_is_last: true" >> "${OUTDIR}/image.yaml"
  echo "rootfs_part_uuid: ${ROOT_PART_UUID}" >> "${OUTDIR}/image.yaml"
  echo "rootfs_size: ${ROOT_SIZE}B" >> "${OUTDIR}/image.yaml"
  echo "rootfs_type: ext4" >> "${OUTDIR}/image.yaml"
  echo "rootfs_uuid: ${ROOT_FS_UUID}" >> "${OUTDIR}/image.yaml"
