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

FROM scratch
NAME: ${CLONE_NAME}

# 1. Fetch the image
GET ${CLONE_SOURCE} /source_image

# 2. Validate & Convert to raw block device (Imperative Bash Level)
RUN [host]: |
  set -e
  CAPSULE_DIR="${BASEDIR}/capsule"
  mkdir -p "${CAPSULE_DIR}"
  
  FORMAT=$(qemu-img info "${ROOTFS}/source_image" | grep "file format" | awk '{print $3}' || true)
  if [ "$FORMAT" = "qcow2" ]; then
      echo "--> Converting QCOW2 to raw capsule..."
      qemu-img convert -f qcow2 -O raw "${ROOTFS}/source_image" "${CAPSULE_DIR}/disk.raw"
  else
      echo "--> Validating raw image partition table..."
      if ! fdisk -l "${ROOTFS}/source_image" | grep -q "^/dev/\|.*img[0-9]"; then
          echo "FATAL: Image is not a QCOW2 and does not contain a recognizable partition table."
          exit 1
      fi
      echo "--> Partition table found. Copying raw image..."
      cp --sparse=always "${ROOTFS}/source_image" "${CAPSULE_DIR}/disk.raw"
  fi

# 3. Analyze layout & Hydrate State (Go Metadata Level)
RUN [host]: sys.analyse_image --disk="${BASEDIR}/capsule/disk.raw"

# 4. Extract Boot Artifacts (Go Metadata Level)
RUN [host]: sys.extract_boot_artifacts --disk="${BASEDIR}/capsule/disk.raw" --outdir="${BASEDIR}/capsule"

# 5. Serialize final YAML (Go Metadata Level)
RUN [host]: sys.emit_image_yaml --outdir="${BASEDIR}/capsule"

# 6. Global Export Directive
EMIT: capsule
