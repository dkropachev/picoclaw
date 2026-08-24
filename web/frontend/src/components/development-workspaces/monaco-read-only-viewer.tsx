import "monaco-editor/basic-languages/monaco.contribution.js"
import * as monaco from "monaco-editor/editor/editor.api.js"
import EditorWorker from "monaco-editor/editor/editor.worker.js?worker"
import { useEffect, useRef } from "react"

interface MonacoReadOnlyViewerProps {
  path: string
  original?: string
  modified: string
  language: string
  theme: "light" | "dark"
  inline: boolean
}

type MonacoEnvironmentGlobal = typeof globalThis & {
  MonacoEnvironment?: {
    getWorker: (_moduleID: string, _label: string) => Worker
  }
}

const monacoGlobal = globalThis as MonacoEnvironmentGlobal
monacoGlobal.MonacoEnvironment ??= {
  getWorker: () => new EditorWorker(),
}

let modelSequence = 0

export default function MonacoReadOnlyViewer({
  path,
  original,
  modified,
  language,
  theme,
  inline,
}: MonacoReadOnlyViewerProps) {
  const containerRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const container = containerRef.current
    if (!container) return
    const monacoTheme = theme === "dark" ? "vs-dark" : "vs"
    monaco.editor.setTheme(monacoTheme)
    const suffix = `${encodeURIComponent(path)}-${++modelSequence}`
    const modifiedModel = monaco.editor.createModel(
      modified,
      language,
      monaco.Uri.parse(`inmemory://candidate/${suffix}`),
    )

    if (original == null) {
      const editor = monaco.editor.create(container, {
        model: modifiedModel,
        readOnly: true,
        domReadOnly: true,
        automaticLayout: true,
        minimap: { enabled: false },
        scrollBeyondLastLine: false,
        accessibilitySupport: "on",
        ariaLabel: `Read-only candidate file ${path}`,
        renderValidationDecorations: "off",
      })
      return () => {
        editor.dispose()
        modifiedModel.dispose()
      }
    }

    const originalModel = monaco.editor.createModel(
      original,
      language,
      monaco.Uri.parse(`inmemory://base/${suffix}`),
    )
    const editor = monaco.editor.createDiffEditor(container, {
      readOnly: true,
      originalEditable: false,
      automaticLayout: true,
      renderSideBySide: !inline,
      useInlineViewWhenSpaceIsLimited: true,
      minimap: { enabled: false },
      scrollBeyondLastLine: false,
      accessibilitySupport: "on",
      ariaLabel: `Read-only base and candidate diff for ${path}`,
      renderValidationDecorations: "off",
    })
    editor.setModel({ original: originalModel, modified: modifiedModel })
    return () => {
      editor.dispose()
      originalModel.dispose()
      modifiedModel.dispose()
    }
  }, [inline, language, modified, original, path, theme])

  return (
    <div
      ref={containerRef}
      className="h-[min(65vh,48rem)] min-h-80 w-full"
      data-testid="monaco-read-only-viewer"
    />
  )
}
