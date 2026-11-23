#!/usr/bin/env bash
set -euo pipefail

SYSTEMD_RUN_DIR="${SYSTEMD_RUN_DIR:-/run/systemd/system}"

die() {
  local message
  message="${1:-Unexpected failure}"
  printf "Error: %s\n" "${message}" >&2
  exit 1
}

usage() {
  printf "Usage: %s [OPTIONS] [ACTION] [OLD_VERSION]\n" "$0"
  printf "Post-installation service registration for bro.\n"
  printf "Options:\n"
  printf "  -h, --help    Display usage instructions\n"
  printf "Actions:\n"
  printf "  configure     Reload systemd daemon and refresh kernel modules\n"
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

reload_systemd_and_modules() {
  local systemd_state
  systemd_state="$(check_systemd_active)"
  case "${systemd_state}" in
  yes)
    systemctl daemon-reload >/dev/null 2>&1 || true
    systemctl restart systemd-modules-load.service >/dev/null 2>&1 || true
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
  "" | configure | 1 | [2-9]*)
    reload_systemd_and_modules
    ;;
  abort-remove | abort-deconfigure | abort-upgrade)
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
