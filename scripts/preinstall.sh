#!/usr/bin/env bash
set -euo pipefail

die() {
  local message
  message="${1:-Unexpected failure}"
  printf "Error: %s\n" "${message}" >&2
  exit 1
}

usage() {
  printf "Usage: %s [OPTIONS] [ACTION]\n" "$0"
  printf "Pre-installation environment validation for bro.\n"
  printf "Options:\n"
  printf "  -h, --help    Display usage instructions\n"
}

cleanup() {
  trap - EXIT INT TERM
}

check_lvm() {
  if ! command -v lvs >/dev/null 2>&1 || ! command -v vgs >/dev/null 2>&1; then
    die "LVM2 utilities (lvs, vgs) not found. LVM2 is required."
  fi

  local lv_count
  lv_count="$(lvs --noheadings 2>/dev/null | wc -l || true)"
  lv_count="$(printf "%s" "${lv_count}" | tr -d '[:space:]')"
  case "${lv_count}" in
  "" | 0)
    die "No LVM Logical Volumes found. bro requires LVM2 storage."
    ;;
  *)
    ;;
  esac

  local free_list
  free_list="$(vgs --noheadings --nosuffix --units b -o vg_free 2>/dev/null || true)"
  local has_free_space
  has_free_space="no"

  while IFS= read -r line; do
    local free_bytes
    free_bytes="$(printf "%s" "${line}" | tr -d '[:space:]')"
    case "${free_bytes}" in
    "" | *[!0-9]*)
      ;;
    *)
      if [[ ${free_bytes} -gt 0 ]]; then
        has_free_space="yes"
        break
      fi
      ;;
    esac
  done < <(printf "%s\n" "${free_list}")

  case "${has_free_space}" in
  no)
    die "No LVM Volume Group with unallocated free space found (vg_free == 0). LVM snapshots require free extents in the Volume Group."
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
  install | upgrade | "")
    check_lvm
    ;;
  abort-upgrade)
    ;;
  *)
    check_lvm
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
