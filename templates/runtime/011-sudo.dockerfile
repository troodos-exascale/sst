RUN [host]: |
  set -e
  if [ -z "${INST_USER}" ]; then exit 0; fi
  echo "--> Granting passwordless sudo to '${INST_USER}'..."
  
  chroot "${ROOTFS}" usermod -aG sudo "${INST_USER}" 2>/dev/null || true
  echo "${INST_USER} ALL=(ALL) NOPASSWD:ALL" > "${ROOTFS}/etc/sudoers.d/99-${INST_USER}-nopasswd"
  chmod 0440 "${ROOTFS}/etc/sudoers.d/99-${INST_USER}-nopasswd"
