# templates/emit/image.dockerfile
RUN [host]: |
  sys.parse_os_release
  sys.emit_image_yaml --outdir="${OUTDIR}"
  sys.copy_blueprints --outdir="${OUTDIR}"
  sys.update_catalog --kind="images"
