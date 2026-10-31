# Copyright 2026 Peter J. Braam
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

FROM alpine:3.22 AS toolchain
# In v0.2, this toolchain stage is replaced by your hermetic gitpm busybox
RUN apk add --no-cache busybox-static cpio gzip

FROM scratch
NAME: thumper-initramfs

# 1. Construct the absolute minimal kernel skeleton
RUN: |
  mkdir -p ${ROOTFS}/bin ${ROOTFS}/dev ${ROOTFS}/proc ${ROOTFS}/sys ${ROOTFS}/mnt/root

# 2. Inject our single statically linked multi-call binary
COPY --from=toolchain /bin/busybox ${ROOTFS}/bin/busybox
RUN: |
  # Create symlinks for all standard POSIX commands (sh, ls, mount, etc)
  ${ROOTFS}/bin/busybox --install -s ${ROOTFS}/bin

# 3. Write the actual PID 1 execution script
RUN: |
  cat << 'EOF' > ${ROOTFS}/init
  #!/bin/sh
  # This runs as PID 1 before the real OS exists
  mount -t proc none /proc
  mount -t sysfs none /sys
  mount -t devtmpfs none /dev

  echo "======================================"
  echo " Booting Thumper Minimal Initramfs... "
  echo "======================================"
  
  # Normally, here you would mount the actual ext4/ZFS disk to /mnt/root
  # e.g., mount -t ext4 /dev/vda1 /mnt/root
  # And then hand off execution to the real OS:
  # exec switch_root /mnt/root /sbin/init
  
  # For this demo, we just drop the user straight into a rescue shell
  exec /bin/sh
  EOF
  
  chmod +x ${ROOTFS}/init

# 4. Pack it into the deterministic cpio format the kernel demands
RUN [host]: |
  cd ${ROOTFS}
  # Note: sorting the output and forcing the UID/GID to 0 guarantees
  # the resulting archive has the exact same SHA256 every time it is built.
  find . | LC_ALL=C sort | cpio --reproducible --owner=0:0 -H newc -o | gzip -9 > /tmp/thumper-initramfs.img
  
  echo "Generated initramfs at /tmp/thumper-initramfs.img"
