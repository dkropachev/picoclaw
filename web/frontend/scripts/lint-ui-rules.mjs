#!/usr/bin/env node

import fs from "node:fs"
import path from "node:path"
import { fileURLToPath } from "node:url"

import ts from "typescript"

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const frontendRoot = path.resolve(scriptDir, "..")
const srcRoot = path.join(frontendRoot, "src")
const configPath = path.join(frontendRoot, "ui-rules.config.json")

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

for (const filePath of walk(srcRoot)) {
  const relPath = toRepoPath(filePath)
  if (generatedFiles.has(relPath)) {
    continue
  }
  const text = fs.readFileSync(filePath, "utf8")
  if (path.extname(relPath) === ".css") {
    lintCss(relPath, text)
  } else {
    lintScript(relPath, filePath, text)
  }
}

if (failures.length > 0) {
  console.error("frontend UI rule lint failed:")
  for (const failure of failures) {
    console.error(`  ${failure}`)
  }
  process.exit(1)
}

console.log("frontend UI rule lint: OK")
