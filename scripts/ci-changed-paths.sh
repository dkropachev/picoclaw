#!/usr/bin/env bash

set -euo pipefail

readonly OUTPUT_NAMES=(
  lint
  frontend
  frontend_ui
  vuln_check
  test
  cross_compile
  integration
)

lint=false
frontend=false
frontend_ui=false
vuln_check=false
test=false
cross_compile=false
integration=false

enable_backend_checks() {
  lint=true
  vuln_check=true
  test=true
  cross_compile=true
  integration=true
}

enable_all_checks() {
  local output
  for output in "${OUTPUT_NAMES[@]}"; do
    printf -v "${output}" '%s' true
  done
}

emit_outputs() {
  local output
  for output in "${OUTPUT_NAMES[@]}"; do
    printf '%s=%s\n' "${output}" "${!output}"
  done
}

classify_path() {
  local path="$1"
  local change="$2"

  case "${path}" in
    .github/workflows/pr.yml | scripts/* | Makefile | web/Makefile)
      enable_all_checks
      ;;
    web/frontend/*)
      lint=true
      frontend=true
      frontend_ui=true
      ;;
    web/backend/*)
      enable_backend_checks
      frontend=true
      ;;
    docs/features/*)
      lint=true
      if [[ "${change}" == "removed" ]]; then
        frontend=true
      fi
      ;;
    docs/reference/repository-reviews-api.md)
      # API route coverage asserts this contract directly.
      test=true
      ;;
    *.go | go.mod | go.sum | go.work | go.work.sum)
      enable_backend_checks
      ;;
    integration/*)
      lint=true
      test=true
      integration=true
      ;;
    docker/* | config/*)
      enable_all_checks
      ;;
    workspace/*)
      test=true
      cross_compile=true
      ;;
    .golangci.yml | .golangci.yaml)
      lint=true
      ;;
    .goreleaser.yml | .goreleaser.yaml)
      test=true
      cross_compile=true
      ;;
    *.md | docs/* | assets/* | examples/* | LICENSE* | NOTICE* | .gitignore | .editorconfig | \
      .github/FUNDING.yml | .github/ISSUE_TEMPLATE/* | .github/pull_request_template.md)
      ;;
    *)
      # Unknown paths run everything. New repository surfaces must opt into a
      # narrower class explicitly instead of silently losing validation.
      enable_all_checks
      ;;
  esac
}

if [[ "${1:-}" == "--force-all" ]]; then
  if [[ "$#" -ne 1 ]]; then
    echo "usage: ci-changed-paths.sh [--force-all]" >&2
    exit 2
  fi
  enable_all_checks
  emit_outputs
  exit 0
fi
if [[ "$#" -ne 0 ]]; then
  echo "usage: ci-changed-paths.sh [--force-all]" >&2
  exit 2
fi

fields=()
while true; do
  field=""
  if IFS= read -r -d '' field; then
    fields+=("${field}")
    continue
  fi
  if [[ -n "${field}" ]]; then
    echo "malformed name-status stream: unterminated field" >&2
    exit 2
  fi
  break
done

index=0
while ((index < ${#fields[@]})); do
  status="${fields[index]}"
  ((index += 1))
  case "${status}" in
    R[0-9][0-9][0-9] | C[0-9][0-9][0-9])
      score="${status:1}"
      if ((10#${score} > 100)); then
        echo "unsupported git change status: ${status}" >&2
        exit 2
      fi
      if ((index + 1 >= ${#fields[@]})); then
        echo "malformed name-status stream: ${status} requires two paths" >&2
        exit 2
      fi
      old_path="${fields[index]}"
      new_path="${fields[index + 1]}"
      ((index += 2))
      if [[ -z "${old_path}" || -z "${new_path}" ]]; then
        echo "malformed name-status stream: ${status} contains an empty path" >&2
        exit 2
      fi
      if [[ "${status}" == R* ]]; then
        classify_path "${old_path}" removed
      else
        classify_path "${old_path}" changed
      fi
      classify_path "${new_path}" changed
      ;;
    A | M | T | U | X | B | D)
      if ((index >= ${#fields[@]})); then
        echo "malformed name-status stream: ${status} requires one path" >&2
        exit 2
      fi
      path="${fields[index]}"
      ((index += 1))
      if [[ -z "${path}" ]]; then
        echo "malformed name-status stream: ${status} contains an empty path" >&2
        exit 2
      fi
      if [[ "${status}" == D ]]; then
        classify_path "${path}" removed
      else
        classify_path "${path}" changed
      fi
      ;;
    *)
      echo "unsupported git change status: ${status}" >&2
      exit 2
      ;;
  esac
done

emit_outputs
