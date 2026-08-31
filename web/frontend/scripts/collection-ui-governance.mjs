import path from "node:path"

import ts from "typescript"

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

function isTestSource(relPath) {
  return /(?:^|\/)[^/]+\.(?:test|spec)\.[^.]+$/.test(relPath)
}

function resolveSourceImport(
  importerPath,
  specifier,
  sourceByAbsolutePath,
  srcRoot,
) {
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

function lineFor(sourceFile, position) {
  return sourceFile.getLineAndCharacterOfPosition(position).line + 1
}

function inspectSource(source, sourceByAbsolutePath, srcRoot) {
  const sourceFile = ts.createSourceFile(
    source.relPath,
    source.text,
    ts.ScriptTarget.Latest,
    true,
    scriptKindFor(source.relPath),
  )
  const imports = []
  const reExports = []
  const tags = []
  const slots = []
  const componentTags = new Map()

  function componentName(node) {
    if (ts.isFunctionDeclaration(node) && node.name) return node.name.text
    if (
      ts.isVariableDeclaration(node) &&
      ts.isIdentifier(node.name) &&
      node.initializer &&
      (ts.isArrowFunction(node.initializer) ||
        ts.isFunctionExpression(node.initializer))
    ) {
      return node.name.text
    }
    return undefined
  }

  function collectComponentTags(node, name) {
    const collected = []
    function collect(child) {
      if (child !== node && componentName(child)) return
      if (ts.isJsxOpeningElement(child) || ts.isJsxSelfClosingElement(child)) {
        const tagName = child.tagName
        if (ts.isIdentifier(tagName)) {
          collected.push({ rootName: tagName.text, memberName: undefined })
        } else if (
          ts.isPropertyAccessExpression(tagName) &&
          ts.isIdentifier(tagName.expression)
        ) {
          collected.push({
            rootName: tagName.expression.text,
            memberName: tagName.name.text,
          })
        }
      }
      ts.forEachChild(child, collect)
    }
    collect(node)
    componentTags.set(name, collected)
  }

  function visit(node) {
    const declaredComponent = componentName(node)
    if (declaredComponent) collectComponentTags(node, declaredComponent)
    if (
      ts.isImportDeclaration(node) &&
      ts.isStringLiteralLike(node.moduleSpecifier)
    ) {
      const resolvedPath = resolveSourceImport(
        source.path,
        node.moduleSpecifier.text,
        sourceByAbsolutePath,
        srcRoot,
      )
      const resolvedRelPath = resolvedPath
        ? sourceByAbsolutePath.get(resolvedPath)?.relPath
        : undefined
      const bindings = node.importClause?.namedBindings
      if (bindings && ts.isNamedImports(bindings)) {
        for (const element of bindings.elements) {
          imports.push({
            importedName: element.propertyName?.text ?? element.name.text,
            localName: element.name.text,
            resolvedRelPath,
          })
        }
      } else if (bindings && ts.isNamespaceImport(bindings)) {
        imports.push({
          importedName: "*",
          localName: bindings.name.text,
          resolvedRelPath,
        })
      }
    }

    if (
      ts.isExportDeclaration(node) &&
      node.moduleSpecifier &&
      ts.isStringLiteralLike(node.moduleSpecifier)
    ) {
      const resolvedPath = resolveSourceImport(
        source.path,
        node.moduleSpecifier.text,
        sourceByAbsolutePath,
        srcRoot,
      )
      const resolvedRelPath = resolvedPath
        ? sourceByAbsolutePath.get(resolvedPath)?.relPath
        : undefined
      if (!node.exportClause) {
        reExports.push({ exportedName: "*", resolvedRelPath })
      } else if (ts.isNamedExports(node.exportClause)) {
        for (const element of node.exportClause.elements) {
          reExports.push({
            exportedName: element.name.text,
            importedName: element.propertyName?.text ?? element.name.text,
            resolvedRelPath,
          })
        }
      }
    }

    if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
      const tagName = node.tagName
      if (ts.isIdentifier(tagName)) {
        tags.push({ rootName: tagName.text, memberName: undefined })
      } else if (
        ts.isPropertyAccessExpression(tagName) &&
        ts.isIdentifier(tagName.expression)
      ) {
        tags.push({
          rootName: tagName.expression.text,
          memberName: tagName.name.text,
        })
      }

      for (const attribute of node.attributes.properties) {
        if (
          ts.isJsxAttribute(attribute) &&
          attribute.name.text === "data-slot"
        ) {
          const initializer = attribute.initializer
          const literal =
            initializer && ts.isStringLiteral(initializer)
              ? initializer
              : initializer &&
                  ts.isJsxExpression(initializer) &&
                  initializer.expression &&
                  ts.isStringLiteralLike(initializer.expression)
                ? initializer.expression
                : undefined
          if (!literal) continue
          slots.push({
            value: literal.text,
            line: lineFor(sourceFile, attribute.getStart(sourceFile)),
          })
        }
      }
    }

    ts.forEachChild(node, visit)
  }

  visit(sourceFile)
  return { ...source, imports, reExports, tags, slots, componentTags }
}

function renderCount(source, imported, componentName) {
  return countMatchingTags(source.tags, imported, componentName)
}

function componentRenderCount(source, parentName, imported, componentName) {
  return countMatchingTags(
    source.componentTags.get(parentName) ?? [],
    imported,
    componentName,
  )
}

