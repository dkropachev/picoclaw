import assert from "node:assert/strict"
import path from "node:path"
import test from "node:test"

import { validateCollectionEditorGovernance } from "./collection-ui-governance.mjs"

const frontendRoot = path.resolve("/test/frontend")
const srcRoot = path.join(frontendRoot, "src")
const editorConfig = {
  standardPage: {
    file: "src/components/collection/standard-collection-page.tsx",
    component: "StandardCollectionPage",
  },
  toolbar: {
    file: "src/components/collection/collection-toolbar.tsx",
    component: "CollectionToolbar",
  },
  queryInput: {
    file: "src/components/collection/collection-query-input.tsx",
    component: "CollectionQueryInput",
    slot: "collection-query-input",
  },
}

function source(relPath, text) {
  return {
    path: path.join(frontendRoot, relPath),
    relPath,
    text,
  }
}

function canonicalSources() {
  return [
    source(
      editorConfig.standardPage.file,
      `
        import { CollectionToolbar as Toolbar } from "@/components/collection/collection-toolbar"
        export function StandardCollectionPage() { return <Toolbar /> }
      `,
    ),
    source(
      editorConfig.toolbar.file,
      `
        import { CollectionQueryInput as QueryInput } from "./collection-query-input"
        export function CollectionToolbar() { return <QueryInput /> }
      `,
    ),
    source(
      editorConfig.queryInput.file,
      `
        export function CollectionQueryInput() {
          return <div data-slot="collection-query-input" />
        }
      `,
    ),
  ]
}

test("the configured standard collection editor chain is accepted", () => {
  const sources = [
    ...canonicalSources(),
    source(
      "src/components/collection/collection-query-input.test.tsx",
      `
        import { CollectionQueryInput } from "./collection-query-input"
        export const subject = <CollectionQueryInput />
      `,
    ),
  ]
  assert.deepEqual(
    validateCollectionEditorGovernance(sources, editorConfig, srcRoot),
    [],
  )
})

test("both direct links in the editor chain are required", () => {
  const sources = canonicalSources()
  sources[0] = source(
    editorConfig.standardPage.file,
    `
      import { CollectionToolbar } from "@/components/collection/collection-toolbar"
      export function StandardCollectionPage() { return <main /> }
    `,
  )
  sources[1] = source(
    editorConfig.toolbar.file,
    `
      import { CollectionQueryInput } from "./collection-query-input"
      export function CollectionToolbar() { return <section /> }
    `,
  )

  assert.deepEqual(
    validateCollectionEditorGovernance(sources, editorConfig, srcRoot).map(
      ({ relPath, message }) => `${relPath}: ${message}`,
    ),
    [
      "src/components/collection/collection-toolbar.tsx: CollectionToolbar must directly import and render exactly one CollectionQueryInput",
      "src/components/collection/standard-collection-page.tsx: StandardCollectionPage must directly import and render exactly one CollectionToolbar",
    ],
  )
})

test("the chain rejects duplicate editor instances", () => {
  const sources = canonicalSources()
  sources[1] = source(
    editorConfig.toolbar.file,
    `
      import { CollectionQueryInput } from "./collection-query-input"
      export function CollectionToolbar() {
        return <><CollectionQueryInput /><CollectionQueryInput /></>
      }
    `,
  )

  assert.deepEqual(
    validateCollectionEditorGovernance(sources, editorConfig, srcRoot).map(
      ({ relPath, message }) => `${relPath}: ${message}`,
    ),
    [
      "src/components/collection/collection-toolbar.tsx: CollectionToolbar must directly import and render exactly one CollectionQueryInput",
    ],
  )
})

