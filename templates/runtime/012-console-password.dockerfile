RUN [host]: |
  set -e
  if [ -z "${INST_USER}" ] || [ -z "${INST_CONSOLE_PASS_HOSTFILE}" ]; then exit 0; fi
  echo "--> Setting console password for '${INST_USER}'..."
  
  PASS_FILE=$(eval echo "${INST_CONSOLE_PASS_HOSTFILE}")
  if [ ! -f "$PASS_FILE" ]; then PASS_FILE="${CONTEXT_DIR}/${INST_CONSOLE_PASS_HOSTFILE}"; fi
  
  if [ -f "$PASS_FILE" ]; then
      PASS=$(cat "$PASS_FILE")
      echo "${INST_USER}:${PASS}" | chroot "${ROOTFS}" chpasswd
  else
      echo "ERROR: Console password file not found at evaluated path: ${PASS_FILE}" >&2
      exit 1
  fi
