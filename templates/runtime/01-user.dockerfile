RUN [host]: |
  set -e
  if [ -z "${INST_USER}" ]; then exit 0; fi
  echo "--> Provisioning user '${INST_USER}'..."
  
  chroot "${ROOTFS}" id -u "${INST_USER}" &>/dev/null || chroot "${ROOTFS}" useradd -m -s /bin/bash "${INST_USER}"
  
  if [ -n "${INST_PUBKEY_HOSTFILE}" ]; then
      KEY_FILE=$(eval echo "${INST_PUBKEY_HOSTFILE}")
      if [ ! -f "$KEY_FILE" ]; then KEY_FILE="${CONTEXT_DIR}/${INST_PUBKEY_HOSTFILE}"; fi
      
      if [ -f "$KEY_FILE" ]; then
          GUEST_SSH="${ROOTFS}/home/${INST_USER}/.ssh"
          mkdir -p "$GUEST_SSH"
          cat "$KEY_FILE" > "${GUEST_SSH}/authorized_keys"
          
          GUEST_UID=$(chroot "${ROOTFS}" id -u "${INST_USER}")
          GUEST_GID=$(chroot "${ROOTFS}" id -g "${INST_USER}")
          chown -R "${GUEST_UID}:${GUEST_GID}" "$GUEST_SSH"
          chmod 600 "${GUEST_SSH}/authorized_keys"
      else
          echo "ERROR: SSH Pubkey file not found at evaluated path: ${KEY_FILE}" >&2
          exit 1
      fi
  fi
