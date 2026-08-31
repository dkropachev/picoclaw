#!/usr/bin/env node

import { execFileSync } from "node:child_process"
import path from "node:path"
import process from "node:process"
import { fileURLToPath, pathToFileURL } from "node:url"

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const frontendRoot = path.resolve(scriptDir, "..")
const repoRoot = path.resolve(frontendRoot, "../..")
const defaultManifest = "web/frontend/collection-surfaces.json"
const allowedStatuses = new Set(["standard", "legacy", "exempt"])
const allowedCapabilities = new Set([
  "query",
  "pagination",
  "selection",
  "bulk-delete",
  "bulk-retry",
  "create",
  "detail",
  "edit",
  "related",
  "settings",
])
const allowedViews = new Set(["list", "table", "grid"])
const requiredStandardCapabilities = ["query", "pagination"]
const requiredStandardViews = [...allowedViews]
const capabilityRoutes = {
  create: "new",
  detail: "detail",
  edit: "edit",
  related: "related",
  settings: "settings",
}

export function globToRegExp(pattern) {
  const escaped = pattern
    .replace(/[.+^${}()|[\]\\]/g, "\\$&")
    .replace(/\*\*/g, "\0")
    .replace(/\*/g, "[^/]*")
    .replace(/\?/g, "[^/]")
    .replace(/\0/g, ".*")
  return new RegExp(`^${escaped}$`)
}

function normalizePath(value) {
  return value.trim().replaceAll("\\", "/").replace(/^\.\//, "")
}

function implementationPatterns(surface) {
  return (surface.implementationGlobs ?? []).map((pattern) => {
    const normalized = normalizePath(pattern)
    return normalized.startsWith("web/frontend/")
      ? normalized
      : `web/frontend/${normalized}`
  })
}

function surfaceWasModified(surface, changedFiles) {
  const patterns = implementationPatterns(surface).map(globToRegExp)
  return changedFiles.some((file) =>
    patterns.some((pattern) => pattern.test(normalizePath(file))),
  )
}

function isRoute(value) {
  return typeof value === "string" && value.startsWith("/")
}

function hasRoute(routes, routeKey) {
  const value = routes?.[routeKey]
  if (routeKey === "related") {
    return Array.isArray(value) && value.length > 0 && value.every(isRoute)
  }
  return isRoute(value)
}

function validateStandardSurface(surface) {
  const failures = []
  const key = surface.key
  const routes =
    surface.routes &&
    typeof surface.routes === "object" &&
    !Array.isArray(surface.routes)
      ? surface.routes
      : undefined

  if (!isRoute(routes?.list)) {
    failures.push(`${key}: standard collection must declare routes.list`)
  }
  if (!isRoute(surface.route) || surface.route !== routes?.list) {
    failures.push(`${key}: route must equal routes.list`)
  }

  const capabilities = surface.capabilities
  if (
    !Array.isArray(capabilities) ||
    capabilities.length === 0 ||
    capabilities.some(
      (capability) =>
        typeof capability !== "string" || !allowedCapabilities.has(capability),
    )
  ) {
    failures.push(`${key}: standard collection has invalid capabilities`)
  } else {
    for (const capability of requiredStandardCapabilities) {
      if (!capabilities.includes(capability)) {
        failures.push(
          `${key}: standard collection must declare the ${capability} capability`,
        )
      }
    }
    for (const [capability, routeKey] of Object.entries(capabilityRoutes)) {
      if (capabilities.includes(capability) && !hasRoute(routes, routeKey)) {
        failures.push(
          `${key}: capability ${capability} requires routes.${routeKey}`,
        )
      }
    }
  }

  if (
    !Array.isArray(surface.views) ||
    surface.views.some(
      (view) => typeof view !== "string" || !allowedViews.has(view),
    ) ||
    requiredStandardViews.some((view) => !surface.views.includes(view))
  ) {
    failures.push(
      `${key}: standard collection must support list, table, and grid views`,
    )
  }

  if (
    typeof surface.owningSpec !== "string" ||
    !/^docs\/features\/[a-z0-9-]+\.md$/.test(surface.owningSpec)
  ) {
    failures.push(
      `${key}: standard collection must name a docs/features owningSpec`,
    )
  }

  if (
    !Array.isArray(surface.implementationGlobs) ||
    surface.implementationGlobs.length === 0 ||
    surface.implementationGlobs.some(
      (pattern) => typeof pattern !== "string" || !pattern.startsWith("src/"),
    )
  ) {
    failures.push(
      `${key}: standard collection must declare implementationGlobs under src/`,
    )
  }

  return failures
}

export function validateCollectionManifest(manifest) {
  const failures = []
  const keys = new Set()
  for (const [index, surface] of (manifest.surfaces ?? []).entries()) {
    const label = `surface ${index}`
    if (!surface || typeof surface !== "object" || Array.isArray(surface)) {
      failures.push(`${label} is not an object`)
      continue
    }
    const rawKey = typeof surface.key === "string" ? surface.key : ""
    const key = rawKey.trim()
    if (rawKey !== key || !/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(key)) {
      failures.push(`${label} has an invalid key`)
    } else if (keys.has(key)) {
      failures.push(`duplicate key ${key}`)
    } else {
      keys.add(key)
    }
    if (!allowedStatuses.has(surface.status)) {
      failures.push(`${key || label} has an invalid status`)
    }
    if (surface.status === "standard") {
      failures.push(...validateStandardSurface(surface))
    }
    if (
      surface.status === "exempt" &&
      (typeof surface.exemptionReason !== "string" ||
        surface.exemptionReason.trim().length < 20)
    ) {
      failures.push(
        `${surface.key}: exempt collection requires a specific reason`,
      )
    }
  }
  return [...new Set(failures)].sort()
}

export function evaluateCollectionDelta(
  baseManifest,
  headManifest,
  changedFiles,
) {
  const failures = validateCollectionManifest(headManifest)
  const baseByKey = new Map(
    (baseManifest.surfaces ?? []).map((surface) => [surface.key, surface]),
  )
  const headByKey = new Map(
    (headManifest.surfaces ?? []).map((surface) => [surface.key, surface]),
  )

  for (const head of headManifest.surfaces ?? []) {
    const base = baseByKey.get(head.key)
    if (!base && head.status === "legacy") {
      failures.push(
        `${head.key}: new collection surfaces cannot be registered as legacy`,
      )
    }
    if (base?.status === "standard" && head.status !== "standard") {
      failures.push(
        `${head.key}: standard collection cannot regress to ${head.status}`,
      )
    }
  }

  for (const base of baseManifest.surfaces ?? []) {
    const head = headByKey.get(base.key)
    if ((base.status === "standard" || base.status === "legacy") && !head) {
      failures.push(
        `${base.key}: ${base.status} collection cannot be removed from the manifest`,
      )
      continue
    }
    if (base.status !== "legacy" || !surfaceWasModified(base, changedFiles)) {
      continue
    }
    if (head.status !== "standard") {
      failures.push(
        `${base.key}: modified legacy collection implementation must migrate to standard`,
      )
    }
  }

  return [...new Set(failures)].sort()
}

function parseArgs(argv) {
  const options = {
    base:
      process.env.BASE_REF ||
      (process.env.GITHUB_BASE_REF
        ? `origin/${process.env.GITHUB_BASE_REF}`
        : "origin/main"),
    head: process.env.HEAD_REF || "HEAD",
    manifest: defaultManifest,
  }
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index]
    if (!["--base", "--head", "--manifest"].includes(argument)) {
      throw new Error(`unknown argument ${argument}`)
    }
    const value = argv[index + 1]
    if (!value) throw new Error(`${argument} requires a value`)
    options[argument.slice(2)] = value
    index += 1
  }
  return options
}

