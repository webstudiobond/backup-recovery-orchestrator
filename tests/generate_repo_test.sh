#!/usr/bin/env bash
set -euo pipefail

set_up() {
  source ./scripts/generate-repo.sh
}

test_generate_repo_help_short() {
  local output
  output="$(main -h)"
  assert_contains "Generate and sign Debian (APT) repository for bro" "${output}"
}

test_generate_repo_help_long() {
  local output
  output="$(main --help)"
  assert_contains "Generate and sign Debian (APT) repository for bro" "${output}"
}

test_generate_repo_no_args() {
  local exit_code
  set +e
  (
    main
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"
}

test_generate_repo_missing_output_dir() {
  local exit_code
  set +e
  (
    main "/tmp" ""
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"
}

test_generate_repo_missing_packages_dir() {
  local exit_code
  set +e
  (
    main "/nonexistent_pkg_dir_xyz" "/tmp/output"
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"
}

test_generate_repo_die_default() {
  local exit_code
  set +e
  (
    die
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"
}

test_generate_repo_has_command_true() {
  local result
  result="$(has_command bash)"
  assert_equals "yes" "${result}"
}

test_generate_repo_has_command_false() {
  local result
  result="$(has_command non_existent_binary_xyz)"
  assert_equals "no" "${result}"
}

test_generate_repo_check_dependencies_success() {
  (
    has_command() {
      printf "yes"
    }
    check_dependencies
    has_command gpg >/dev/null
  )
  assert_successful_code
}

test_generate_repo_check_dependencies_missing() {
  local exit_code
  set +e
  (
    has_command() {
      case "$1" in
      gpg)
        printf "no"
        ;;
      *)
        printf "yes"
        ;;
      esac
    }
    check_dependencies
    has_command gpg >/dev/null
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"
}

test_generate_repo_import_gpg_key_no_env() {
  local exit_code
  set +e
  (
    GPG_PRIVATE_KEY="" import_gpg_key
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"
}

test_generate_repo_import_gpg_key_without_passphrase() {
  (
    gpg() {
      return 0
    }
    GPG_PRIVATE_KEY="dummy-key" GPG_PASSPHRASE="" import_gpg_key
    gpg >/dev/null
  )
  assert_successful_code
}

test_generate_repo_import_gpg_key_with_passphrase() {
  (
    gpg() {
      return 0
    }
    GPG_PRIVATE_KEY="dummy-key" GPG_PASSPHRASE="secret" import_gpg_key
    gpg >/dev/null
  )
  assert_successful_code
}

test_generate_repo_import_gpg_key_failure() {
  local exit_code
  set +e
  (
    gpg() {
      return 1
    }
    GPG_PRIVATE_KEY="dummy-key" GPG_PASSPHRASE="" import_gpg_key
    gpg >/dev/null
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"
}

test_generate_repo_sign_detached_no_passphrase() {
  local tmp_file
  tmp_file="$(mktemp)"
  (
    gpg() {
      touch "$7"
      return 0
    }
    GPG_PASSPHRASE="" sign_detached "${tmp_file}" "${tmp_file}.sig"
    gpg >/dev/null
  )
  assert_successful_code
  rm -f "${tmp_file}" "${tmp_file}.sig"
}

test_generate_repo_sign_detached_with_passphrase() {
  local tmp_file
  tmp_file="$(mktemp)"
  (
    gpg() {
      touch "${10}"
      return 0
    }
    GPG_PASSPHRASE="secret" sign_detached "${tmp_file}" "${tmp_file}.sig"
    gpg >/dev/null
  )
  assert_successful_code
  rm -f "${tmp_file}" "${tmp_file}.sig"
}

test_generate_repo_sign_detached_failure() {
  local tmp_file
  tmp_file="$(mktemp)"
  local exit_code
  set +e
  (
    gpg() {
      return 1
    }
    GPG_PASSPHRASE="" sign_detached "${tmp_file}" "${tmp_file}.sig"
    gpg >/dev/null
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"
  rm -f "${tmp_file}"
}

test_generate_repo_sign_clearsign_no_passphrase() {
  local tmp_file
  tmp_file="$(mktemp)"
  (
    gpg() {
      touch "$6"
      return 0
    }
    GPG_PASSPHRASE="" sign_clearsign "${tmp_file}" "${tmp_file}.sig"
    gpg >/dev/null
  )
  assert_successful_code
  rm -f "${tmp_file}" "${tmp_file}.sig"
}

test_generate_repo_sign_clearsign_with_passphrase() {
  local tmp_file
  tmp_file="$(mktemp)"
  (
    gpg() {
      touch "${9}"
      return 0
    }
    GPG_PASSPHRASE="secret" sign_clearsign "${tmp_file}" "${tmp_file}.sig"
    gpg >/dev/null
  )
  assert_successful_code
  rm -f "${tmp_file}" "${tmp_file}.sig"
}

test_generate_repo_sign_clearsign_failure() {
  local tmp_file
  tmp_file="$(mktemp)"
  local exit_code
  set +e
  (
    gpg() {
      return 1
    }
    GPG_PASSPHRASE="" sign_clearsign "${tmp_file}" "${tmp_file}.sig"
    gpg >/dev/null
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"
  rm -f "${tmp_file}"
}

test_generate_repo_rotate_packages() {
  local tmp_dir
  tmp_dir="$(mktemp -d)"

  for i in $(seq 1 55); do
    touch "${tmp_dir}/pkg_${i}_amd64.deb"
  done

  rotate_packages "${tmp_dir}" "*_amd64.deb"

  local remaining_count
  remaining_count="$(find "${tmp_dir}" -maxdepth 1 -type f -name "*_amd64.deb" | wc -l | tr -d ' ')"
  assert_equals "50" "${remaining_count}"

  rm -rf "${tmp_dir}"
}

test_generate_repo_rotate_packages_empty() {
  local tmp_dir
  tmp_dir="$(mktemp -d)"

  rotate_packages "${tmp_dir}" "*_amd64.deb"

  local remaining_count
  remaining_count="$(find "${tmp_dir}" -maxdepth 1 -type f -name "*_amd64.deb" | wc -l | tr -d ' ')"
  assert_equals "0" "${remaining_count}"

  rm -rf "${tmp_dir}"
}

test_generate_repo_main_success() {
  local tmp_dir
  tmp_dir="$(mktemp -d)"
  local pkg_dir="${tmp_dir}/pkgs"
  local out_dir="${tmp_dir}/out"
  local existing_dir="${tmp_dir}/existing"

  mkdir -p "${pkg_dir}" "${out_dir}" "${existing_dir}/deb/pool/main"
  touch "${pkg_dir}/bro_1.0.0_amd64.deb" "${pkg_dir}/bro_1.0.0_arm64.deb"
  touch "${existing_dir}/deb/pool/main/bro_0.9.0_amd64.deb"

  (
    check_dependencies() {
      return 0
    }
    import_gpg_key() {
      return 0
    }
    dpkg-scanpackages() {
      printf "Package: bro\n"
      return 0
    }
    gzip() {
      touch "${2}"
      return 0
    }
    apt-ftparchive() {
      printf "Origin: webstudiobond\n"
      return 0
    }
    sign_detached() {
      touch "${2}"
      return 0
    }
    sign_clearsign() {
      touch "${2}"
      return 0
    }
    main "${pkg_dir}" "${out_dir}" "${existing_dir}"
    check_dependencies >/dev/null
    import_gpg_key >/dev/null
    dpkg-scanpackages >/dev/null
    gzip "" "" >/dev/null
    apt-ftparchive >/dev/null
    sign_detached "" "" >/dev/null
    sign_clearsign "" "" >/dev/null
  )
  assert_successful_code

  assert_file_exists "${out_dir}/deb/dists/stable/main/binary-amd64/Packages"
  assert_file_exists "${out_dir}/deb/dists/stable/main/binary-arm64/Packages"
  assert_file_exists "${out_dir}/deb/dists/stable/Release"

  rm -rf "${tmp_dir}"
}

test_generate_repo_main_scan_failure() {
  local tmp_dir
  tmp_dir="$(mktemp -d)"
  local pkg_dir="${tmp_dir}/pkgs"
  local out_dir="${tmp_dir}/out"

  mkdir -p "${pkg_dir}" "${out_dir}"
  touch "${pkg_dir}/bro_1.0.0_amd64.deb"

  local exit_code
  set +e
  (
    check_dependencies() {
      return 0
    }
    import_gpg_key() {
      return 0
    }
    dpkg-scanpackages() {
      return 1
    }
    main "${pkg_dir}" "${out_dir}"
    check_dependencies >/dev/null
    import_gpg_key >/dev/null
    dpkg-scanpackages >/dev/null
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"

  rm -rf "${tmp_dir}"
}

test_generate_repo_main_gzip_failure() {
  local tmp_dir
  tmp_dir="$(mktemp -d)"
  local pkg_dir="${tmp_dir}/pkgs"
  local out_dir="${tmp_dir}/out"

  mkdir -p "${pkg_dir}" "${out_dir}"
  touch "${pkg_dir}/bro_1.0.0_amd64.deb"

  local exit_code
  set +e
  (
    check_dependencies() {
      return 0
    }
    import_gpg_key() {
      return 0
    }
    dpkg-scanpackages() {
      return 0
    }
    gzip() {
      return 1
    }
    main "${pkg_dir}" "${out_dir}"
    check_dependencies >/dev/null
    import_gpg_key >/dev/null
    dpkg-scanpackages >/dev/null
    gzip >/dev/null
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"

  rm -rf "${tmp_dir}"
}

test_generate_repo_main_release_failure() {
  local tmp_dir
  tmp_dir="$(mktemp -d)"
  local pkg_dir="${tmp_dir}/pkgs"
  local out_dir="${tmp_dir}/out"

  mkdir -p "${pkg_dir}" "${out_dir}"
  touch "${pkg_dir}/bro_1.0.0_amd64.deb"

  local exit_code
  set +e
  (
    check_dependencies() {
      return 0
    }
    import_gpg_key() {
      return 0
    }
    dpkg-scanpackages() {
      return 0
    }
    gzip() {
      return 0
    }
    apt-ftparchive() {
      return 1
    }
    main "${pkg_dir}" "${out_dir}"
    check_dependencies >/dev/null
    import_gpg_key >/dev/null
    dpkg-scanpackages >/dev/null
    gzip >/dev/null
    apt-ftparchive >/dev/null
  ) >/dev/null 2>&1
  exit_code=$?
  set -e
  assert_equals "1" "${exit_code}"

  rm -rf "${tmp_dir}"
}

test_generate_repo_trace() {
  local tmp_dir
  tmp_dir="$(mktemp -d)"
  local pkg_dir="${tmp_dir}/pkgs"
  local out_dir="${tmp_dir}/out"
  mkdir -p "${pkg_dir}" "${out_dir}"

  (
    check_dependencies() {
      return 0
    }
    import_gpg_key() {
      return 0
    }
    dpkg-scanpackages() {
      return 0
    }
    gzip() {
      return 0
    }
    apt-ftparchive() {
      return 0
    }
    sign_detached() {
      return 0
    }
    sign_clearsign() {
      return 0
    }
    TRACE=1 main "${pkg_dir}" "${out_dir}"
    check_dependencies >/dev/null
    import_gpg_key >/dev/null
    dpkg-scanpackages >/dev/null
    gzip >/dev/null
    apt-ftparchive >/dev/null
    sign_detached >/dev/null
    sign_clearsign >/dev/null
  )
  assert_successful_code
  rm -rf "${tmp_dir}"
}

test_generate_repo_entrypoint_execution() {
  local output
  output="$(
    export BASH_SOURCE_OVERRIDE="./scripts/generate-repo.sh"
    source ./scripts/generate-repo.sh --help
  )"
  assert_contains "Generate and sign Debian (APT) repository for bro" "${output}"
}
