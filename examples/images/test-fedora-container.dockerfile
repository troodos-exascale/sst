# CANONICAL_NAME: fedora-43-container-test-fedora-container
# LAST_BUILD: 20260321-160911
FROM fedora:latest
NAME: test-fedora-container

# EMIT a dummy format so mkimg doesn't default to 'raw' and trigger qemu-img disk creation
EMIT: container

# Drop into the extracted root filesystem using systemd-nspawn
RUN [host]: |
  set -e
  
  # Ensure the container engine is installed on the build host (ub)
  if ! command -v systemd-nspawn >/dev/null 2>&1; then
    echo "--> Installing systemd-container on host..."
    apt-get update && apt-get install -y systemd-container
  fi
  
  echo "======================================================="
  echo "🚀 Dropping into interactive Fedora container!"
  echo "   Type 'exit' to cleanly finish the mkimg pipeline."
  echo "======================================================="
  
  # -q: Quiet (suppress nspawn boot logs)
  # -D: Define the root directory of the container
  # --as-pid2: Run bash as PID 2 (with a dummy stub init as PID 1 to handle signals properly)
  systemd-nspawn -q -D "${ROOTFS}" --as-pid2 /bin/bash
