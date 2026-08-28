import assert from "node:assert/strict"
import fs from "node:fs"
import path from "node:path"
import test from "node:test"
import { fileURLToPath } from "node:url"

import {
  evaluateCollectionDelta,
  globToRegExp,
  validateCollectionManifest,
} from "./check-collection-delta.mjs"

const scriptDir = path.dirname(fileURLToPath(import.meta.url))

function manifest(...surfaces) {
  return { version: 1, surfaces }
}

function surface(key, status, implementationGlobs = [`src/${key}/**`]) {
  return { key, status, implementationGlobs }
}

function standardSurface(key, overrides = {}) {
  const route = `/${key}`
  return {
    key,
    route,
    owningSpec: "docs/features/skills.md",
    implementationGlobs: [`src/${key}/**`],
    capabilities: ["query", "pagination", "create", "detail", "edit"],
    views: ["list", "table", "grid"],
    status: "standard",
    routes: {
      list: route,
      new: `${route}/new`,
      detail: `${route}/:id`,
      edit: `${route}/:id/edit`,
    },
    ...overrides,
  }
}

function exemptSurface(key, exemptionReason) {
  return {
    ...surface(key, "exempt"),
    exemptionReason,
  }
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
      standardSurface("new-standard"),
      exemptSurface(
        "result-log",
        "Append-only operational results are not resource administration.",
      ),
    ),
    [],
  )
  assert.deepEqual(failures, [])
})

test("the checked-in collection manifest remains valid", () => {
  const checkedInManifest = JSON.parse(
    fs.readFileSync(
      path.resolve(scriptDir, "../collection-surfaces.json"),
      "utf8",
    ),
  )
  assert.deepEqual(validateCollectionManifest(checkedInManifest), [])
})

test("repository-review child collections remain fully standard", () => {
  const checkedInManifest = JSON.parse(
    fs.readFileSync(
      path.resolve(scriptDir, "../collection-surfaces.json"),
      "utf8",
    ),
  )
  const byKey = new Map(
    checkedInManifest.surfaces.map((candidate) => [candidate.key, candidate]),
  )
  for (const key of [
    "repository-review-run-findings",
    "repository-findings",
    "repository-review-issue-previews",
  ]) {
    const candidate = byKey.get(key)
    assert.equal(candidate?.status, "standard", key)
    assert.deepEqual(candidate?.views, ["list", "table", "grid"], key)
    assert.equal(candidate?.capabilities.includes("query"), true, key)
    assert.equal(candidate?.capabilities.includes("pagination"), true, key)
  }
})

test("standard surfaces require complete metadata", () => {
  const incomplete = [
    standardSurface("missing-routes", { routes: undefined }),
    standardSurface("missing-capabilities", { capabilities: [] }),
    standardSurface("missing-views", { views: ["list", "table"] }),
    standardSurface("missing-owning-spec", { owningSpec: "" }),
    standardSurface("missing-globs", { implementationGlobs: [] }),
  ]
  assert.deepEqual(validateCollectionManifest(manifest(...incomplete)), [
    "missing-capabilities: standard collection has invalid capabilities",
    "missing-globs: standard collection must declare implementationGlobs under src/",
    "missing-owning-spec: standard collection must name a docs/features owningSpec",
    "missing-routes: capability create requires routes.new",
    "missing-routes: capability detail requires routes.detail",
    "missing-routes: capability edit requires routes.edit",
    "missing-routes: route must equal routes.list",
    "missing-routes: standard collection must declare routes.list",
    "missing-views: standard collection must support list, table, and grid views",
  ])
})

test("standard capabilities require their canonical routes", () => {
  const missingRoutes = standardSurface("profiles", {
    capabilities: [
      "query",
      "pagination",
      "create",
      "detail",
      "edit",
      "related",
      "settings",
    ],
    routes: { list: "/profiles" },
  })
  assert.deepEqual(validateCollectionManifest(manifest(missingRoutes)), [
    "profiles: capability create requires routes.new",
    "profiles: capability detail requires routes.detail",
    "profiles: capability edit requires routes.edit",
    "profiles: capability related requires routes.related",
    "profiles: capability settings requires routes.settings",
  ])
})

test("standard surfaces always declare query and pagination", () => {
  const failures = validateCollectionManifest(
    manifest(
      standardSurface("profiles", {
        capabilities: ["detail"],
        routes: { list: "/profiles", detail: "/profiles/:id" },
      }),
    ),
  )
  assert.deepEqual(failures, [
    "profiles: standard collection must declare the pagination capability",
    "profiles: standard collection must declare the query capability",
  ])
})

test("exempt surfaces require a specific reason", () => {
  assert.deepEqual(
    validateCollectionManifest(
      manifest(
        exemptSurface("missing-reason"),
        exemptSurface("generic-reason", "Not applicable"),
        exemptSurface(
          "operational-run-log",
          "Run history is an operational timeline rather than an administrative resource.",
        ),
      ),
    ),
    [
      "generic-reason: exempt collection requires a specific reason",
      "missing-reason: exempt collection requires a specific reason",
    ],
  )
})

test("manifest surfaces require unique valid keys and known statuses", () => {
  assert.deepEqual(
    validateCollectionManifest(
      manifest(
        { ...standardSurface("empty-key"), key: "" },
        { ...standardSurface("invalid-key"), key: "Invalid key" },
        { ...standardSurface("spaced-key"), key: "spaced-key " },
        standardSurface("duplicate"),
        standardSurface("duplicate"),
        surface("unknown-status", "pending"),
      ),
    ),
    [
      "duplicate key duplicate",
      "surface 0 has an invalid key",
      "surface 1 has an invalid key",
      "surface 2 has an invalid key",
      "unknown-status has an invalid status",
    ],
  )
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
      manifest(standardSurface("profiles")),
      changed,
    ),
    [],
  )
})

test("unchanged legacy debt is allowed", () => {
  const base = manifest(surface("profiles", "legacy"))
  assert.deepEqual(
    evaluateCollectionDelta(base, base, ["web/frontend/src/other/file.tsx"]),
    [],
  )
})

test("legacy surfaces cannot be removed from the manifest", () => {
  assert.deepEqual(
    evaluateCollectionDelta(
      manifest(surface("profiles", "legacy")),
      manifest(),
      ["web/frontend/src/profiles/profile-card.tsx"],
    ),
    ["profiles: legacy collection cannot be removed from the manifest"],
  )
})

test("standard surfaces cannot be removed from the manifest", () => {
  assert.deepEqual(
    evaluateCollectionDelta(
      manifest(standardSurface("agents")),
      manifest(),
      [],
    ),
    ["agents: standard collection cannot be removed from the manifest"],
  )
})

test("standard status cannot regress", () => {
  const failures = evaluateCollectionDelta(
    manifest(surface("agents", "standard")),
    manifest(
      exemptSurface(
        "agents",
        "Operational agent output is not an administrative resource.",
      ),
    ),
    [],
  )
  assert.deepEqual(failures, [
    "agents: standard collection cannot regress to exempt",
  ])
})