function countMatchingTags(tags, imported, componentName) {
  if (imported.importedName === componentName) {
    return tags.filter(
      (tag) =>
        tag.rootName === imported.localName && tag.memberName === undefined,
    ).length
  }
  if (imported.importedName === "*") {
    return tags.filter(
      (tag) =>
        tag.rootName === imported.localName && tag.memberName === componentName,
    ).length
  }
  return 0
}

/**
 * Validate the production ownership chain for the standard collection query
 * editor. Server and component tests may render the shared pieces directly;
 * production sources must keep the configured chain and slot ownership.
 */
export function validateCollectionEditorGovernance(
  sourceFiles,
  editorConfig,
  srcRoot,
) {
  const failures = []
  const addFailure = (relPath, line, message) => {
    failures.push({ relPath, line, message })
  }
  if (!editorConfig || typeof editorConfig !== "object") {
    addFailure(
      "ui-rules.config.json",
      1,
      "collectionRules.sharedEditor must configure the shared editor chain",
    )
    return failures
  }

  const nodes = [
    ["standardPage", editorConfig.standardPage],
    ["toolbar", editorConfig.toolbar],
    ["queryInput", editorConfig.queryInput],
  ]
  for (const [name, node] of nodes) {
    if (
      !node ||
      typeof node.file !== "string" ||
      typeof node.component !== "string"
    ) {
      addFailure(
        "ui-rules.config.json",
        1,
        `collectionRules.sharedEditor.${name} must declare file and component`,
      )
    }
  }
  if (
    !editorConfig.queryInput ||
    typeof editorConfig.queryInput.slot !== "string"
  ) {
    addFailure(
      "ui-rules.config.json",
      1,
      "collectionRules.sharedEditor.queryInput must declare its reserved slot",
    )
  }
  if (failures.length > 0) return failures

  const sourceByAbsolutePath = new Map(
    sourceFiles.map((source) => [source.path, source]),
  )
  const inspectedSources = sourceFiles
    .filter((source) => path.extname(source.relPath) !== ".css")
    .map((source) => inspectSource(source, sourceByAbsolutePath, srcRoot))
  const sourceByRelPath = new Map(
    inspectedSources.map((source) => [source.relPath, source]),
  )

  for (const [, node] of nodes) {
    if (!sourceByRelPath.has(node.file)) {
      addFailure(node.file, 1, `configured ${node.component} source is missing`)
    }
  }
  if (failures.length > 0) return failures

  const chain = [
    [editorConfig.standardPage, editorConfig.toolbar],
    [editorConfig.toolbar, editorConfig.queryInput],
  ]
  for (const [parent, child] of chain) {
    const parentSource = sourceByRelPath.get(parent.file)
    const imports = parentSource.imports.filter(
      (candidate) => candidate.resolvedRelPath === child.file,
    )
    const renderTotal = imports.reduce(
      (total, candidate) =>
        total +
        componentRenderCount(
          parentSource,
          parent.component,
          candidate,
          child.component,
        ),
      0,
    )
    if (renderTotal !== 1) {
      addFailure(
        parent.file,
        1,
        `${parent.component} must directly import and render exactly one ${child.component}`,
      )
    }
  }

  const reservedConsumers = [
    [editorConfig.toolbar, editorConfig.standardPage],
    [editorConfig.queryInput, editorConfig.toolbar],
  ]
  for (const [component, owner] of reservedConsumers) {
    for (const source of inspectedSources) {
      if (source.relPath === component.file) continue
      if (
        source.reExports.some(
          (candidate) =>
            candidate.resolvedRelPath === component.file &&
            (candidate.exportedName === "*" ||
              candidate.importedName === component.component),
        )
      ) {
        addFailure(
          source.relPath,
          1,
          `${component.component} cannot be re-exported outside its canonical source`,
        )
      }
    }
    for (const source of inspectedSources) {
      if (source.relPath === owner.file || isTestSource(source.relPath))
        continue
      const canonicalImports = source.imports.filter(
        (candidate) => candidate.resolvedRelPath === component.file,
      )
      if (
        canonicalImports.some(
          (candidate) =>
            renderCount(source, candidate, component.component) > 0,
        )
      ) {
        addFailure(
          source.relPath,
          1,
          `${component.component} is reserved for ${owner.component}`,
        )
      }
    }
  }

  const slot = editorConfig.queryInput.slot
  const slotOwners = inspectedSources.flatMap((source) =>
    source.slots
      .filter((candidate) => candidate.value === slot)
      .map((candidate) => ({ source, line: candidate.line })),
  )
  const canonicalSlots = slotOwners.filter(
    (candidate) => candidate.source.relPath === editorConfig.queryInput.file,
  )
  if (canonicalSlots.length !== 1) {
    addFailure(
      editorConfig.queryInput.file,
      1,
      `${editorConfig.queryInput.component} must own exactly one data-slot=\"${slot}\"`,
    )
  }
  for (const candidate of slotOwners) {
    if (candidate.source.relPath === editorConfig.queryInput.file) continue
    addFailure(
      candidate.source.relPath,
      candidate.line,
      `data-slot=\"${slot}\" is reserved for ${editorConfig.queryInput.component}`,
    )
  }

  return failures.sort(
    (left, right) =>
      left.relPath.localeCompare(right.relPath) ||
      left.line - right.line ||
      left.message.localeCompare(right.message),
  )
}
