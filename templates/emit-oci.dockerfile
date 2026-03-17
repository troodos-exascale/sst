RUN [host]: |
  set -e
  echo "--> Emitting OCI format..."
  TAR="${OUTDIR}/${NAME}.tar"
  mkdir -p "${OUTDIR}"
  tmpDir=$(mktemp -d)
  
  # 1. Create the flattened rootfs layer
  tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner -cf "${tmpDir}/layer.tar" -C "${ROOTFS}" .
  LAYER_HASH=$(sha256sum "${tmpDir}/layer.tar" | awk '{print $1}')
  
  # 2. Build the strict OCI config.json
  cat <<EOF > "${tmpDir}/config.json"
  {
    "architecture": "$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')",
    "os": "linux",
    "config": { "Env": ["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"] },
    "rootfs": { "type": "layers", "diff_ids": ["sha256:${LAYER_HASH}"] },
    "history": [{"created": "1970-01-01T00:00:00Z", "created_by": "sst mkimage"}]
  }
EOF
  
  CONFIG_HASH=$(sha256sum "${tmpDir}/config.json" | awk '{print $1}')
  mv "${tmpDir}/config.json" "${tmpDir}/${CONFIG_HASH}.json"
  
  # 3. Build the OCI manifest.json
  cat <<EOF > "${tmpDir}/manifest.json"
  [{"Config": "${CONFIG_HASH}.json", "RepoTags": ["${NAME}:latest"], "Layers": ["layer.tar"]}]
EOF
  
  # 4. Pack the final transport archive
  tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner -cf "${TAR}" -C "${tmpDir}" manifest.json "${CONFIG_HASH}.json" layer.tar
  rm -rf "${tmpDir}"
