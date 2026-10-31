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

RUN [base, host, raw]: |
  set -e
  mkdir -p "${ARTEFACTS_DIR}"
  IMG="${ARTEFACTS_DIR}/disk.raw"
  
  if [ -f "${BASEDIR}/disk.raw" ]; then
      echo "--> Moving assembled RAW Disk into capsule artefacts..."
      mv "${BASEDIR}/disk.raw" "$IMG"
  elif [ ! -f "$IMG" ]; then
      echo "--> Assembling default sparse RAW Disk: $IMG"
      truncate -s 4G "$IMG"
      mke2fs -t ext4 -d "${ROOTFS}" -U "12345678-1234-1234-1234-123456789abc" -E "hash_seed=12345678-1234-1234-1234-123456789abc" -F "$IMG" > /dev/null 2>&1
  fi
  
  # Instantly inject partition IDs into the in-memory ActiveManifest
  sys.analyse_image --disk="$IMG" --prefix="capsule-${CAPSULE_NAME}-artifacts/"
