RUN [host]: |
  set -e
  echo "--> Applying standard hardening..."
  SSHD_CONF="${ROOTFS}/etc/ssh/sshd_config"
  if [ -f "$SSHD_CONF" ]; then
      sed -i 's/#PermitRootLogin.*/PermitRootLogin no/' "$SSHD_CONF" || true
      sed -i 's/#PasswordAuthentication.*/PasswordAuthentication no/' "$SSHD_CONF" || true
  fi
