# templates/emit/runtime.dockerfile
RUN [host]: |
  sys.emit_instance_yaml --outdir="${OUTDIR}"
  sys.copy_blueprints --outdir="${OUTDIR}"
  sys.update_catalog --kind="runtimes"
