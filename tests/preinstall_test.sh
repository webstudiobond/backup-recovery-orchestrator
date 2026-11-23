#!/usr/bin/env bash
set -euo pipefail

set_up() {
  source ./scripts/preinstall.sh
}

test_preinstall_help_short() {
  local output
  output="$(main -h)"
  assert_contains "Pre-installation environment validation for bro" "${output}"
}

test_preinstall_help_long() {
  local output
  output="$(main --help)"
  assert_contains "Pre-installation environment validation for bro" "${output}"
}

test_preinstall_die_default() {
  local exit_code
  set +e
  (
    die
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"
}

test_preinstall_missing_lvs() {
  local empty_dir
  empty_dir="$(mktemp -d)"
  local exit_code
  set +e
  (
    PATH="${empty_dir}" check_lvm
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"
  rm -rf "${empty_dir}"
}

test_preinstall_no_lvs() {
  local exit_code
  set +e
  (
    lvs() {
      printf ""
    }
    vgs() {
      printf ""
    }
    check_lvm
    lvs >/dev/null
    vgs >/dev/null
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"
}

test_preinstall_vg_free_zero() {
  local exit_code
  set +e
  (
    lvs() {
      printf "lv_root\n"
    }
    vgs() {
      printf "\nnot_number\n0\n"
    }
    check_lvm
    lvs >/dev/null
    vgs >/dev/null
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"
}

test_preinstall_vg_free_positive() {
  (
    lvs() {
      printf "lv_root\n"
    }
    vgs() {
      printf "10737418240\n"
    }
    main install
    lvs >/dev/null
    vgs >/dev/null
  )
  assert_successful_code
}

test_preinstall_abort_upgrade() {
  (
    main abort-upgrade
  )
  assert_successful_code
}

test_preinstall_trace() {
  (
    TRACE=1 main abort-upgrade
  )
  assert_successful_code
}

test_preinstall_default_arg() {
  (
    lvs() {
      printf "lv_root\n"
    }
    vgs() {
      printf "10737418240\n"
    }
    main ""
    lvs >/dev/null
    vgs >/dev/null
  )
  assert_successful_code
}

test_preinstall_other_arg() {
  (
    lvs() {
      printf "lv_root\n"
    }
    vgs() {
      printf "10737418240\n"
    }
    main other
    lvs >/dev/null
    vgs >/dev/null
  )
  assert_successful_code
}

test_preinstall_entrypoint_execution() {
  local output
  output="$(
    export BASH_SOURCE_OVERRIDE="./scripts/preinstall.sh"
    source ./scripts/preinstall.sh --help
  )"
  assert_contains "Pre-installation environment validation for bro" "${output}"
}