test("the toolbar and query input cannot be rendered by production peers", () => {
  const sources = [
    ...canonicalSources(),
    source(
      "src/components/rogue.tsx",
      `
        import { CollectionQueryInput as QueryEditor } from "@/components/collection/collection-query-input"
        import * as Collection from "@/components/collection/collection-toolbar"
        export const rogue = <><QueryEditor /><Collection.CollectionToolbar /></>
      `,
    ),
  ]

  assert.deepEqual(
    validateCollectionEditorGovernance(sources, editorConfig, srcRoot).map(
      ({ relPath, message }) => `${relPath}: ${message}`,
    ),
    [
      "src/components/rogue.tsx: CollectionQueryInput is reserved for CollectionToolbar",
      "src/components/rogue.tsx: CollectionToolbar is reserved for StandardCollectionPage",
    ],
  )
})

test("an unrelated helper cannot satisfy a configured component chain link", () => {
  const sources = canonicalSources()
  sources[0] = source(
    editorConfig.standardPage.file,
    `
      import { CollectionToolbar } from "@/components/collection/collection-toolbar"
      function Decoy() { return <CollectionToolbar /> }
      export function StandardCollectionPage() { return <main /> }
    `,
  )

  assert.deepEqual(
    validateCollectionEditorGovernance(sources, editorConfig, srcRoot).map(
      ({ relPath, message }) => `${relPath}: ${message}`,
    ),
    [
      "src/components/collection/standard-collection-page.tsx: StandardCollectionPage must directly import and render exactly one CollectionToolbar",
    ],
  )
})

test("same-named components from unrelated sources are not reserved", () => {
  const sources = [
    ...canonicalSources(),
    source(
      "src/components/unrelated.tsx",
      `export function CollectionQueryInput() { return <div /> }`,
    ),
    source(
      "src/components/rogue.tsx",
      `
        import { CollectionQueryInput } from "./unrelated"
        export const allowed = <CollectionQueryInput />
      `,
    ),
  ]

  assert.deepEqual(
    validateCollectionEditorGovernance(sources, editorConfig, srcRoot),
    [],
  )
})

test("reserved components cannot escape through barrel re-exports", () => {
  const sources = [
    ...canonicalSources(),
    source(
      "src/components/collection/index.ts",
      `export { CollectionQueryInput } from "./collection-query-input"`,
    ),
  ]

  assert.deepEqual(
    validateCollectionEditorGovernance(sources, editorConfig, srcRoot).map(
      ({ relPath, message }) => `${relPath}: ${message}`,
    ),
    [
      "src/components/collection/index.ts: CollectionQueryInput cannot be re-exported outside its canonical source",
    ],
  )
})

test("reserved components cannot escape through star re-exports", () => {
  const sources = [
    ...canonicalSources(),
    source(
      "src/components/collection/index.ts",
      `export * from "./collection-query-input"`,
    ),
  ]

  assert.deepEqual(
    validateCollectionEditorGovernance(sources, editorConfig, srcRoot).map(
      ({ relPath, message }) => `${relPath}: ${message}`,
    ),
    [
      "src/components/collection/index.ts: CollectionQueryInput cannot be re-exported outside its canonical source",
    ],
  )
})

test("only the shared query input owns the collection-query-input slot", () => {
  const sources = [
    ...canonicalSources(),
    source(
      "src/components/rogue.tsx",
      `export const rogue = <div data-slot="collection-query-input" />`,
    ),
  ]

  assert.deepEqual(
    validateCollectionEditorGovernance(sources, editorConfig, srcRoot).map(
      ({ relPath, line, message }) => `${relPath}:${line}: ${message}`,
    ),
    [
      'src/components/rogue.tsx:1: data-slot="collection-query-input" is reserved for CollectionQueryInput',
    ],
  )
})

test("the reserved slot also rejects JSX expression literals", () => {
  const sources = [
    ...canonicalSources(),
    source(
      "src/components/rogue.tsx",
      `export const rogue = <div data-slot={"collection-query-input"} />`,
    ),
  ]

  assert.deepEqual(
    validateCollectionEditorGovernance(sources, editorConfig, srcRoot).map(
      ({ relPath, line, message }) => `${relPath}:${line}: ${message}`,
    ),
    [
      'src/components/rogue.tsx:1: data-slot="collection-query-input" is reserved for CollectionQueryInput',
    ],
  )
})
