#!/usr/bin/env bash
set -euo pipefail

set_up() {
  source ./scripts/postremove.sh
}

test_postremove_help_short() {
  local output
  output="$(main -h)"
  assert_contains "Post-removal cleanup and systemd daemon reload for bro" "${output}"
}

test_postremove_help_long() {
  local output
  output="$(main --help)"
  assert_contains "Post-removal cleanup and systemd daemon reload for bro" "${output}"
}

test_postremove_invalid_arg() {
  local exit_code
  set +e
  (
    main invalid-action
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"
}

test_postremove_die_default() {
  local exit_code
  set +e
  (
    die
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"
}

test_postremove_trace() {
  (
    TRACE=1 main 0
  )
  assert_successful_code
}

test_postremove_without_systemd() {
  local non_existent_dir
  non_existent_dir="$(mktemp -d)"
  rm -rf "${non_existent_dir}"
  (
    SYSTEMD_RUN_DIR="${non_existent_dir}" main remove
  )
  assert_successful_code
}

test_postremove_with_systemd() {
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

test_postremove_purge() {
  local tmp_dir
  tmp_dir="$(mktemp -d)"

  local cfg_dir="${tmp_dir}/cfg"
  local doc_dir="${tmp_dir}/doc"
  local srv_file="${tmp_dir}/srv.service"
  local tmr_file="${tmp_dir}/tmr.timer"
  local mod_file="${tmp_dir}/mod.conf"
  local lck_file="${tmp_dir}/lck.lock"

  mkdir -p "${cfg_dir}" "${doc_dir}"
  touch "${srv_file}" "${tmr_file}" "${mod_file}" "${lck_file}"

  (
    CONFIG_DIR="${cfg_dir}" \
      DOC_DIR="${doc_dir}" \
      SERVICE_FILE="${srv_file}" \
      TIMER_FILE="${tmr_file}" \
      MODULES_FILE="${mod_file}" \
      LOCK_FILE="${lck_file}" \
      main purge
  )
  assert_successful_code

  assert_file_not_exists "${cfg_dir}"
  assert_file_not_exists "${doc_dir}"
  assert_file_not_exists "${srv_file}"
  assert_file_not_exists "${tmr_file}"
  assert_file_not_exists "${mod_file}"
  assert_file_not_exists "${lck_file}"

  rm -rf "${tmp_dir}"
}

test_postremove_actions() {
  local non_existent_dir
  non_existent_dir="$(mktemp -d)"
  rm -rf "${non_existent_dir}"
  (
    SYSTEMD_RUN_DIR="${non_existent_dir}" main 0
    SYSTEMD_RUN_DIR="${non_existent_dir}" main 1
    SYSTEMD_RUN_DIR="${non_existent_dir}" main ""
    SYSTEMD_RUN_DIR="${non_existent_dir}" main upgrade
    SYSTEMD_RUN_DIR="${non_existent_dir}" main failed-upgrade
    SYSTEMD_RUN_DIR="${non_existent_dir}" main abort-install
    SYSTEMD_RUN_DIR="${non_existent_dir}" main abort-upgrade
    SYSTEMD_RUN_DIR="${non_existent_dir}" main disappear
  )
  assert_successful_code
}

test_postremove_entrypoint_execution() {
  local output
  output="$(
    export BASH_SOURCE_OVERRIDE="./scripts/postremove.sh"
    source ./scripts/postremove.sh --help
  )"
  assert_contains "Post-removal cleanup and systemd daemon reload for bro" "${output}"
}
