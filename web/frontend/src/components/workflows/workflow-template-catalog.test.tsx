import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactElement } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  type WorkflowDefinitionInspection,
  inspectWorkflowTemplate,
} from "@/api/workflows"

import { WorkflowTemplateCatalog } from "./workflow-template-catalog"

vi.mock("@/api/workflows", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/workflows")>()
  return {
    ...actual,
    inspectWorkflowTemplate: vi.fn(),
  }
})

describe("WorkflowTemplateCatalog", () => {
  beforeEach(() => {
    vi.mocked(inspectWorkflowTemplate).mockReset()
    vi.mocked(inspectWorkflowTemplate).mockImplementation((name) =>
      Promise.resolve(templateInspection(name)),
    )
  })

  it("offers install, installed, modified, and blocked states safely", async () => {
    const onInstall = vi.fn()
    const user = userEvent.setup()
    renderCatalog(
      <WorkflowTemplateCatalog
        templates={[
          {
            name: "available-template",
            ref: "workflows/available.yml",
            state: "available",
          },
          {
            name: "installed-template",
            ref: "workflows/installed.yml",
            state: "installed",
          },
          {
            name: "modified-template",
            ref: "workflows/modified.yml",
            state: "modified",
          },
          {
            name: "blocked-template",
            ref: "workflows/blocked.yml",
            state: "blocked",
            blocked_reason: "target_not_regular",
          },
        ]}
        loading={false}
        unavailable={false}
        onInstall={onInstall}
      />,
    )

    const available = screen.getByRole("article", {
      name: "Available Template template",
    })
    await user.click(within(available).getByRole("button", { name: "Install" }))
    expect(onInstall).toHaveBeenCalledWith("available-template", false)

    expect(
      screen.getByText("Installed and byte-for-byte current"),
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        "The target is not a regular file. Resolve it manually.",
      ),
    ).toBeInTheDocument()
    expect(
      within(
        screen.getByRole("article", { name: "Blocked Template template" }),
      ).queryByRole("button", { name: /^(Install|Restore built-in)$/ }),
    ).not.toBeInTheDocument()
  })

  it("requires explicit confirmation before restoring a modified template", async () => {
    const onInstall = vi.fn()
    const user = userEvent.setup()
    renderCatalog(
      <WorkflowTemplateCatalog
        templates={[
          {
            name: "code-review",
            ref: "workflows/code-review.yml",
            state: "modified",
          },
        ]}
        loading={false}
        unavailable={false}
        onInstall={onInstall}
      />,
    )

    await user.click(screen.getByRole("button", { name: "Restore built-in" }))
    const dialog = screen.getByRole("alertdialog")
    expect(
      within(dialog).getByText(/replaces local changes/i),
    ).toBeInTheDocument()
    expect(onInstall).not.toHaveBeenCalled()

    await user.click(
      within(dialog).getByRole("button", { name: "Restore built-in" }),
    )
    expect(onInstall).toHaveBeenCalledWith("code-review", true)
  })

  it("keeps catalog states visible but disables mutations during active development", async () => {
    const user = userEvent.setup()
    renderCatalog(
      <WorkflowTemplateCatalog
        templates={[
          {
            name: "available-template",
            ref: "workflows/available.yml",
            state: "available",
          },
          {
            name: "modified-template",
            ref: "workflows/modified.yml",
            state: "modified",
          },
        ]}
        loading={false}
        unavailable={false}
        disabled
        disabledReason="Finish or discard the active workflow draft before installing or restoring templates."
        onInstall={vi.fn()}
      />,
    )

    expect(screen.getByRole("status")).toHaveTextContent(
      "Finish or discard the active workflow draft",
    )
    expect(screen.getByRole("button", { name: "Install" })).toBeDisabled()
    expect(
      screen.getByRole("button", { name: "Restore built-in" }),
    ).toBeDisabled()
    expect(inspectWorkflowTemplate).not.toHaveBeenCalled()
    const available = screen.getByRole("article", {
      name: "Available Template template",
    })
    const modified = screen.getByRole("article", {
      name: "Modified Template template",
    })
    const availableInspection = within(available).getByRole("button", {
      name: "Built-in definition: available-template",
    })
    const modifiedInspection = within(modified).getByRole("button", {
      name: "Built-in definition: modified-template",
    })
    expect(availableInspection).toBeEnabled()
    expect(modifiedInspection).toBeEnabled()
    await user.click(availableInspection)
    await user.click(modifiedInspection)
    await waitFor(() => {
      expect(inspectWorkflowTemplate).toHaveBeenCalledWith(
        "available-template",
        expect.any(AbortSignal),
      )
      expect(inspectWorkflowTemplate).toHaveBeenCalledWith(
        "modified-template",
        expect.any(AbortSignal),
      )
    })
  })

  it("uses a bounded unavailable message", () => {
    renderCatalog(
      <WorkflowTemplateCatalog
        templates={[]}
        loading={false}
        unavailable
        onInstall={vi.fn()}
      />,
    )

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Built-in workflow templates are unavailable. Refresh to try again.",
    )
  })

  it("shows operator guidance instead of retry advice for recovery conflicts", () => {
    renderCatalog(
      <WorkflowTemplateCatalog
        templates={[]}
        loading={false}
        unavailable
        unavailableMessage="Workflow recovery found files changed outside the interrupted transaction. Operator reconciliation is required; no files were changed."
        onInstall={vi.fn()}
      />,
    )

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Operator reconciliation is required",
    )
    expect(screen.getByRole("alert")).not.toHaveTextContent("Refresh")
  })
})

function renderCatalog(element: ReactElement) {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: Number.POSITIVE_INFINITY },
    },
  })
  return render(
    <QueryClientProvider client={client}>{element}</QueryClientProvider>,
  )
}

function templateInspection(name: string): WorkflowDefinitionInspection {
  return {
    source: { kind: "template", template_name: name },
    revision: `revision:${name}`,
    complete: true,
    validation: {
      valid: true,
      issue_count: 0,
      issues: [],
      truncated: false,
    },
    triggers: {
      manual: { present: false, projected: true },
      schedule: { present: false, projected: true },
      channel_message: { present: false, projected: true },
      command: { present: false, projected: true },
      runtime_event: { present: false, projected: true },
      event: { present: false, projected: true },
      workflow_call: { present: false, projected: true },
    },
    jobs: [],
    dependencies: [],
    effects: [],
    limits: [],
  }
}
