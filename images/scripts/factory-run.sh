#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# SST Factory Runner (v9)
# Builds the factory container (if needed) and runs factory-build.sh inside it.
# ---------------------------------------------------------------------------

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"
. "${SCRIPT_DIR}/env.sh" || exit 1

show_help=false
for arg in "$@"; do
  if [[ "$arg" == "--help" || "$arg" == "-h" ]]; then show_help=true; break; fi
done

if [[ "$show_help" = true ]]; then
cat <<EOF
Usage: $0 [--arch <arch>] [--pkg <name>] [--force] [--help]

Builds and runs the SST artifact factory container to produce .deb packages.
This wraps around factory-build.sh, which runs inside the container.

Examples:
  $0 --arch arm64
  $0 --arch amd64 --pkg cue
  $0 --arch arm64 --force
EOF
exit 0
fi

ARCH="${ARCH:-$(dpkg --print-architecture || uname -m)}"
case "$ARCH" in
  aarch64) ARCH="arm64" ;;
  x86_64) ARCH="amd64" ;;
esac

REGISTRY="${LOCAL_REGISTRY:-localhost:5000}"
IMAGE_NAME="factory"
TAG="${REGISTRY}/sst/${IMAGE_NAME}-${ARCH}:latest"
ASSET_DIR="${REPO_ROOT}/images/assets/${ARCH}"

echo "--> Ensuring host asset directory exists and is writable: ${ASSET_DIR}"
mkdir -p "${ASSET_DIR}"
current_uid=$(id -u)
current_gid=$(id -g)

if [[ "$current_uid" -ne 0 ]]; then
  sudo chown -R "${current_uid}:${current_gid}" "${ASSET_DIR}"
  sudo chmod -R u+rwX,go+rX "${ASSET_DIR}"
else
  chown -R "${current_uid}:${current_gid}" "${ASSET_DIR}"
  chmod -R u+rwX,go+rX "${ASSET_DIR}"
fi

echo "--- SST Artifact Factory Run ---"
echo "Target Arch : ${ARCH}"
echo "Factory Image: ${TAG}"
echo "Host Asset Dir: ${ASSET_DIR}"
echo "Passed Args : $*"
echo "--------------------------------"

echo "--> Building or pulling factory image..."
"${SCRIPT_DIR}/build-container.sh" "${IMAGE_NAME}" "${ARCH}"
echo "--> Factory image ready."

local_build_script="${SCRIPT_DIR}/factory-build.sh"
extra_mount=""
if [[ -f "$local_build_script" ]]; then
  echo "--> Found local factory-build.sh. Mounting override."
  extra_mount="-v ${local_build_script}:/usr/local/bin/factory-build.sh:ro"
fi

echo "--> Running factory container to build packages..."
${BUILDER_CMD} run --rm \
  -v "${ASSET_DIR}:/assets" \
  ${extra_mount} \
  "${TAG}" \
  factory-build.sh --arch "${ARCH}" --out /assets "$@"

run_exit_code=$?
if [[ $run_exit_code -ne 0 ]]; then
  echo "[ERROR] factory-build.sh failed inside the container (exit ${run_exit_code})." >&2
  exit $run_exit_code
fi

echo "--> Factory run complete. Assets available under ${ASSET_DIR}/"
