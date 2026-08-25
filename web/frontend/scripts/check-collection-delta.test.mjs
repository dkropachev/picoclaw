import assert from "node:assert/strict"
import test from "node:test"

import {
  evaluateCollectionDelta,
  globToRegExp,
} from "./check-collection-delta.mjs"

function manifest(...surfaces) {
  return { version: 1, surfaces }
}

function surface(key, status, implementationGlobs = [`src/${key}/**`]) {
  return { key, status, implementationGlobs }
}

test("glob matching keeps a double-star recursive", () => {
  const pattern = globToRegExp("web/frontend/src/components/example/**")
  assert.equal(
    pattern.test("web/frontend/src/components/example/nested/page.tsx"),
    true,
  )
  assert.equal(
    pattern.test("web/frontend/src/components/other/page.tsx"),
    false,
  )
})

test("new standard and exempt surfaces are allowed", () => {
  const failures = evaluateCollectionDelta(
    manifest(),
    manifest(
      surface("new-standard", "standard"),
      surface("result-log", "exempt"),
    ),
    [],
  )
  assert.deepEqual(failures, [])
})

test("new legacy surfaces are rejected", () => {
  const failures = evaluateCollectionDelta(
    manifest(),
    manifest(surface("new-debt", "legacy")),
    [],
  )
  assert.deepEqual(failures, [
    "new-debt: new collection surfaces cannot be registered as legacy",
  ])
})

test("modified legacy implementation must become standard", () => {
  const base = manifest(surface("profiles", "legacy"))
  const changed = ["web/frontend/src/profiles/profile-card.tsx"]
  assert.deepEqual(
    evaluateCollectionDelta(
      base,
      manifest(surface("profiles", "legacy")),
      changed,
    ),
    [
      "profiles: modified legacy collection implementation must migrate to standard",
    ],
  )
  assert.deepEqual(
    evaluateCollectionDelta(
      base,
      manifest(surface("profiles", "standard")),
      changed,
    ),
    [],
  )
})

test("unchanged legacy debt and removed legacy surfaces are allowed", () => {
  const base = manifest(surface("profiles", "legacy"))
  assert.deepEqual(
    evaluateCollectionDelta(base, base, ["web/frontend/src/other/file.tsx"]),
    [],
  )
  assert.deepEqual(
    evaluateCollectionDelta(base, manifest(), [
      "web/frontend/src/profiles/profile-card.tsx",
    ]),
    [],
  )
})

test("standard status cannot regress", () => {
  const failures = evaluateCollectionDelta(
    manifest(surface("agents", "standard")),
    manifest(surface("agents", "exempt")),
    [],
  )
  assert.deepEqual(failures, [
    "agents: standard collection cannot regress to exempt",
  ])
})