function git(args, options = {}) {
  return execFileSync("git", ["-C", repoRoot, ...args], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", options.allowFailure ? "ignore" : "pipe"],
  })
}

function readManifestAt(ref, manifestPath, allowMissing = false) {
  let source
  try {
    source = git(["show", `${ref}:${manifestPath}`])
  } catch (error) {
    if (allowMissing) return undefined
    throw new Error(
      `cannot read ${manifestPath} at ${ref}: ${error instanceof Error ? error.message : String(error)}`,
    )
  }
  try {
    return JSON.parse(source)
  } catch (error) {
    throw new Error(
      `cannot parse ${manifestPath} at ${ref}: ${error instanceof Error ? error.message : String(error)}`,
    )
  }
}

function changedFiles(base, head) {
  return git([
    "diff",
    "--name-only",
    "--diff-filter=ACMRTD",
    `${base}...${head}`,
  ])
    .split(/\r?\n/)
    .map(normalizePath)
    .filter(Boolean)
}

function main() {
  const options = parseArgs(process.argv.slice(2))
  const manifestPath = normalizePath(options.manifest)
  for (const ref of [options.base, options.head]) {
    try {
      git(["rev-parse", "--verify", `${ref}^{commit}`])
    } catch (error) {
      throw new Error(
        `cannot resolve git ref ${ref}: ${error instanceof Error ? error.message : String(error)}`,
      )
    }
  }
  const baseManifest = readManifestAt(options.base, manifestPath, true)
  const headManifest = readManifestAt(options.head, manifestPath)

  // The first manifest PR inventories pre-existing debt. With no base manifest,
  // that checked-in inventory becomes the baseline; its standard and exempt
  // entries must still be complete, and subsequent PRs add delta enforcement.
  if (!baseManifest) {
    const failures = validateCollectionManifest(headManifest)
    if (failures.length > 0) {
      console.error(`collection delta failed (${failures.length}):`)
      for (const failure of failures) console.error(`  ${failure}`)
      process.exitCode = 1
      return
    }
    console.log(
      "collection delta: OK (bootstrap inventory; base manifest not present)",
    )
    return
  }

  const failures = evaluateCollectionDelta(
    baseManifest,
    headManifest,
    changedFiles(options.base, options.head),
  )
  if (failures.length > 0) {
    console.error(`collection delta failed (${failures.length}):`)
    for (const failure of failures) console.error(`  ${failure}`)
    process.exitCode = 1
    return
  }
  console.log("collection delta: OK")
}

if (
  process.argv[1] &&
  pathToFileURL(path.resolve(process.argv[1])).href === import.meta.url
) {
  main()
}
