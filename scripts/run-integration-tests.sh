#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASE_COMPOSE="$ROOT_DIR/integration/docker-compose.runner.yml"
SUITES_DIR="$ROOT_DIR/integration/suites"

if [[ ! -d "$SUITES_DIR" ]]; then
  echo "missing integration suites directory: $SUITES_DIR" >&2
  exit 1
fi

resolve_cache_directory() {
  local name="$1"
  local configured="$2"
  local fallback="$3"
  local path="${configured:-$fallback}"

  if [[ "$path" == *$'\n'* || "$path" == *$'\r'* ]]; then
    echo "$name contains a line break" >&2
    return 1
  fi
  if [[ "$path" != /* ]]; then
    echo "$name must be an absolute path: $path" >&2
    return 1
  fi
  if [[ -e "$path" && ! -d "$path" ]]; then
    echo "$name is not a directory: $path" >&2
    return 1
  fi
  mkdir -p "$path"
  path="$(cd "$path" && pwd -P)"
  if [[ "$path" == "/" ]]; then
    echo "$name resolves to an unsafe directory: $path" >&2
    return 1
  fi
  if [[ ! -w "$path" ]]; then
    echo "$name is not writable: $path" >&2
    return 1
  fi
  printf '%s\n' "$path"
}

integration_gocache="$(resolve_cache_directory \
  INTEGRATION_GOCACHE "${INTEGRATION_GOCACHE:-}" "$ROOT_DIR/.cache/go-build")"
integration_gomodcache="$(resolve_cache_directory \
  INTEGRATION_GOMODCACHE "${INTEGRATION_GOMODCACHE:-}" "$ROOT_DIR/.cache/go-mod")"
if [[ "$integration_gocache" == "$integration_gomodcache" ||
  "$integration_gocache" == "$integration_gomodcache/"* ||
  "$integration_gomodcache" == "$integration_gocache/"* ]]; then
  echo "integration Go build and module caches must not overlap" >&2
  exit 1
fi
readonly INTEGRATION_GOCACHE="$integration_gocache"
readonly INTEGRATION_GOMODCACHE="$integration_gomodcache"
export INTEGRATION_GOCACHE INTEGRATION_GOMODCACHE
readonly requested_integration_runner_uid="${INTEGRATION_RUNNER_UID:-}"
readonly requested_integration_runner_gid="${INTEGRATION_RUNNER_GID:-}"
active_suite_pid=""

terminate_suite_and_wait() {
  local pid="$1"
  local target="-$pid"
  if ! kill -0 -- "$target" 2>/dev/null; then
    target="$pid"
  fi
  if kill -0 -- "$target" 2>/dev/null; then
    kill -TERM -- "$target" 2>/dev/null || true
    # A just-launched background group may have stopped on terminal input
    # before `fg` transferred the terminal. Resume it so TERM cleanup runs.
    kill -CONT -- "$target" 2>/dev/null || true
    # Nested suite cleanup has its own bounded escalation, while Docker
    # teardown may use the engine's ten-second stop grace. Do not preempt
    # either cleanup boundary with an equally short outer deadline.
    for ((attempt = 0; attempt < 600; attempt++)); do
      if ! kill -0 -- "$target" 2>/dev/null; then
        break
      fi
      sleep 0.05
    done
    if kill -0 -- "$target" 2>/dev/null; then
      kill -KILL -- "$target" 2>/dev/null || true
    fi
  fi
  wait "$pid" 2>/dev/null || true
}

handle_runner_signal() {
  local status="$1"
  local pid="$active_suite_pid"
  trap - INT TERM
  if [[ -z "$pid" ]]; then
    pid="$(jobs -p | head -n 1 || true)"
  fi
  if [[ -n "$pid" ]]; then
    terminate_suite_and_wait "$pid"
    active_suite_pid=""
  fi
  exit "$status"
}

trap 'handle_runner_signal 130' INT
trap 'handle_runner_signal 143' TERM

resolve_runner_identity() {
  local configured_uid="$requested_integration_runner_uid"
  local configured_gid="$requested_integration_runner_gid"
  if [[ -n "$configured_uid" || -n "$configured_gid" ]]; then
    if [[ -z "$configured_uid" || -z "$configured_gid" ]]; then
      echo "INTEGRATION_RUNNER_UID and INTEGRATION_RUNNER_GID must be set together" >&2
      return 1
    fi
    printf '%s %s\n' "$configured_uid" "$configured_gid"
    return
  fi

  if [[ "$(uname -s)" != "Linux" ]]; then
    printf '0 0\n'
    return
  fi

  local engine_details
  if engine_details="$(
    docker info --format '{{json .SecurityOptions}} {{.OperatingSystem}} {{.Name}}' 2>/dev/null
  )"; then
    engine_details="${engine_details,,}"
    if [[ "$engine_details" == *rootless* || "$engine_details" == *"docker desktop"* ||
      "$engine_details" == *docker-desktop* ]]; then
      printf '0 0\n'
      return
    fi
    if [[ "$engine_details" == *userns* ]]; then
      echo "Docker userns-remap requires explicit integration runner UID and GID" >&2
      return 1
    fi
  else
    local podman_rootless
    podman_rootless="$(docker info --format '{{.Host.Security.Rootless}}' 2>/dev/null || true)"
    if [[ "${podman_rootless,,}" == "true" ]]; then
      printf '0 0\n'
      return
    fi
  fi
  printf '%s %s\n' "$(id -u)" "$(id -g)"
}

sanitize_project_name() {
  printf '%s' "$1" | tr '[:upper:]' '[:lower:]' | tr -cs 'a-z0-9' '-'
}

collect_suite_dirs() {
  if [[ "$#" -gt 0 ]]; then
    local suite
    for suite in "$@"; do
      printf '%s\n' "$SUITES_DIR/$suite"
    done
    return
  fi

  find "$SUITES_DIR" -mindepth 1 -maxdepth 1 -type d | sort
}

run_suite() {
  local suite_dir="$1"
  local suite_name
  suite_name="$(basename "$suite_dir")"
  local manifest="$suite_dir/suite.env"
  local -r project_namespace="${INTEGRATION_COMPOSE_PROJECT_NAMESPACE:-}"
  local -r integration_gomaxprocs="${INTEGRATION_GOMAXPROCS:-2}"
  local project_identity="${project_namespace:+${project_namespace}-}${suite_name}"

  if [[ ! -f "$manifest" ]]; then
    echo "suite $suite_name is missing manifest: $manifest" >&2
    return 1
  fi

  local compose_args=()
  compose_args+=(--project-directory "$ROOT_DIR" -p "picoclaw-int-$(sanitize_project_name "$project_identity")")
  compose_args+=(-f "$BASE_COMPOSE")

  local compose_files=()
  while IFS= read -r compose_file; do
    compose_files+=("$compose_file")
    compose_args+=(-f "$compose_file")
  done < <(find "$suite_dir" -maxdepth 1 -type f \( -name 'docker-compose.yml' -o -name 'docker-compose.*.yml' \) | sort)

  (
    set -a
    # shellcheck disable=SC1090
    source "$manifest"
    set +a
    export INTEGRATION_REPO_ROOT="$ROOT_DIR"

    : "${TEST_COMMAND:?suite $suite_name must define TEST_COMMAND in $manifest}"
    runner_mode="${RUNNER_MODE:-docker}"
    if [[ "$runner_mode" != "host" && "$runner_mode" != "docker" ]]; then
      echo "suite $suite_name has invalid RUNNER_MODE: $runner_mode" >&2
      return 1
    fi
    if [[ "$runner_mode" == "host" && -n "${RUNNER_SERVICE:-}" ]]; then
      echo "suite $suite_name cannot set RUNNER_SERVICE in host mode" >&2
      return 1
    fi

    local run_env=()
    local host_cover_profile=""
    if [[ -n "${INTEGRATION_COVERPROFILE_DIR:-}" ]]; then
      local cover_profile="$INTEGRATION_COVERPROFILE_DIR/$suite_name.cover.out"
      local host_cover_dir=""
      if [[ "$INTEGRATION_COVERPROFILE_DIR" == /workspace/* ]]; then
        host_cover_dir="$ROOT_DIR/${INTEGRATION_COVERPROFILE_DIR#/workspace/}"
      else
        host_cover_dir="$INTEGRATION_COVERPROFILE_DIR"
      fi
      mkdir -p "$host_cover_dir"
      host_cover_profile="$host_cover_dir/$suite_name.cover.out"
      if [[ "$runner_mode" == "docker" ]]; then
        run_env+=(-e "INTEGRATION_COVERPKG=${INTEGRATION_COVERPKG:-}")
        run_env+=(-e "INTEGRATION_COVERPROFILE=$cover_profile")
        run_env+=(-e "GOMAXPROCS=$integration_gomaxprocs")
      fi
    fi

    local run_command='go() {
  if [[ "$1" == "test" ]]; then
    shift
    local argument
    for argument in "$@"; do
      case "$argument" in
        -count=1 | --count=1 | -test.count=1 | --test.count=1) ;;
        -count | --count | -test.count | --test.count | \
          -count=* | --count=* | -test.count=* | --test.count=*)
          printf "integration suites cannot override go test -count=1: %s\n" "$argument" >&2
          return 2
          ;;
      esac
    done
    local test_args=(-buildvcs=false -count=1)
    if [[ -n "${INTEGRATION_COVERPROFILE:-}" ]]; then
      test_args+=(-covermode=atomic -coverprofile="$INTEGRATION_COVERPROFILE")
      if [[ -n "${INTEGRATION_COVERPKG:-}" ]]; then
        test_args+=(-coverpkg="$INTEGRATION_COVERPKG")
      fi
    fi
    command go test "${test_args[@]}" "$@"
  else
    command go "$@"
  fi
}
export -f go
'"$TEST_COMMAND"

    if [[ "$runner_mode" == "host" ]]; then
      export GOCACHE="$INTEGRATION_GOCACHE"
      export GOMODCACHE="$INTEGRATION_GOMODCACHE"
      export GOTOOLCHAIN=auto
      export CGO_ENABLED=0
      export GOFLAGS="${INTEGRATION_GOFLAGS:--tags=goolm,stdjson,integration}"
      if [[ -n "$host_cover_profile" ]]; then
        export INTEGRATION_COVERPKG="${INTEGRATION_COVERPKG:-}"
        export INTEGRATION_COVERPROFILE="$host_cover_profile"
        export GOMAXPROCS="$integration_gomaxprocs"
      fi
      echo "==> [$suite_name] running on host: $TEST_COMMAND"
      cd "$ROOT_DIR"
      bash -c "$run_command"
      return
    fi

    if [[ ! -f "$BASE_COMPOSE" ]]; then
      echo "missing base compose file: $BASE_COMPOSE" >&2
      return 1
    fi
    if [[ "${#compose_files[@]}" -eq 0 ]]; then
      echo "suite $suite_name has no docker-compose file" >&2
      return 1
    fi

    local runner_identity integration_runner_uid integration_runner_gid
    runner_identity="$(resolve_runner_identity)"
    read -r integration_runner_uid integration_runner_gid <<< "$runner_identity"
    if [[ ! "$integration_runner_uid" =~ ^[0-9]+$ || ! "$integration_runner_gid" =~ ^[0-9]+$ ]]; then
      echo "integration runner UID and GID must be numeric" >&2
      return 1
    fi
    readonly INTEGRATION_RUNNER_UID="$integration_runner_uid"
    readonly INTEGRATION_RUNNER_GID="$integration_runner_gid"
    export INTEGRATION_RUNNER_UID INTEGRATION_RUNNER_GID

    runner_service="${RUNNER_SERVICE:-integration-runner}"
    cleanup() {
      local cleanup_args=(down -v --remove-orphans)
      if [[ -n "$project_namespace" ]]; then
        cleanup_args+=(--rmi local)
      fi
      docker compose "${compose_args[@]}" "${cleanup_args[@]}" >/dev/null 2>&1 || true
    }
    trap cleanup EXIT

    echo "==> [$suite_name] resolving services"
    local services=()
    while IFS= read -r service; do
      services+=("$service")
    done < <(docker compose "${compose_args[@]}" config --services)

    local dependency_services=()
    for service in "${services[@]}"; do
      if [[ "$service" != "$runner_service" ]]; then
        dependency_services+=("$service")
      fi
    done

    if [[ "${#dependency_services[@]}" -gt 0 ]]; then
      echo "==> [$suite_name] starting docker services: ${dependency_services[*]}"
      docker compose "${compose_args[@]}" up -d --build --wait "${dependency_services[@]}"
    fi

    echo "==> [$suite_name] running: $TEST_COMMAND"
    # integration-runner already uses `bash -c` as its entrypoint, so pass the
    # suite command as a single argument for Bash to execute directly.
    docker compose "${compose_args[@]}" run --rm -T "${run_env[@]}" "$runner_service" "$run_command"
  )
}

run_suite_supervised() {
  local suite_dir="$1"
  local status=0

  # Job control gives the suite its own process group. Integration suites are
  # automation and explicitly receive no terminal input, so the parent can
  # retain a PID that its signal traps supervise without risking SIGTTIN.
  set -m
  run_suite "$suite_dir" </dev/null &
  active_suite_pid="$!"
  set +m
  if wait "$active_suite_pid"; then
    status=0
  else
    status="$?"
  fi
  active_suite_pid=""
  return "$status"
}

main() {
  local suite_dirs=()
  while IFS= read -r suite_dir; do
    suite_dirs+=("$suite_dir")
  done < <(collect_suite_dirs "$@")

  if [[ "${#suite_dirs[@]}" -eq 0 ]]; then
    echo "no integration suites found" >&2
    exit 1
  fi

  local suite_dir
  for suite_dir in "${suite_dirs[@]}"; do
    if [[ ! -d "$suite_dir" ]]; then
      echo "unknown integration suite: $suite_dir" >&2
      exit 1
    fi
    run_suite_supervised "$suite_dir"
  done
}

main "$@"
