RUN [$TARGET_STAGE, host]: |
  set -e
  if [ -z "${GET_URL}" ] || [ -z "${GET_DEST}" ]; then
    echo "Error: GET_URL or GET_DEST not set."
    exit 1
  fi
  
  HASH=$(echo -n "${GET_URL}" | sha256sum | awk '{print $1}')
  CACHE_FILE="${SST_CACHE_GET}/${HASH}"
  
  if [ ! -f "$CACHE_FILE" ]; then
    echo "--> GET cache miss: downloading payload..."
    curl -fsSL "${GET_URL}" -o "$CACHE_FILE"
  else
    echo "--> GET cache hit: ${GET_URL}"
  fi
  
  DST="${ROOTFS}/${GET_DEST}"
  
  # POSIX-compliant check for trailing slash
  case "${GET_DEST}" in
    */) DST="${DST}$(basename "${GET_URL}" | cut -d? -f1)" ;;
  esac
  
  mkdir -p "$(dirname "$DST")"
  echo "--> Injecting cached payload to $DST"
  cp -a "$CACHE_FILE" "$DST"
