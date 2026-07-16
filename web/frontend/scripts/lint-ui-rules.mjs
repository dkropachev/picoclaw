#!/usr/bin/env node

import fs from "node:fs"
import path from "node:path"
import { fileURLToPath } from "node:url"

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const frontendRoot = path.resolve(scriptDir, "..")
const srcRoot = path.join(frontendRoot, "src")

const sourceExtensions = new Set([".ts", ".tsx", ".js", ".jsx", ".css"])
const generatedFiles = new Set(["src/routeTree.gen.ts"])
const hardcodedColorAllowedFiles = new Map([
  ["src/components/chat/message-code-block.tsx", "code block theme colors"],
  ["src/lib/ansi-log.ts", "ANSI terminal color palette"],
])

const failures = []

function toRepoPath(filePath) {
  return path.relative(frontendRoot, filePath).split(path.sep).join("/")
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

function hasNearbyAllow(lines, index, token) {
  const start = Math.max(0, index - 8)
  for (let i = start; i <= index; i += 1) {
    if (lines[i]?.includes(`ui-rule-allow ${token}`)) {
      return true
    }
  }
  return false
}

function lintFetch(relPath, lines) {
  if (relPath.startsWith("src/api/")) {
    return
  }
  lines.forEach((line, index) => {
    if (/\bfetch\s*\(/.test(line)) {
      addFailure(
        relPath,
        index + 1,
        "frontend-api-boundary",
        "move HTTP calls into src/api/** and call an API helper from UI code",
      )
    }
  })
}

function lintInlineStyle(relPath, lines) {
  if (relPath.startsWith("src/components/ui/")) {
    return
  }
  if (!/\.(tsx|jsx)$/.test(relPath)) {
    return
  }
  lines.forEach((line, index) => {
    const trimmed = line.trim()
    const hasStyleAttribute =
      trimmed.startsWith("style=") || /<[^>]*\sstyle=/.test(line)
    if (hasStyleAttribute && !hasNearbyAllow(lines, index, "dynamic-style")) {
      addFailure(
        relPath,
        index + 1,
        "frontend-dynamic-style",
        "inline styles need a nearby ui-rule-allow dynamic-style comment",
      )
    }
  })
}

function lintHardcodedColor(relPath, lines) {
  if (hardcodedColorAllowedFiles.has(relPath)) {
    return
  }
  lines.forEach((line, index) => {
    if (/#[0-9A-Fa-f]{3,8}\b/.test(line)) {
      addFailure(
        relPath,
        index + 1,
        "frontend-color-token",
        "use semantic tokens instead of raw hex colors, or add this file to the approved rendering exceptions",
      )
    }
  })
}

for (const filePath of walk(srcRoot)) {
  const relPath = toRepoPath(filePath)
  if (generatedFiles.has(relPath)) {
    continue
  }
  const lines = fs.readFileSync(filePath, "utf8").split(/\r?\n/)
  lintFetch(relPath, lines)
  lintInlineStyle(relPath, lines)
  lintHardcodedColor(relPath, lines)
}

if (failures.length > 0) {
  console.error("frontend UI rule lint failed:")
  for (const failure of failures) {
    console.error(`  ${failure}`)
  }
  process.exit(1)
}

console.log("frontend UI rule lint: OK")
