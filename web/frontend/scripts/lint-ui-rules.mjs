#!/usr/bin/env node

import fs from "node:fs"
import path from "node:path"
import { fileURLToPath } from "node:url"

import ts from "typescript"

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const frontendRoot = path.resolve(scriptDir, "..")
const srcRoot = path.join(frontendRoot, "src")
const configPath = path.join(frontendRoot, "ui-rules.config.json")
const allowPendingStandard = process.argv.includes("--allow-pending-standard")

const config = JSON.parse(fs.readFileSync(configPath, "utf8"))
const sourceExtensions = new Set([".ts", ".tsx", ".js", ".jsx", ".css"])
const generatedFiles = new Set(config.generatedFiles ?? [])
const directFetchAllowed = config.apiBoundary?.directFetchAllowed ?? []
const dynamicStyleIgnored = config.dynamicStyle?.ignoredFiles ?? []
const dynamicStyleAllowToken =
  config.dynamicStyle?.allowCommentToken ?? "ui-rule-allow dynamic-style"
const dynamicStyleLookback = config.dynamicStyle?.allowCommentLookbackLines ?? 8
const hardcodedColorAllowedFiles = new Set(
  Object.keys(config.hardcodedColors?.allowedFiles ?? {}),
)
const collectionManifestPath = path.resolve(
  frontendRoot,
  config.collectionRules?.manifest ?? "collection-surfaces.json",
)
const sharedShellImportPrefixes =
  config.collectionRules?.sharedShellImportPrefixes ?? []
const oneOffInfrastructureImports = new Set(
  config.collectionRules?.oneOffInfrastructureImports ?? [],
)

const failures = []

function toRepoPath(filePath) {
  return path.relative(frontendRoot, filePath).split(path.sep).join("/")
}

function globToRegExp(pattern) {
  const escaped = pattern
    .replace(/[.+^${}()|[\]\\]/g, "\\$&")
    .replace(/\*\*/g, "\0")
    .replace(/\*/g, "[^/]*")
    .replace(/\?/g, "[^/]")
    .replace(/\0/g, ".*")
  return new RegExp(`^${escaped}$`)
}

function matchesAny(relPath, patterns) {
  return patterns.some((pattern) => globToRegExp(pattern).test(relPath))
}

function walk(dir, files = []) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    if (entry.name === "node_modules" || entry.name === "dist") {
      continue
    }
    const next = path.join(dir, entry.name)
    if (entry.isDirectory()) {
      walk(next, files)
    } else if (sourceExtensions.has(path.extname(entry.name))) {
      files.push(next)
    }
  }
  return files
}

function addFailure(relPath, line, rule, message) {
  failures.push(`${relPath}:${line}: ${rule}: ${message}`)
}

function lineFor(sourceFile, position) {
  return sourceFile.getLineAndCharacterOfPosition(position).line + 1
}

function hasNearbyAllow(lines, lineNumber, token) {
  const index = lineNumber - 1
  const start = Math.max(0, index - dynamicStyleLookback)
  for (let i = start; i <= index; i += 1) {
    if (lines[i]?.includes(token)) {
      return true
    }
  }
  return false
}

function scriptKindFor(relPath) {
  switch (path.extname(relPath)) {
    case ".tsx":
      return ts.ScriptKind.TSX
    case ".jsx":
      return ts.ScriptKind.JSX
    case ".js":
      return ts.ScriptKind.JS
    default:
      return ts.ScriptKind.TS
  }
}

function shouldCheckHardcodedColors(relPath) {
  return !hardcodedColorAllowedFiles.has(relPath)
}

function normalizeRoutePath(routePath) {
  const normalized = routePath
    .trim()
    .replace(/_(?=\/|$)/g, "")
    .replace(/\$([A-Za-z0-9_]+)/g, ":$1")
    .replace(/\/+$/, "")
  return normalized || "/"
}

function routeShape(routePath) {
  return normalizeRoutePath(routePath).replace(/:[^/]+/g, ":*")
}

