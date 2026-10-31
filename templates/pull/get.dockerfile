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

RUN [$TARGET_STAGE, host]: |
  set -e
  if [ -z "${GET_URL}" ] || [ -z "${GET_DEST}" ]; then
    echo "Error: GET_URL or GET_DEST not set."
    exit 1
  fi
  
  if echo "${GET_URL}" | grep -qE '^https?://'; then
      HASH=$(echo -n "${GET_URL}" | sha256sum | awk '{print $1}')
      CACHE_FILE="${SST_CACHE_GET}/${HASH}"
      
      if [ ! -f "$CACHE_FILE" ]; then
        echo "--> GET cache miss: downloading payload..."
        curl -fsSL "${GET_URL}" -o "$CACHE_FILE"
      else
        echo "--> GET cache hit: ${GET_URL}"
      fi
  else
      if [ ! -f "${GET_URL}" ]; then
        echo "Error: Local file ${GET_URL} does not exist."
        exit 1
      fi
      CACHE_FILE="${GET_URL}"
  fi
  
  DST="${ROOTFS}/${GET_DEST}"
  
  # POSIX-compliant check for trailing slash
  case "${GET_DEST}" in
    */) DST="${DST}$(basename "${GET_URL}" | cut -d? -f1)" ;;
  esac
  
  mkdir -p "$(dirname "$DST")"
  echo "--> Injecting payload to $DST"
  cp -a "$CACHE_FILE" "$DST"
