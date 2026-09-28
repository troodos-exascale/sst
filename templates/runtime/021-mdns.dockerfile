RUN [host]: |
  set -e
  echo "--> Configuring mDNS (Avahi) for ${INST_HOSTNAME}.local ..."
  
  # Inject avahi if the base image lacks it
  if ! chroot "${ROOTFS}" command -v avahi-daemon >/dev/null; then
      export DEBIAN_FRONTEND=noninteractive
      chroot "${ROOTFS}" apt-get update
      chroot "${ROOTFS}" apt-get install -y avahi-daemon libnss-mdns
  fi
  
  AVAHI_CONF="${ROOTFS}/etc/avahi/avahi-daemon.conf"
  if [ -f "$AVAHI_CONF" ]; then
      sed -i "s/.*host-name=.*/host-name=${INST_HOSTNAME}/" "$AVAHI_CONF"
  fi
  
  if chroot "${ROOTFS}" command -v systemctl >/dev/null; then
      chroot "${ROOTFS}" systemctl enable avahi-daemon || true
  fi
