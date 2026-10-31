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

RUN [host]: |
  set -e
  echo "--> Extracting kernel and initrd from rootfs for direct boot..."
  mkdir -p "${ARTEFACTS_DIR}"
  
  KFILE=$(find "${ROOTFS}/boot" -maxdepth 1 \( -name "vmlinu*" -o -name "bzImage" \) | head -n 1 || true)
  IFILE=$(find "${ROOTFS}/boot" -maxdepth 1 \( -name "initramfs*" -o -name "initrd*" \) | head -n 1 || true)
  
  if [ -n "$KFILE" ] && [ -e "$KFILE" ]; then
    echo "    Uncompressing kernel using mkimg extract_kernel..."
    mkimg extract_kernel "$KFILE" "${ARTEFACTS_DIR}/kernel"
    # Instantly bind it to the ActiveManifest
    sys.set_boot_asset --type=kernel --path="capsule-${CAPSULE_NAME}-artifacts/kernel"
  else
    echo "    No kernel found in ${ROOTFS}/boot. Skipping kernel extraction."
  fi
  
  if [ -n "$IFILE" ] && [ -e "$IFILE" ]; then
    echo "    Copying initrd..."
    cp -L "$IFILE" "${ARTEFACTS_DIR}/initrd"
    # Instantly bind it to the ActiveManifest
    sys.set_boot_asset --type=initrd --path="capsule-${CAPSULE_NAME}-artifacts/initrd"
  fi
