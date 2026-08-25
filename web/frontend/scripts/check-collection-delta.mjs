#!/usr/bin/env node

import { execFileSync } from "node:child_process"
import path from "node:path"
import process from "node:process"
import { fileURLToPath, pathToFileURL } from "node:url"

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const frontendRoot = path.resolve(scriptDir, "..")
const repoRoot = path.resolve(frontendRoot, "../..")
const defaultManifest = "web/frontend/collection-surfaces.json"

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

export function evaluateCollectionDelta(
  baseManifest,
  headManifest,
  changedFiles,
) {
  const failures = []
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
    if (base.status !== "legacy" || !surfaceWasModified(base, changedFiles)) {
      continue
    }
    const head = headByKey.get(base.key)
    if (head && head.status !== "standard") {
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
  // that checked-in inventory becomes the baseline; subsequent PRs are strict.
  if (!baseManifest) {
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
