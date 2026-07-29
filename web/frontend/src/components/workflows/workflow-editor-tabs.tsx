import { type KeyboardEvent, useRef } from "react"

import { Button } from "@/components/ui/button"

export type WorkflowEditorMode = "builder" | "yaml"

const editorModes: WorkflowEditorMode[] = ["builder", "yaml"]

export function WorkflowEditorTabs({
  mode,
  onModeChange,
}: {
  mode: WorkflowEditorMode
  onModeChange: (mode: WorkflowEditorMode) => void
}) {
  const builderRef = useRef<HTMLButtonElement>(null)
  const yamlRef = useRef<HTMLButtonElement>(null)
  const tabRefs = {
    builder: builderRef,
    yaml: yamlRef,
  }

  const selectAndFocus = (nextMode: WorkflowEditorMode) => {
    onModeChange(nextMode)
    tabRefs[nextMode].current?.focus()
  }

  const onKeyDown = (
    event: KeyboardEvent<HTMLButtonElement>,
    currentMode: WorkflowEditorMode,
  ) => {
    const currentIndex = editorModes.indexOf(currentMode)
    let nextMode: WorkflowEditorMode | undefined
    switch (event.key) {
      case "ArrowRight":
        nextMode = editorModes[(currentIndex + 1) % editorModes.length]
        break
      case "ArrowLeft":
        nextMode =
          editorModes[
            (currentIndex - 1 + editorModes.length) % editorModes.length
          ]
        break
      case "Home":
        nextMode = editorModes[0]
        break
      case "End":
        nextMode = editorModes[editorModes.length - 1]
        break
      default:
        return
    }
    event.preventDefault()
    selectAndFocus(nextMode)
  }

  return (
    <div
      role="tablist"
      aria-label="Workflow editor"
      className="border-border bg-background flex rounded-md border p-0.5"
    >
      <Button
        ref={builderRef}
        id="workflow-builder-tab"
        type="button"
        role="tab"
        tabIndex={mode === "builder" ? 0 : -1}
        aria-selected={mode === "builder"}
        aria-controls="workflow-builder-panel"
        variant={mode === "builder" ? "secondary" : "ghost"}
        size="sm"
        onClick={() => onModeChange("builder")}
        onKeyDown={(event) => onKeyDown(event, "builder")}
      >
        Builder
      </Button>
      <Button
        ref={yamlRef}
        id="workflow-yaml-tab"
        type="button"
        role="tab"
        tabIndex={mode === "yaml" ? 0 : -1}
        aria-selected={mode === "yaml"}
        aria-controls="workflow-yaml-panel"
        variant={mode === "yaml" ? "secondary" : "ghost"}
        size="sm"
        onClick={() => onModeChange("yaml")}
        onKeyDown={(event) => onKeyDown(event, "yaml")}
      >
        YAML
      </Button>
    </div>
  )
}
