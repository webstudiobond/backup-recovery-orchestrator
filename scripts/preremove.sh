#!/usr/bin/env bash
set -euo pipefail

SERVICE_NAME="bro-full.service"
TIMER_NAME="bro-full.timer"
SYSTEMD_RUN_DIR="${SYSTEMD_RUN_DIR:-/run/systemd/system}"

die() {
  local message
  message="${1:-Unexpected failure}"
  printf "Error: %s\n" "${message}" >&2
  exit 1
}

usage() {
  printf "Usage: %s [OPTIONS] [ACTION]\n" "$0"
  printf "Pre-removal service de-registration for bro.\n"
  printf "Options:\n"
  printf "  -h, --help    Display usage instructions\n"
  printf "Actions:\n"
  printf "  remove        Stop and disable the timer and service\n"
  printf "  upgrade       Preserve service configuration during upgrade\n"
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

stop_service_only() {
  local systemd_state
  systemd_state="$(check_systemd_active)"
  case "${systemd_state}" in
  yes)
    systemctl is-active --quiet "${SERVICE_NAME}" 2>/dev/null && systemctl stop "${SERVICE_NAME}" >/dev/null 2>&1 || true
    ;;
  *)
    ;;
  esac
}

stop_and_disable_all() {
  local systemd_state
  systemd_state="$(check_systemd_active)"
  case "${systemd_state}" in
  yes)
    systemctl is-active --quiet "${TIMER_NAME}" 2>/dev/null && systemctl stop "${TIMER_NAME}" >/dev/null 2>&1 || true
    systemctl is-enabled --quiet "${TIMER_NAME}" 2>/dev/null && systemctl disable "${TIMER_NAME}" >/dev/null 2>&1 || true
    systemctl is-active --quiet "${SERVICE_NAME}" 2>/dev/null && systemctl stop "${SERVICE_NAME}" >/dev/null 2>&1 || true
    ;;
  *)
    ;;
  esac
}

main() {
  case "${TRACE:-0}" in
  1)
    set -x
    ;;
  *)
    ;;
  esac

  local first_arg
  first_arg="${1:-}"

  case "${first_arg}" in
  -h | --help)
    usage
    cleanup
    return 0
    ;;
  upgrade | [1-9]*)
    stop_service_only
    ;;
  0 | "" | remove | deconfigure | failed-upgrade)
    stop_and_disable_all
    ;;
  *)
    die "Invalid argument: ${first_arg}"
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
