# Executes on the host to assemble a standard EFI + Root partitioned disk
RUN [base, host, raw, qcow2]: |
  set -e
  IMG="${BASEDIR}/images/${NAME}/${NAME}.raw"
  MNT="${BOOTDIR}"
  mkdir -p "$(dirname "$IMG")"
  
  echo "--> Assembling 2G EFI Disk: $IMG"
  truncate -s 2G "$IMG"
  sgdisk -n 1:0:+200M -t 1:ef00 "$IMG"
  sgdisk -n 2:0:0 -t 2:8300 "$IMG"
  
  LOOP=$(losetup -f -P --show "$IMG")
  # Ensure strict cleanup regardless of success/failure
  trap 'umount -l "$MNT/dev" "$MNT/proc" "$MNT/sys" 2>/dev/null || true; umount -R "$MNT" 2>/dev/null || true; losetup -d "$LOOP" 2>/dev/null || true' EXIT
  
  echo "--> Formatting partitions..."
  mkfs.vfat -F32 "${LOOP}p1"
  mkfs.ext4 "${LOOP}p2"
  
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
  
  echo "--> Installing GRUB EFI..."
  chroot "$MNT" /bin/sh -c "grub-install --target=x86_64-efi --efi-directory=/boot/efi --removable"
  chroot "$MNT" /bin/sh -c "grub-mkconfig -o /boot/grub/grub.cfg"
  
  echo "--> Emitting manifest..."
  cat <<EOF > "${BASEDIR}/images/${NAME}/image.yaml"
  name: ${NAME}
  raw_image: ${NAME}.raw
  EOF
