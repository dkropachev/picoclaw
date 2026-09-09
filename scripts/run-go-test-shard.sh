#!/usr/bin/env bash

set -euo pipefail

list_only=false
if [[ "${1:-}" == "--list" ]]; then
  list_only=true
  shift
fi
if [[ "$#" -ne 1 || ( "$1" != "slow" && "$1" != "remaining" ) ]]; then
  echo "usage: run-go-test-shard.sh [--list] {slow|remaining}" >&2
  exit 2
fi
readonly requested_shard="$1"

module_path="$(go list -m)"
if [[ -z "${module_path}" || "${module_path}" == *$'\n'* ]]; then
  echo "invalid Go module path" >&2
  exit 1
fi

package_output="$(go list -tags goolm,stdjson -f '{{.ImportPath}}' ./...)"
if [[ -z "${package_output}" ]]; then
  echo "Go package list is empty" >&2
  exit 1
fi
mapfile -t packages <<< "${package_output}"

declare -A seen=()
slow_packages=()
remaining_packages=()
for package in "${packages[@]}"; do
  if [[ -z "${package}" || "${package}" == *[[:space:]]* ]]; then
    echo "invalid Go package path: ${package@Q}" >&2
    exit 1
  fi
  if [[ -n "${seen[${package}]:-}" ]]; then
    echo "duplicate Go package path: ${package}" >&2
    exit 1
  fi
  seen["${package}"]=true

  case "${package}" in
    "${module_path}/pkg/agent" | "${module_path}/pkg/agent/"* | \
      "${module_path}/web/backend/api" | "${module_path}/web/backend/api/"*)
      slow_packages+=("${package}")
      ;;
    *)
      remaining_packages+=("${package}")
      ;;
  esac
done

if (( ${#slow_packages[@]} == 0 || ${#remaining_packages[@]} == 0 )); then
  echo "Go test partition contains an empty shard" >&2
  exit 1
fi
if (( ${#slow_packages[@]} + ${#remaining_packages[@]} != ${#packages[@]} )); then
  echo "Go test partition does not cover every package exactly once" >&2
  exit 1
fi

selected=()
case "${requested_shard}" in
  slow)
    selected=("${slow_packages[@]}")
    ;;
  remaining)
    selected=("${remaining_packages[@]}")
    ;;
esac

if [[ "${list_only}" == true ]]; then
  printf '%s\n' "${selected[@]}"
  exit 0
fi

printf 'Go test shard %s: %d of %d packages\n' \
  "${requested_shard}" "${#selected[@]}" "${#packages[@]}"
printf '  %s\n' "${selected[@]}"
exec go test -p 4 -tags goolm,stdjson -timeout 20m "${selected[@]}"
