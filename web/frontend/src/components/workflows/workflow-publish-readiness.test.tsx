import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ComponentProps } from "react"
import { describe, expect, it, vi } from "vitest"

import type { WorkflowDependencyCheckResponse } from "@/api/workflows"

import {
  WorkflowDependencyReadinessPanel,
  WorkflowPublishReadinessPanel,
} from "./workflow-publish-readiness"

describe("WorkflowPublishReadinessPanel", () => {
  it("shows bounded loading, stale, and unavailable states", () => {
    const view = renderReadiness({ dependencyState: "loading" })
    expect(screen.getByRole("status")).toHaveTextContent(
      "Checking dependencies for the exact current draft",
    )

    view.rerender(
      <WorkflowPublishReadinessPanel
        targetReady
        yamlReady
        validationStatus="valid"
        testStatus="succeeded"
        dependencyState="stale"
        readinessMessage="Waiting for dependencies."
      />,
    )
    expect(screen.getByRole("status")).toHaveTextContent(
      "The dependency result is stale",
    )

    view.rerender(
      <WorkflowPublishReadinessPanel
        targetReady
        yamlReady
        validationStatus="valid"
        testStatus="succeeded"
        dependencyState="error"
        readinessMessage="Dependency readiness could not be checked."
      />,
    )
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Workflow dependency readiness is unavailable.",
    )
  })

  it("lists structural blockers and every runtime occurrence with fixed guidance", () => {
    renderReadiness({
      dependencyState: "current",
      dependencyReport: dependencyReport({
        structural_ready: false,
        runtime_ready: false,
        ready: false,
        structural_issues: [
          {
            code: "missing_required_input",
            workflow_ref: "workflows/review.yml",
            path: "jobs.shared.with.owner",
            dependency_kind: "reusable",
            dependency_name: "workflows/shared.yml",
          },
          {
            code: "human_task_reusable_unsupported",
            workflow_ref: "workflows/review.yml",
            path: "jobs.review.steps[0].uses",
            dependency_kind: "human",
            dependency_name: "task",
          },
        ],
        dependencies: [
          {
            dependency: {
              kind: "mcp",
              name: "github/get_pull_request",
              workflow_ref: "workflows/review.yml",
              path: "jobs.review.steps[0].uses",
            },
            code: "not_connected",
            ready: false,
          },
          {
            dependency: {
              kind: "function",
              name: "render_summary",
              workflow_ref: "workflows/shared.yml",
              path: "jobs.render.steps[0].uses",
            },
            code: "ready",
            ready: true,
          },
        ],
      }),
    })

    const panel = screen.getByRole("region", { name: "Publish readiness" })
    expect(
      within(panel).getByText("Structural blockers (2)"),
    ).toBeInTheDocument()
    expect(
      within(panel).getByText(
        "Map the required input or add a default in the reusable workflow.",
      ),
    ).toBeInTheDocument()
    expect(
      within(panel).getByText(
        "Keep human/task and reusable workflow calls in separate workflow closures.",
      ),
    ).toBeInTheDocument()
    expect(
      within(panel).getByText("mcp/github/get_pull_request"),
    ).toBeInTheDocument()
    expect(
      within(panel).getByText("Connect or start the required runtime server."),
    ).toBeInTheDocument()
    expect(
      within(panel).getByText("function/render_summary"),
    ).toBeInTheDocument()
    expect(within(panel).getByText("Ready now.")).toBeInTheDocument()
    expect(
      within(panel).queryByText("opaque:must-not-be-displayed"),
    ).not.toBeInTheDocument()
  })

  it("makes the workflows-disabled state an explicit blocker", () => {
    renderReadiness({
      dependencyState: "current",
      dependencyReport: dependencyReport({
        ready: false,
        workflow_enabled: false,
      }),
    })

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Workflows are disabled. Enable workflows in Settings before publishing.",
    )
    expect(
      screen.getByText("No declared runtime dependencies."),
    ).toBeInTheDocument()
  })

  it("reduces unknown readiness codes to bounded fallback guidance", () => {
    renderReadiness({
      dependencyState: "current",
      dependencyReport: dependencyReport({
        ready: false,
        structural_ready: false,
        runtime_ready: false,
        structural_issues: [
          {
            code: "raw structural failure: /private/path" as never,
            workflow_ref: "workflows/review.yml",
            path: "jobs.review",
          },
        ],
        dependencies: [
          {
            dependency: {
              kind: "tool",
              name: "review",
              workflow_ref: "workflows/review.yml",
              path: "jobs.review.steps[0].uses",
            },
            code: "raw provider failure: secret-token" as never,
            ready: false,
          },
        ],
      }),
    })

    expect(
      screen.queryByText(/raw structural failure|raw provider failure/),
    ).not.toBeInTheDocument()
    expect(screen.getByText("structural blocker")).toBeInTheDocument()
    expect(
      screen.getByText(
        "Check the runtime and retry when this capability is available.",
      ),
    ).toBeInTheDocument()
  })

  it("shows published-ref readiness and offers a bounded retry", async () => {
    const user = userEvent.setup()
    const onRetry = vi.fn()
    const view = render(
      <WorkflowDependencyReadinessPanel
        workflowRef="workflows/published.yml"
        dependencyState="current"
        dependencyReport={dependencyReport({
          root_ref: "workflows/published.yml",
        })}
        onRetry={onRetry}
      />,
    )

    const panel = screen.getByRole("region", {
      name: "Published workflow dependency readiness",
    })
    expect(within(panel).getByText("workflows/published.yml")).toBeVisible()
    expect(
      within(panel).getByText("No declared runtime dependencies."),
    ).toBeVisible()

    view.rerender(
      <WorkflowDependencyReadinessPanel
        workflowRef="workflows/published.yml"
        dependencyState="error"
        onRetry={onRetry}
      />,
    )
    expect(within(panel).getByRole("alert")).toHaveTextContent(
      "Published workflow dependency readiness is unavailable.",
    )
    await user.click(
      within(panel).getByRole("button", { name: "Retry dependency check" }),
    )
    expect(onRetry).toHaveBeenCalledOnce()
  })
})

function renderReadiness({
  dependencyState = "current",
  dependencyReport: report = dependencyReport(),
}: {
  dependencyState?: ComponentProps<
    typeof WorkflowPublishReadinessPanel
  >["dependencyState"]
  dependencyReport?: WorkflowDependencyCheckResponse
} = {}) {
  return render(
    <WorkflowPublishReadinessPanel
      targetReady
      yamlReady
      validationStatus="valid"
      testStatus="succeeded"
      dependencyState={dependencyState}
      dependencyReport={report}
      readinessMessage="Resolve the listed blockers before publishing."
    />,
  )
}

function dependencyReport(
  overrides: Partial<WorkflowDependencyCheckResponse> = {},
): WorkflowDependencyCheckResponse {
  return {
    root_ref: "workflows/review.yml",
    revision: "opaque:must-not-be-displayed",
    ready: true,
    workflow_enabled: true,
    structural_ready: true,
    runtime_ready: true,
    dependencies: [],
    structural_issues: [],
    ...overrides,
  }
}
