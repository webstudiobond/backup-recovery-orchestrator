#!/usr/bin/env bash
set -euo pipefail

set_up() {
  source ./scripts/postinstall.sh
}

test_postinstall_help_short() {
  local output
  output="$(main -h)"
  assert_contains "Post-installation service registration for bro" "${output}"
}

test_postinstall_help_long() {
  local output
  output="$(main --help)"
  assert_contains "Post-installation service registration for bro" "${output}"
}

test_postinstall_invalid_arg() {
  local exit_code
  set +e
  (
    main invalid-action
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"
}

test_postinstall_die_default() {
  local exit_code
  set +e
  (
    die
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"
}

test_postinstall_trace() {
  (
    TRACE=1 main abort-upgrade
  )
  assert_successful_code
}

test_postinstall_without_systemd() {
  local non_existent_dir
  non_existent_dir="$(mktemp -d)"
  rm -rf "${non_existent_dir}"
  (
    SYSTEMD_RUN_DIR="${non_existent_dir}" main configure
  )
  assert_successful_code
}

test_postinstall_with_systemd() {
  local tmp_run
  tmp_run="$(mktemp -d)"
  (
    systemctl() {
      return 0
    }
    SYSTEMD_RUN_DIR="${tmp_run}" main configure
    systemctl >/dev/null
  )
  assert_successful_code
  rm -rf "${tmp_run}"
}

test_postinstall_actions() {
  local non_existent_dir
  non_existent_dir="$(mktemp -d)"
  rm -rf "${non_existent_dir}"
  (
    SYSTEMD_RUN_DIR="${non_existent_dir}" main ""
    SYSTEMD_RUN_DIR="${non_existent_dir}" main 1
    SYSTEMD_RUN_DIR="${non_existent_dir}" main 2
    SYSTEMD_RUN_DIR="${non_existent_dir}" main abort-remove
    SYSTEMD_RUN_DIR="${non_existent_dir}" main abort-deconfigure
    SYSTEMD_RUN_DIR="${non_existent_dir}" main abort-upgrade
  )
  assert_successful_code
}

test_postinstall_entrypoint_execution() {
  local output
  output="$(
    export BASH_SOURCE_OVERRIDE="./scripts/postinstall.sh"
    source ./scripts/postinstall.sh --help
  )"
  assert_contains "Post-installation service registration for bro" "${output}"
}
