#!/usr/bin/env bash
set -euo pipefail

set_up() {
  source ./scripts/preremove.sh
}

test_preremove_help_short() {
  local output
  output="$(main -h)"
  assert_contains "Pre-removal service de-registration for bro" "${output}"
}

test_preremove_help_long() {
  local output
  output="$(main --help)"
  assert_contains "Pre-removal service de-registration for bro" "${output}"
}

test_preremove_invalid_arg() {
  local exit_code
  set +e
  (
    main invalid-action
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"
}

test_preremove_die_default() {
  local exit_code
  set +e
  (
    die
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"
}

test_preremove_trace() {
  (
    TRACE=1 main 0
  )
  assert_successful_code
}

test_preremove_upgrade_without_systemd() {
  local non_existent_dir
  non_existent_dir="$(mktemp -d)"
  rm -rf "${non_existent_dir}"
  (
    SYSTEMD_RUN_DIR="${non_existent_dir}" main upgrade
  )
  assert_successful_code
}

test_preremove_upgrade_with_systemd() {
  local tmp_run
  tmp_run="$(mktemp -d)"
  (
    systemctl() {
      return 0
    }
    SYSTEMD_RUN_DIR="${tmp_run}" main upgrade
    systemctl >/dev/null
  )
  assert_successful_code
  rm -rf "${tmp_run}"
}

test_preremove_remove_without_systemd() {
  local non_existent_dir
  non_existent_dir="$(mktemp -d)"
  rm -rf "${non_existent_dir}"
  (
    SYSTEMD_RUN_DIR="${non_existent_dir}" main remove
  )
  assert_successful_code
}

test_preremove_remove_with_systemd() {
  local tmp_run
  tmp_run="$(mktemp -d)"
  (
    systemctl() {
      return 0
    }
    SYSTEMD_RUN_DIR="${tmp_run}" main remove
    systemctl >/dev/null
  )
  assert_successful_code
  rm -rf "${tmp_run}"
}

test_preremove_actions() {
  local non_existent_dir
  non_existent_dir="$(mktemp -d)"
  rm -rf "${non_existent_dir}"
  (
    SYSTEMD_RUN_DIR="${non_existent_dir}" main 1
    SYSTEMD_RUN_DIR="${non_existent_dir}" main 2
    SYSTEMD_RUN_DIR="${non_existent_dir}" main 0
    SYSTEMD_RUN_DIR="${non_existent_dir}" main ""
    SYSTEMD_RUN_DIR="${non_existent_dir}" main deconfigure
    SYSTEMD_RUN_DIR="${non_existent_dir}" main failed-upgrade
  )
  assert_successful_code
}

test_preremove_entrypoint_execution() {
  local output
  output="$(
    export BASH_SOURCE_OVERRIDE="./scripts/preremove.sh"
    source ./scripts/preremove.sh --help
  )"
  assert_contains "Pre-removal service de-registration for bro" "${output}"
}
