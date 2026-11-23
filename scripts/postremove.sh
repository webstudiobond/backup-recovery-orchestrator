#!/usr/bin/env bash
set -euo pipefail

SERVICE_NAME="bro-full.service"
TIMER_NAME="bro-full.timer"
SYSTEMD_RUN_DIR="${SYSTEMD_RUN_DIR:-/run/systemd/system}"

CONFIG_DIR="${CONFIG_DIR:-/etc/bro}"
DOC_DIR="${DOC_DIR:-/usr/share/doc/bro}"
SERVICE_FILE="${SERVICE_FILE:-/etc/systemd/system/bro-full.service}"
TIMER_FILE="${TIMER_FILE:-/etc/systemd/system/bro-full.timer}"
MODULES_FILE="${MODULES_FILE:-/etc/modules-load.d/lvm-snapshot.conf}"
LOCK_FILE="${LOCK_FILE:-/run/lock/bro.lock}"

die() {
  local message
  message="${1:-Unexpected failure}"
  printf "Error: %s\n" "${message}" >&2
  exit 1
}

usage() {
  printf "Usage: %s [OPTIONS] [ACTION]\n" "$0"
  printf "Post-removal cleanup and systemd daemon reload for bro.\n"
  printf "Options:\n"
  printf "  -h, --help    Display usage instructions\n"
  printf "Actions:\n"
  printf "  remove        Standard package removal\n"
  printf "  purge         Complete removal including residual configuration and systemd units\n"
  printf "  upgrade       Package upgrade notification\n"
}

cleanup() {
  trap - EXIT INT TERM
}

check_systemd_active() {
  local active
  active="no"
  test -d "${SYSTEMD_RUN_DIR}" && command -v systemctl >/dev/null 2>&1 && active="yes"
  printf "%s" "${active}"
}

reload_systemd() {
  local systemd_state
  systemd_state="$(check_systemd_active)"
  case "${systemd_state}" in
  yes)
    systemctl daemon-reload >/dev/null 2>&1 || true
    systemctl reset-failed "${SERVICE_NAME}" "${TIMER_NAME}" >/dev/null 2>&1 || true
    ;;
  *)
    ;;
  esac
}

purge_residual_files() {
  test -d "${CONFIG_DIR}" && rm -rf "${CONFIG_DIR}" 2>/dev/null || true
  test -d "${DOC_DIR}" && rm -rf "${DOC_DIR}" 2>/dev/null || true
  test -f "${SERVICE_FILE}" && rm -f "${SERVICE_FILE}" 2>/dev/null || true
  test -f "${TIMER_FILE}" && rm -f "${TIMER_FILE}" 2>/dev/null || true
  test -f "${MODULES_FILE}" && rm -f "${MODULES_FILE}" 2>/dev/null || true
  test -f "${LOCK_FILE}" && rm -f "${LOCK_FILE}" 2>/dev/null || true
}

main() {
  case "${TRACE:-0}" in
  1)
    set -x
    ;;
  *)
    ;;
  esac

  local action
  action="${1:-}"

  case "${action}" in
  -h | --help)
    usage
    cleanup
    return 0
    ;;
  purge)
    reload_systemd
    purge_residual_files
    ;;
  remove | upgrade | failed-upgrade | abort-install | abort-upgrade | disappear | [0-9]* | "")
    reload_systemd
    ;;
  *)
    die "Invalid argument: ${action}"
    ;;
  esac

  cleanup
  return 0
}

case "${BASH_SOURCE[0]}" in
"${0}" | "${BASH_SOURCE_OVERRIDE:-}")
  trap cleanup EXIT INT TERM
  main "$@"
  ;;
*)
  ;;
esac
