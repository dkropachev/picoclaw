#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
  echo "usage: grade.sh CHECKOUT OUTPUT_DIR [EXPECTED_COMMIT]" >&2
  exit 2
fi

for dependency in git go gofmt jq realpath sha256sum; do
  if ! command -v "$dependency" >/dev/null 2>&1; then
    echo "grader dependency is unavailable: $dependency" >&2
    exit 2
  fi
done

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
checkout="$(realpath "$1")"
output="$(realpath -m "$2")"
expected_commit="${3:-}"
hidden="$script_dir/testdata/hidden/ledger_hidden_test.go"
mutants_root="$script_dir/testdata/mutants"
ledger_dir="$checkout/ledger"
target="$ledger_dir/ledger_hidden_test.go"
patch_index=""
mutation_root=""
hidden_installed=0

if [ -e "$output" ] || [ -L "$output" ]; then
  echo "grader output must not already exist" >&2
  exit 2
fi
case "$output/" in
  "$checkout/"*)
    echo "grader output must be outside the candidate checkout" >&2
    exit 2
    ;;
esac
if [ -n "$expected_commit" ] && [[ ! "$expected_commit" =~ ^[0-9a-f]{40}$ ]]; then
  echo "expected commit is invalid" >&2
  exit 2
fi
if [ ! -f "$hidden" ] || [ -L "$hidden" ] || [ ! -d "$mutants_root" ] ||
  [ -e "$target" ] || [ -L "$target" ]; then
  echo "grader input is invalid" >&2
  exit 2
fi
repository_root="$(git -C "$checkout" rev-parse --show-toplevel 2>/dev/null || true)"
if [ "$repository_root" != "$checkout" ]; then
  echo "checkout must be the root of one Git worktree" >&2
  exit 2
fi
ledger_root="$(realpath "$ledger_dir" 2>/dev/null || true)"
if [ ! -d "$ledger_dir" ] || [ -L "$ledger_dir" ] || [ "$ledger_root" != "$ledger_dir" ]; then
  echo "checkout ledger must be a real in-worktree directory" >&2
  exit 2
fi

mkdir -m 700 -p "$output"

cleanup() {
  if [ "$hidden_installed" -eq 1 ]; then
    rm -f -- "$target" || true
  fi
  if [ -n "$patch_index" ]; then
    rm -f -- "$patch_index" || true
  fi
  if [ -n "$mutation_root" ]; then
    rm -rf -- "$mutation_root" || true
  fi
}
trap cleanup EXIT INT TERM

cp -- "$hidden" "$target"
chmod 600 "$target"
hidden_installed=1

run_check() {
  local name="$1"
  shift
  if (cd "$checkout" && env GOWORK=off GOPROXY=off "$@") >"$output/$name.log" 2>&1; then
    printf 1
  else
    printf 0
  fi
}

format_ok=1
if format_files="$(cd "$checkout" && gofmt -l . 2>"$output/gofmt.log")"; then
  if [ -n "$format_files" ]; then
    format_ok=0
    printf '%s\n' "$format_files" >>"$output/gofmt.log"
  fi
else
  format_ok=0
fi

vet_ok="$(run_check vet go vet ./...)"
test_ok="$(run_check test go test -count=1 ./...)"
race_ok="$(run_check race go test -race -count=1 ./...)"

rm -f -- "$target"
hidden_installed=0

head_after="$(git -C "$checkout" rev-parse HEAD 2>/dev/null || true)"
head_ok=0
if [[ "$head_after" =~ ^[0-9a-f]{40}$ ]] &&
  { [ -z "$expected_commit" ] || [ "$expected_commit" = "$head_after" ]; }; then
  head_ok=1
fi

changed="$(
  cd "$checkout" || exit 1
  { git diff --name-only HEAD; git ls-files --others --exclude-standard; } | sort -u
)"
scope_ok=1
while IFS= read -r path; do
  [ -z "$path" ] && continue
  case "$path" in
    ledger/*.go)
      relative="${path#ledger/}"
      if [[ "$relative" == */* ]] || [ -L "$checkout/$path" ]; then
        scope_ok=0
      fi
      ;;
    *) scope_ok=0 ;;
  esac
done <<<"$changed"
if ! git -C "$checkout" diff --quiet HEAD -- go.mod go.sum 2>/dev/null; then
  scope_ok=0
fi

tests_changed=0
if ! git -C "$checkout" diff --quiet HEAD -- 'ledger/*_test.go'; then
  tests_changed=1
