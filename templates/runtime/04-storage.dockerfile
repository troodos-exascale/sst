RUN [host]: |
  set -e
  echo "--> Provisioning storage mounts..."
  if [ -n "${INST_SHARE_HOST_DIRS}" ]; then
      # Split by comma
      IFS=',' read -r -a shares <<< "${INST_SHARE_HOST_DIRS}"
      for share in "${shares[@]}"; do
          # Split by colon
          IFS=':' read -r host_path guest_path <<< "$share"
          if [ -n "$guest_path" ]; then
              mkdir -p "${ROOTFS}${guest_path}"
              echo "${host_path} ${guest_path} virtiofs defaults 0 0" >> "${ROOTFS}/etc/fstab"
          fi
      done
  fi
