# Coding-agent benchmark assets

The tracked smoke fixture lives under
`integration/fixtures/coding-agent-benchmark/transfer-idempotency-v1`. That
directory is the complete model-visible checkout. Hidden tests, reference
solutions, fixed mutants, and grading code live here so fixture preparation
cannot accidentally disclose them to the coding agent.

The fixture's `benchmark.yaml` declares the required format, vet, normal-test,
and race-test steps and pins the benchmark reasoning effort to `low`. Its
`.picoclaw/ci.yaml` contains the same four-step plan in
the format consumed by the production sandboxed LocalCI discovery path.

The Gateway-package harness lives in
`pkg/gateway/coding_agent_benchmark_test.go`. Ordinary CI uses its scripted
provider and deterministic validation feedback path; explicit environment
opt-ins run the same lifecycle with the production LocalCI sandbox or an
authenticated provider. Every mode stops at the publication gate.

## Offline verification

Ordinary CI can verify LocalCI discovery, hidden behavior, every fixed mutant,
grader cleanup, and the version 2 grader artifact without model or network
access:

```bash
go test ./integration/codingagentbenchmark/transfer-idempotency-v1
```

The grader accepts a Git worktree, a new output path outside that worktree, and
an optional exact expected commit:

```bash
integration/codingagentbenchmark/transfer-idempotency-v1/grade.sh \
  /absolute/candidate-checkout /absolute/new-private-output "$EXPECTED_COMMIT"
```

The caller is responsible for running untrusted candidates through the
production sandbox. The script itself does not grant network or shell authority;
its Go commands force `GOPROXY=off` and `GOWORK=off`.

The hidden contract gives replay resolution this precedence: validate
`requestID`, resolve an already-successful ID as exact replay or conflict, then
validate the remaining arguments for an unseen ID. Mutation points are awarded
only when candidate-authored tests kill the external replay-precedence,
conflict, failed-ID-reuse, overflow, and locking mutants. Hidden tests are
removed before mutation runs and are never copied into the visible fixture.
