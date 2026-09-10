#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
RUNTIME_DIR="$(mktemp -d "${TMPDIR:-/tmp}/picoclaw-mcp-streamable.XXXXXX")"
SERVER_BINARY="$RUNTIME_DIR/mcp-streamable-server"
READY_FILE="$RUNTIME_DIR/ready-address"
SERVER_LOG="$RUNTIME_DIR/server.log"
server_pid=""
test_pid=""
test_process_group=false

terminate_and_wait() {
  local pid="$1"
  local process_group="${2:-false}"
  local target="$pid"
  if [[ "$process_group" == "true" ]]; then
    target="-$pid"
  fi
  if kill -0 -- "$target" 2>/dev/null; then
    kill -TERM -- "$target" 2>/dev/null || true
    for ((attempt = 0; attempt < 20; attempt++)); do
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

cleanup() {
  local status="$?"
  trap - EXIT
  if [[ -n "$test_pid" ]]; then
    terminate_and_wait "$test_pid" "$test_process_group"
  fi
  if [[ -n "$server_pid" ]]; then
    terminate_and_wait "$server_pid"
  fi
  if [[ "$status" -ne 0 && -f "$SERVER_LOG" ]]; then
    sed -n '1,120p' "$SERVER_LOG" >&2
  fi
  rm -r -- "$RUNTIME_DIR" 2>/dev/null || true
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if ! command -v curl >/dev/null 2>&1; then
  echo "mcp-streamable integration suite requires curl" >&2
  exit 1
fi

cd "$ROOT_DIR"
go build -buildvcs=false -o "$SERVER_BINARY" ./integration/fixtures/mcp-streamable-server
STREAMABLE_JSON_RESPONSE=true \
STREAMABLE_LISTEN_ADDRESS=127.0.0.1:0 \
STREAMABLE_READY_FILE="$READY_FILE" \
  "$SERVER_BINARY" >"$SERVER_LOG" 2>&1 &
server_pid="$!"

server_address=""
for ((attempt = 0; attempt < 200; attempt++)); do
  if ! kill -0 "$server_pid" 2>/dev/null; then
    wait "$server_pid" || status="$?"
    server_pid=""
    echo "mcp-streamable fixture exited before readiness (status ${status:-0})" >&2
    sed -n '1,120p' "$SERVER_LOG" >&2
    exit 1
  fi
  if [[ -s "$READY_FILE" ]]; then
    IFS= read -r server_address <"$READY_FILE"
    if [[ "$server_address" == 127.0.0.1:* ]] &&
      curl --fail --silent --show-error --max-time 1 "http://$server_address/healthz" >/dev/null; then
      break
    fi
    server_address=""
  fi
  sleep 0.05
done

server_port="${server_address#127.0.0.1:}"
if [[ -z "$server_address" || "$server_address" != 127.0.0.1:* ||
  ! "$server_port" =~ ^[0-9]+$ || "$server_port" -lt 1 || "$server_port" -gt 65535 ]]; then
  echo "mcp-streamable fixture did not publish a healthy loopback address" >&2
  sed -n '1,120p' "$SERVER_LOG" >&2
  exit 1
fi

export PICOCLAW_MCP_REAL_SERVER_JSON="{\"enabled\":true,\"type\":\"http\",\"url\":\"http://$server_address/mcp\"}"
export PICOCLAW_MCP_REAL_EXPECT_TOOL_COUNT=1
export PICOCLAW_MCP_REAL_TOOL_NAME=echo
export PICOCLAW_MCP_REAL_TOOL_ARGS_JSON='{"message":"hello from host integration suite"}'
export PICOCLAW_MCP_REAL_EXPECT_SUBSTRING='hello from host integration suite'

set -m
go test ./pkg/mcp -run '^TestIntegration_RealConfiguredServer$' -v &
test_pid="$!"
test_process_group=true
set +m
if wait "$test_pid"; then
  test_pid=""
else
  status="$?"
  test_pid=""
  exit "$status"
fi