else
  while IFS= read -r -d '' path; do
    if [[ "${path#ledger/}" != */* ]] && [ ! -L "$checkout/$path" ]; then
      tests_changed=1
      break
    fi
  done < <(git -C "$checkout" ls-files -z --others --exclude-standard -- 'ledger/*_test.go')
fi

mutation_root="$(mktemp -d "$output/.mutation-work.XXXXXX")"
mutation_base="$mutation_root/base"
mkdir -m 700 -p "$mutation_base"
copy_ok=1
while IFS= read -r -d '' path; do
  if [[ "$path" == /* || "$path" == ".." || "$path" == ../* || "$path" == */../* ]] ||
    [ -L "$checkout/$path" ]; then
    copy_ok=0
    break
  fi
  if [ ! -e "$checkout/$path" ]; then
    continue
  fi
  if [ ! -f "$checkout/$path" ]; then
    copy_ok=0
    break
  fi
  mkdir -p "$mutation_base/$(dirname "$path")"
  cp -- "$checkout/$path" "$mutation_base/$path"
done < <(git -C "$checkout" ls-files -z --cached --others --exclude-standard)
if [ "$copy_ok" -ne 1 ]; then
  scope_ok=0
fi

mutants_jsonl="$output/.mutants.jsonl"
: >"$mutants_jsonl"
mutant_total=0
mutant_killed=0
while IFS= read -r mutant; do
  [ -z "$mutant" ] && continue
  mutant_id="$(basename "$(dirname "$mutant")")"
  mutant_total=$((mutant_total + 1))
  killed=0
  if [ "$tests_changed" -eq 1 ] && [ "$copy_ok" -eq 1 ]; then
    mutant_checkout="$mutation_root/run-$mutant_id"
    cp -R -- "$mutation_base" "$mutant_checkout"
    cp -- "$mutant" "$mutant_checkout/ledger/ledger.go"
    if ! (cd "$mutant_checkout" && env GOWORK=off GOPROXY=off go test -race -count=1 ./...) \
      >"$output/mutant-$mutant_id.log" 2>&1; then
      killed=1
      mutant_killed=$((mutant_killed + 1))
    fi
  else
    printf 'candidate tests were not eligible for mutation execution\n' \
      >"$output/mutant-$mutant_id.log"
  fi
  jq -cn --arg id "$mutant_id" --argjson killed "$killed" \
    '{id:$id,killed:($killed == 1)}' >>"$mutants_jsonl"
done < <(find "$mutants_root" -mindepth 2 -maxdepth 2 -type f -name ledger.go | sort)

if [ "$mutant_total" -eq 0 ]; then
  echo "grader has no mutation fixtures" >&2
  exit 2
fi
mutation_points=$((10 * mutant_killed / mutant_total))
mutants_json="$(jq -sc '.' "$mutants_jsonl")"
rm -f -- "$mutants_jsonl"

build_points=$((10 * test_ok))
functional_points=$((35 * test_ok))
atomic_points=$((25 * race_ok))
scope_points=$((10 * scope_ok * head_ok))
verification_ok=$((format_ok * vet_ok))
verification_points=$((10 * verification_ok))
score=$((build_points + functional_points + atomic_points + mutation_points + scope_points + verification_points))
mandatory_pass=$((format_ok * vet_ok * test_ok * race_ok * scope_ok * head_ok))
if [ "$mandatory_pass" -eq 0 ] && [ "$score" -gt 59 ]; then
  score=59
fi

untracked_paths=()
while IFS= read -r -d '' path; do
  untracked_paths+=("$path")
done < <(git -C "$checkout" ls-files -z --others --exclude-standard)
patch_index="$output/.grader-patch-index"
if [ -e "$patch_index" ]; then
  echo "grader patch index already exists" >&2
  exit 2
fi
patch_sha="$(
  (
    set -e
    cd "$checkout"
    GIT_INDEX_FILE="$patch_index" git read-tree HEAD
    if [ "${#untracked_paths[@]}" -gt 0 ]; then
      GIT_INDEX_FILE="$patch_index" git add --intent-to-add -- "${untracked_paths[@]}"
    fi
    GIT_INDEX_FILE="$patch_index" git diff --binary HEAD
  ) | sha256sum | awk '{print $1}'
)"
rm -f -- "$patch_index"
patch_index=""

grader_sha="$(sha256sum "$0" | awk '{print $1}')"
hidden_sha="$(sha256sum "$hidden" | awk '{print $1}')"
mutants_sha="$(
  (
    cd "$mutants_root" || exit 1
    while IFS= read -r path; do
      printf '%s\0' "$path"
      sha256sum "$path"
    done < <(find . -mindepth 2 -maxdepth 2 -type f -name ledger.go | sort)
  ) | sha256sum | awk '{print $1}'
)"
changed_json="$(printf '%s\n' "$changed" | jq -Rsc 'split("\n") | map(select(length > 0))')"

jq -n \
  --arg fixture "transfer-idempotency-v1" \
  --arg patch_sha256 "sha256:$patch_sha" \
  --arg grader_sha256 "sha256:$grader_sha" \
  --arg hidden_test_sha256 "sha256:$hidden_sha" \
  --arg mutants_sha256 "sha256:$mutants_sha" \
  --argjson score "$score" \
  --argjson mandatory_pass "$mandatory_pass" \
  --argjson format_ok "$format_ok" \
  --argjson vet_ok "$vet_ok" \
  --argjson test_ok "$test_ok" \
  --argjson race_ok "$race_ok" \
  --argjson scope_ok "$scope_ok" \
  --argjson head_ok "$head_ok" \
  --argjson tests_changed "$tests_changed" \
  --argjson mutant_killed "$mutant_killed" \
  --argjson mutant_total "$mutant_total" \
  --argjson mutation_points "$mutation_points" \
  --argjson mutants "$mutants_json" \
  --argjson changed_files "$changed_json" \
  '{version:2, fixture:$fixture, score:$score, mandatory_pass:($mandatory_pass == 1),
    checks:{format:($format_ok == 1), vet:($vet_ok == 1), test:($test_ok == 1),
      race:($race_ok == 1), scope:($scope_ok == 1), git_head:($head_ok == 1),
      tests_changed:($tests_changed == 1)},
    mutation:{killed:$mutant_killed,total:$mutant_total,points:$mutation_points,mutants:$mutants},
    patch_sha256:$patch_sha256, grader_sha256:$grader_sha256,
    hidden_test_sha256:$hidden_test_sha256, mutants_sha256:$mutants_sha256,
    changed_files:$changed_files}' >"$output/grader.json"
find "$output" -maxdepth 1 -type f -exec chmod 600 {} +
cat "$output/grader.json"

if [ "$mandatory_pass" -ne 1 ]; then
  exit 1
fi
