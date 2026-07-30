import { type KeyboardEvent, useRef } from "react"

import { Button } from "@/components/ui/button"

export type WorkflowBuilderSection = "triggers" | "jobs"

const builderSections: WorkflowBuilderSection[] = ["triggers", "jobs"]

const builderSectionLabels: Record<WorkflowBuilderSection, string> = {
  triggers: "Triggers",
  jobs: "Jobs & actions",
}

export function WorkflowBuilderTabs({
  section,
  disabledReason,
  onSectionChange,
}: {
  section: WorkflowBuilderSection
  disabledReason?: string
  onSectionChange: (section: WorkflowBuilderSection) => void
}) {
  const triggersRef = useRef<HTMLButtonElement>(null)
  const jobsRef = useRef<HTMLButtonElement>(null)
  const tabRefs = {
    triggers: triggersRef,
    jobs: jobsRef,
  }

  const selectAndFocus = (nextSection: WorkflowBuilderSection) => {
    if (nextSection === section) {
      return
    }
    if (disabledReason == null) {
      onSectionChange(nextSection)
      tabRefs[nextSection].current?.focus()
    }
  }

  const onKeyDown = (
    event: KeyboardEvent<HTMLButtonElement>,
    currentSection: WorkflowBuilderSection,
  ) => {
    const currentIndex = builderSections.indexOf(currentSection)
    let nextSection: WorkflowBuilderSection | undefined
    switch (event.key) {
      case "ArrowRight":
        nextSection =
          builderSections[(currentIndex + 1) % builderSections.length]
        break
      case "ArrowLeft":
        nextSection =
          builderSections[
            (currentIndex - 1 + builderSections.length) % builderSections.length
          ]
        break
      case "Home":
        nextSection = builderSections[0]
        break
      case "End":
        nextSection = builderSections[builderSections.length - 1]
        break
      default:
        return
    }
    event.preventDefault()
    selectAndFocus(nextSection)
  }

  return (
    <div
      role="tablist"
      aria-label="Workflow builder"
      className="border-border bg-background flex min-w-0 rounded-md border p-0.5"
    >
      {builderSections.map((nextSection) => (
        <Button
          key={nextSection}
          ref={tabRefs[nextSection]}
          id={`workflow-${nextSection}-builder-tab`}
          type="button"
          role="tab"
          tabIndex={section === nextSection ? 0 : -1}
          aria-selected={section === nextSection}
          aria-disabled={
            nextSection !== section && disabledReason != null ? true : undefined
          }
          aria-controls={`workflow-${nextSection}-builder-panel`}
          variant={section === nextSection ? "secondary" : "ghost"}
          size="sm"
          title={
            nextSection !== section && disabledReason != null
              ? disabledReason
              : undefined
          }
          onClick={() => selectAndFocus(nextSection)}
          onKeyDown={(event) => onKeyDown(event, nextSection)}
        >
          {builderSectionLabels[nextSection]}
        </Button>
      ))}
    </div>
  )
}
