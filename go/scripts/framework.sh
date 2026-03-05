#!/usr/bin/env bash
# test-debug-framework.sh — logx-integrated shell step framework
#
# - Structured logging via logx()
# - No per-step log files
# - Preserves step/run semantics
# - Propagates SST_CALL_PATH
# - Compatible with Go logx + future OTEL

# ===== guards =====
if [[ -n "${__SST_FRAMEWORK_LOADED:-}" ]]; then return 0; fi
__SST_FRAMEWORK_LOADED=1

# ===== defaults =====
: "${SST_VERBOSE:=1}"          # 0/1: show command execution events
: "${SST_CALL_PATH:=}"         # inherited execution path
: "${SST_TRACE_ID:=}"          # inherited trace id (optional)

# ===== utilities =====

_now_iso() {
  date -Is
}

_sanitize() {
  echo "$1" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9._-]/_/g'
}

# ===== logx =====
# Usage:
#   logx LEVEL MESSAGE [key=value ...]
#
# LEVEL: debug | info | warn | error
#
logx() {
  local level="$1"; shift
  local msg="$1"; shift

  printf '{'
  printf '"level":"%s",' "$level"
  printf '"time":"%s",' "$(_now_iso)"
  printf '"pid":%d,' "$$"

  if [[ -n "$SST_CALL_PATH" ]]; then
    printf '"call_path":"%s",' "$SST_CALL_PATH"
  fi
  if [[ -n "$SST_TRACE_ID" ]]; then
    printf '"trace_id":"%s",' "$SST_TRACE_ID"
  fi

  printf '"message":"%s"' "$msg"

  for kv in "$@"; do
    key="${kv%%=*}"
    val="${kv#*=}"
    printf ',"%s":"%s"' "$key" "$val"
  done

  printf '}\n'
}

die() {
  logx error "fatal" msg="$*"
  exit 1
}

# ===== step state =====

: "${SST_STEP:=0}"
SST_STEP_NAME=""
_SST_PREV_CALL_PATH=""

step_enter() {
  SST_STEP=$(( SST_STEP + 1 ))
  SST_STEP_NAME="${1:-step}"
  local idx="$SST_STEP"
  local safe
  safe="$(_sanitize "$SST_STEP_NAME")"

  _SST_PREV_CALL_PATH="$SST_CALL_PATH"
  if [[ -n "$SST_CALL_PATH" ]]; then
    export SST_CALL_PATH="$SST_CALL_PATH.shell.$safe"
  else
    export SST_CALL_PATH="shell.$safe"
  fi

  logx info "step.start" step="$SST_STEP_NAME" step_idx="$idx"
}

step_ok() {
  logx info "step.ok" step="$SST_STEP_NAME" step_idx="$SST_STEP"
  export SST_CALL_PATH="$_SST_PREV_CALL_PATH"
}

step_fail() {
  logx error "step.fail" step="$SST_STEP_NAME" step_idx="$SST_STEP"
  export SST_CALL_PATH="$_SST_PREV_CALL_PATH"
  exit 1
}

# ===== command execution =====

run() {
  local cmd="$*"
  local start_ns end_ns dur_ms rc

  start_ns="$(date +%s%N)"
  logx debug "exec.start" cmd="$cmd" cwd="$(pwd)"

  "$@" 2>&1
  rc=$?

  end_ns="$(date +%s%N)"
  dur_ms=$(( (end_ns - start_ns) / 1000000 ))

  if [[ "$rc" -ne 0 ]]; then
    logx error "exec.fail" \
      cmd="$cmd" \
      exit_code="$rc" \
      duration_ms="$dur_ms"
    exit "$rc"
  fi

  if [[ "$SST_VERBOSE" -eq 1 ]]; then
    logx info "exec.ok" \
      cmd="$cmd" \
      duration_ms="$dur_ms"
  else
    logx debug "exec.ok" \
      cmd="$cmd" \
      duration_ms="$dur_ms"
  fi
}