function importSpecifiers(text) {
  const imports = []
  const pattern = /(?:from\s*|import\s*\()\s*["'`]([^"'`]+)["'`]/g
  for (const match of text.matchAll(pattern)) {
    imports.push(match[1])
  }
  return imports
}

function resolveSourceImport(importerPath, specifier, sourceByAbsolutePath) {
  let base
  if (specifier.startsWith("@/")) {
    base = path.join(srcRoot, specifier.slice(2))
  } else if (specifier.startsWith(".")) {
    base = path.resolve(path.dirname(importerPath), specifier)
  } else {
    return undefined
  }

  const candidates = [
    base,
    ...[".ts", ".tsx", ".js", ".jsx"].map((extension) => base + extension),
    ...[".ts", ".tsx", ".js", ".jsx"].map((extension) =>
      path.join(base, `index${extension}`),
    ),
  ]
  return candidates.find((candidate) => sourceByAbsolutePath.has(candidate))
}

function sourceClosure(entryPath, sourceByAbsolutePath) {
  const visited = new Set()
  const pending = [entryPath]
  while (pending.length > 0) {
    const current = pending.pop()
    if (!current || visited.has(current)) continue
    visited.add(current)
    const source = sourceByAbsolutePath.get(current)
    if (!source) continue
    for (const specifier of importSpecifiers(source.text)) {
      const resolved = resolveSourceImport(
        current,
        specifier,
        sourceByAbsolutePath,
      )
      if (resolved && !visited.has(resolved)) pending.push(resolved)
    }
  }
  return [...visited]
}

function manifestRoutes(routes) {
  const values = []
  if (!routes || typeof routes !== "object" || Array.isArray(routes)) {
    return values
  }
  for (const value of Object.values(routes)) {
    if (typeof value === "string") values.push(value)
    if (Array.isArray(value)) {
      values.push(...value.filter((item) => typeof item === "string"))
    }
  }
  return values
}

function lintCollectionManifest(sourceFiles) {
  if (!fs.existsSync(collectionManifestPath)) {
    addFailure(
      path.relative(frontendRoot, collectionManifestPath),
      1,
      "collection-manifest",
      "configured collection manifest does not exist",
    )
    return
  }

  let manifest
  try {
    manifest = JSON.parse(fs.readFileSync(collectionManifestPath, "utf8"))
  } catch (error) {
    addFailure(
      path.relative(frontendRoot, collectionManifestPath),
      1,
      "collection-manifest",
      `invalid JSON: ${error instanceof Error ? error.message : String(error)}`,
    )
    return
  }

  const manifestRelPath = toRepoPath(collectionManifestPath)
  if (manifest.version !== 1 || !Array.isArray(manifest.surfaces)) {
    addFailure(
      manifestRelPath,
      1,
      "collection-manifest",
      "expected version 1 and a surfaces array",
    )
    return
  }

  const allowedStatuses = new Set(["standard", "legacy", "exempt"])
  const allowedViews = new Set(["list", "table", "grid"])
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
  const keys = new Set()
  const sourceByAbsolutePath = new Map(
    sourceFiles.map((source) => [source.path, source]),
  )
  const routeSources = new Map()
  const routePattern = /createFileRoute\s*\(\s*["'`]([^"'`]+)["'`]\s*,?\s*\)/g
  for (const source of sourceFiles) {
    if (!source.relPath.startsWith("src/routes/")) continue
    for (const match of source.text.matchAll(routePattern)) {
      routeSources.set(routeShape(match[1]), {
        path: source.path,
        rawShape: match[1].replace(/:[^/]+|\$[A-Za-z0-9_]+/g, ":*"),
      })
    }
  }

  for (const [index, surface] of manifest.surfaces.entries()) {
    const label = `surface ${index}`
    if (!surface || typeof surface !== "object" || Array.isArray(surface)) {
      addFailure(
        manifestRelPath,
        1,
        "collection-manifest",
        `${label} is not an object`,
      )
      continue
    }
    const key = typeof surface.key === "string" ? surface.key.trim() : ""
    if (!/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(key)) {
      addFailure(
        manifestRelPath,
        1,
        "collection-manifest",
        `${label} has an invalid key`,
      )
      continue
    }
    if (keys.has(key)) {
      addFailure(
        manifestRelPath,
        1,
        "collection-manifest",
        `duplicate key ${key}`,
      )
    }
    keys.add(key)

    if (!allowedStatuses.has(surface.status)) {
      addFailure(
        manifestRelPath,
        1,
        "collection-manifest",
        `${key} has an invalid status`,
      )
    }
    if (typeof surface.route !== "string" || !surface.route.startsWith("/")) {
      addFailure(
        manifestRelPath,
        1,
        "collection-manifest",
        `${key} has an invalid route`,
      )
    }
    if (
      typeof surface.owningSpec !== "string" ||
      !/^docs\/features\/[a-z0-9-]+\.md$/.test(surface.owningSpec) ||
      !fs.existsSync(path.resolve(frontendRoot, "../..", surface.owningSpec))
    ) {
      addFailure(
        manifestRelPath,
        1,
        "collection-manifest",
        `${key} must name an existing docs/features owning spec`,
      )
    }
    if (
      !Array.isArray(surface.implementationGlobs) ||
      surface.implementationGlobs.length === 0 ||
      surface.implementationGlobs.some(
        (pattern) => typeof pattern !== "string" || !pattern.startsWith("src/"),
      )
    ) {
      addFailure(
        manifestRelPath,
        1,
        "collection-manifest",
        `${key} must declare implementation globs under src/`,
      )
    }
    if (
      !Array.isArray(surface.capabilities) ||
      surface.capabilities.some(
        (capability) => !allowedCapabilities.has(capability),
      )
    ) {
      addFailure(
        manifestRelPath,
        1,
        "collection-manifest",
        `${key} has invalid capabilities`,
      )
    }
    if (
      !Array.isArray(surface.views) ||
      surface.views.length === 0 ||
      surface.views.some((view) => !allowedViews.has(view))
    ) {
      addFailure(
        manifestRelPath,
        1,
        "collection-manifest",
        `${key} has invalid views`,
      )
    }
    if (
      surface.status === "exempt" &&
      (typeof surface.exemptionReason !== "string" ||
        surface.exemptionReason.trim().length < 20)
    ) {
      addFailure(
        manifestRelPath,
        1,
        "collection-manifest",
        `${key} requires a specific exemptionReason`,
      )
    }

    if (surface.route !== surface.routes?.list) {
      addFailure(
        manifestRelPath,
        1,
        "collection-manifest",
        `${key} route must equal routes.list`,
      )
    }
    const implementationFiles = sourceFiles.filter((source) =>
      matchesAny(source.relPath, surface.implementationGlobs ?? []),
    )
    if (implementationFiles.length === 0) {
      addFailure(
        manifestRelPath,
        1,
        "collection-manifest",
        `${key} implementation globs match no source files`,
      )
    }
    if (!routeSources.has(routeShape(surface.routes?.list ?? ""))) {
      addFailure(
        manifestRelPath,
        1,
        "collection-manifest",
        `${key} list route is not registered`,
      )
    }

    if (surface.status !== "standard") continue

    if (
      !Array.isArray(surface.views) ||
      [...allowedViews].some((view) => !surface.views.includes(view))
    ) {
      addFailure(
        manifestRelPath,
        1,
        "collection-standard",
        `${key} must support list, table, and grid views`,
      )
    }
    const capabilityRoutes = {
      create: "new",
      detail: "detail",
      edit: "edit",
      related: "related",
      settings: "settings",
    }
    for (const [capability, routeKey] of Object.entries(capabilityRoutes)) {
      if (
        Array.isArray(surface.capabilities) &&
        surface.capabilities.includes(capability) &&
        surface.routes?.[routeKey] == null
      ) {
        addFailure(
          manifestRelPath,
          1,
          "collection-standard",
          `${key} capability ${capability} requires routes.${routeKey}`,
        )
      }
    }
    const listRoute = surface.routes?.list
    const listRouteSource =
      typeof listRoute === "string"
        ? routeSources.get(routeShape(listRoute))
        : undefined
    const pendingFailures = []
    for (const requiredRoute of manifestRoutes(surface.routes)) {
      if (!routeSources.has(routeShape(requiredRoute))) {
        pendingFailures.push(`missing registered route ${requiredRoute}`)
      }
    }
    if (!listRouteSource) {
      pendingFailures.push("list route is missing or not declared")
    } else {
      const closure = sourceClosure(listRouteSource.path, sourceByAbsolutePath)
      const closureSources = closure
        .map((filePath) => sourceByAbsolutePath.get(filePath))
        .filter(Boolean)
      const importsStandardCollectionPage = closureSources.some(
        (source) =>
          source.text.includes("<StandardCollectionPage") &&
          importSpecifiers(source.text).some((specifier) =>
            sharedShellImportPrefixes.some((prefix) =>
              specifier.startsWith(prefix),
            ),
          ),
      )
      if (!importsStandardCollectionPage) {
        pendingFailures.push(
          "list adapter import closure does not use shared StandardCollectionPage",
        )
      }
      for (const source of closureSources) {
        for (const specifier of importSpecifiers(source.text)) {
          if (oneOffInfrastructureImports.has(specifier)) {
            pendingFailures.push(
              `${source.relPath} imports one-off collection infrastructure ${specifier}`,
            )
          }
        }
      }
    }

    if (listRouteSource) {
      for (const requiredRoute of manifestRoutes(surface.routes)) {
        if (requiredRoute === listRoute) continue
        const declaration = routeSources.get(routeShape(requiredRoute))
        if (declaration?.rawShape.startsWith(`${listRouteSource.rawShape}/`)) {
          pendingFailures.push(
            `${requiredRoute} is nested beneath the list route; escape it into a dedicated sibling route`,
          )
        }
      }
    }

    if (!allowPendingStandard) {
      for (const message of [...new Set(pendingFailures)]) {
        addFailure(
          manifestRelPath,
          1,
          "collection-standard",
          `${key}: ${message}`,
        )
      }
    }
  }
}

function inspectTextForHexColors(relPath, sourceFile, text, position) {
  if (!shouldCheckHardcodedColors(relPath)) {
    return
  }
  const match = /#[0-9A-Fa-f]{3,8}\b/.exec(text)
  if (match) {
    addFailure(
      relPath,
      lineFor(sourceFile, position + match.index),
      "frontend-color-token",
      "use semantic tokens instead of raw hex colors, or add this file to ui-rules.config.json rendering exceptions",
    )
  }
}

function lintScript(relPath, filePath, text) {
  const sourceFile = ts.createSourceFile(
    relPath,
    text,
    ts.ScriptTarget.Latest,
    true,
    scriptKindFor(relPath),
  )
  const lines = text.split(/\r?\n/)
  const allowDirectFetch = matchesAny(relPath, directFetchAllowed)
  const ignoreDynamicStyle = matchesAny(relPath, dynamicStyleIgnored)

  function visit(node) {
    if (
      !allowDirectFetch &&
      ts.isCallExpression(node) &&
      ts.isIdentifier(node.expression) &&
      node.expression.text === "fetch"
    ) {
      addFailure(
        relPath,
        lineFor(sourceFile, node.expression.getStart(sourceFile)),
        "frontend-api-boundary",
        "move HTTP calls into src/api/** and call an API helper from UI code",
      )
    }

    if (
      !ignoreDynamicStyle &&
      ts.isJsxAttribute(node) &&
      ts.isIdentifier(node.name) &&
      node.name.text === "style"
    ) {
      const line = lineFor(sourceFile, node.name.getStart(sourceFile))
      if (!hasNearbyAllow(lines, line, dynamicStyleAllowToken)) {
        addFailure(
          relPath,
          line,
          "frontend-dynamic-style",
          `inline styles need a nearby ${dynamicStyleAllowToken} comment`,
        )
      }
    }

    if (
      ts.isStringLiteralLike(node) ||
      ts.isNoSubstitutionTemplateLiteral(node)
    ) {
      inspectTextForHexColors(
        relPath,
        sourceFile,
        node.text,
        node.getStart(sourceFile),
      )
    }

    ts.forEachChild(node, visit)
  }

  visit(sourceFile)
}

function lintCss(relPath, text) {
  if (!shouldCheckHardcodedColors(relPath)) {
    return
  }
  const lines = text.split(/\r?\n/)
  lines.forEach((line, index) => {
    if (/#[0-9A-Fa-f]{3,8}\b/.test(line)) {
      addFailure(
        relPath,
        index + 1,
        "frontend-color-token",
        "use semantic tokens instead of raw hex colors, or add this file to ui-rules.config.json rendering exceptions",
      )
    }
  })
}

const sourceFiles = []
for (const filePath of walk(srcRoot)) {
  const relPath = toRepoPath(filePath)
  if (generatedFiles.has(relPath)) {
    continue
  }
  const text = fs.readFileSync(filePath, "utf8")
  sourceFiles.push({ path: filePath, relPath, text })
  if (path.extname(relPath) === ".css") {
    lintCss(relPath, text)
  } else {
    lintScript(relPath, filePath, text)
  }
}

lintCollectionManifest(sourceFiles)

if (failures.length > 0) {
  console.error("frontend UI rule lint failed:")
  for (const failure of failures) {
    console.error(`  ${failure}`)
  }
  process.exit(1)
}

console.log(
  allowPendingStandard
    ? "frontend UI rule lint: OK (pending standard implementations allowed)"
    : "frontend UI rule lint: OK",
)
