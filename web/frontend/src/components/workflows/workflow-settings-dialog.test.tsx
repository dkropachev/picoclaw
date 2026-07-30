import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import type { WorkflowSettingsResponse } from "@/api/workflows"

import { WorkflowSettingsDialog } from "./workflow-settings-dialog"

describe("WorkflowSettingsDialog", () => {
  it("shows configured and effective values and submits all workflow fields", async () => {
    const user = userEvent.setup()
    const onSave = vi.fn()
    renderSettings({ onSave })

    expect(screen.getByLabelText("Definitions directory")).toHaveValue("")
    expect(
      screen.getByText("Configured: Default (blank) · Effective: workflows"),
    ).toBeInTheDocument()
    expect(
      screen.getByText("Configured: Default (0) · Effective: 4 runs"),
    ).toBeInTheDocument()

    await user.click(screen.getByRole("switch", { name: "Enable workflows" }))
    await user.click(
      screen.getByRole("switch", { name: "Enable workflow tool" }),
    )
    await user.type(
      screen.getByLabelText("Definitions directory"),
      "automations",
    )
    const concurrency = screen.getByLabelText("Concurrent runs")
    await user.clear(concurrency)
    await user.type(concurrency, "6")
    await user.click(screen.getByRole("button", { name: "Save settings" }))

    expect(onSave).toHaveBeenCalledWith(
      {
        enabled: false,
        tool_enabled: true,
        definitions_dir: "automations",
        max_concurrent_runs: 6,
        default_timeout_seconds: 0,
        max_call_depth: 0,
        retention_days: 0,
      },
      "sha256:settings-1",
    )
    expect(screen.getByRole("status")).toHaveTextContent(
      "Workflow tool access will remain blocked",
    )
  })

  it("rejects invalid numeric values before save", async () => {
    const user = userEvent.setup()
    const onSave = vi.fn()
    renderSettings({ onSave })

    const retention = screen.getByLabelText("Run retention")
    await user.clear(retention)
    await user.type(retention, "-1")
    await user.click(screen.getByRole("button", { name: "Save settings" }))

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Run retention must be a non-negative whole number.",
    )
    expect(onSave).not.toHaveBeenCalled()
  })

  it("rejects values above supported runtime limits", async () => {
    const user = userEvent.setup()
    const onSave = vi.fn()
    renderSettings({ onSave })

    const depth = screen.getByLabelText("Maximum call depth")
    await user.clear(depth)
    await user.type(depth, "65")
    await user.click(screen.getByRole("button", { name: "Save settings" }))

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Maximum call depth must be 64 or less.",
    )
    expect(onSave).not.toHaveBeenCalled()
  })

  it("shows reload and gateway restart guidance from returned effects", async () => {
    const user = userEvent.setup()
    const onReload = vi.fn()
    renderSettings({
      settings: workflowSettings({
        effects: {
          launcher_effect: "applied",
          catalog_effect: "reload_required",
          gateway_effect: "restart_required",
        },
      }),
      onReload,
    })

    expect(
      screen.getByText(/Reload workflow definitions to apply/i),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/Restart the gateway from its status controls/i),
    ).toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Reload definitions" }))
    expect(onReload).toHaveBeenCalledTimes(1)
  })

  it("renders only a bounded unavailable message", () => {
    renderSettings({
      settings: undefined,
      unavailable: true,
    })

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Workflow settings are unavailable.",
    )
  })

  it("rehydrates a clean form when a newer settings revision arrives", () => {
    const first = workflowSettings()
    const { rerender } = render(
      <WorkflowSettingsDialog
        open
        onOpenChange={vi.fn()}
        settings={first}
        loading={false}
        unavailable={false}
        saving={false}
        reloading={false}
        onRetry={vi.fn()}
        onSave={vi.fn()}
        onReload={vi.fn()}
      />,
    )

    rerender(
      <WorkflowSettingsDialog
        open
        onOpenChange={vi.fn()}
        settings={workflowSettings({
          config_revision: "sha256:settings-2",
          configured: {
            ...first.configured,
            definitions_dir: "new-workflows",
            retention_days: 90,
          },
        })}
        loading={false}
        unavailable={false}
        saving={false}
        reloading={false}
        onRetry={vi.fn()}
        onSave={vi.fn()}
        onReload={vi.fn()}
      />,
    )

    expect(screen.getByLabelText("Definitions directory")).toHaveValue(
      "new-workflows",
    )
    expect(screen.getByLabelText("Run retention")).toHaveValue(90)
    expect(screen.getByRole("button", { name: "Save settings" })).toBeDisabled()
  })

  it("preserves dirty values and shows a conflict after settings refetch", async () => {
    const user = userEvent.setup()
    const first = workflowSettings()
    const onSave = vi.fn()
    const { rerender } = render(
      <WorkflowSettingsDialog
        open
        onOpenChange={vi.fn()}
        settings={first}
        loading={false}
        unavailable={false}
        saving={false}
        reloading={false}
        onRetry={vi.fn()}
        onSave={onSave}
        onReload={vi.fn()}
      />,
    )
    const definitions = screen.getByLabelText("Definitions directory")
    await user.type(definitions, "my-workflows")

    rerender(
      <WorkflowSettingsDialog
        open
        onOpenChange={vi.fn()}
        settings={workflowSettings({
          config_revision: "sha256:settings-2",
          configured: {
            ...first.configured,
            definitions_dir: "server-workflows",
            retention_days: 90,
          },
        })}
        loading={false}
        unavailable={false}
        saving={false}
        reloading={false}
        onRetry={vi.fn()}
        onSave={onSave}
        onReload={vi.fn()}
      />,
    )

    expect(definitions).toHaveValue("my-workflows")
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Workflow settings changed elsewhere.",
    )
    expect(screen.getByRole("button", { name: "Save settings" })).toBeDisabled()

    await user.click(
      screen.getByRole("button", { name: "Reload latest values" }),
    )
    expect(definitions).toHaveValue("server-workflows")
    expect(screen.getByLabelText("Run retention")).toHaveValue(90)
    expect(screen.getByRole("button", { name: "Save settings" })).toBeDisabled()
    expect(onSave).not.toHaveBeenCalled()
  })

  it("rebases disjoint local edits without overwriting a concurrent tool setting", async () => {
    const user = userEvent.setup()
    const first = workflowSettings()
    const onSave = vi.fn()
    const view = renderSettings({ settings: first, onSave })

    const retention = screen.getByLabelText("Run retention")
    await user.clear(retention)
    await user.type(retention, "60")

    view.rerender(
      <WorkflowSettingsDialog
        open
        onOpenChange={vi.fn()}
        settings={workflowSettings({
          config_revision: "sha256:settings-2",
          configured: {
            ...first.configured,
            tool_enabled: true,
          },
          effective: {
            ...first.effective,
            tool_enabled: true,
          },
        })}
        loading={false}
        unavailable={false}
        saving={false}
        reloading={false}
        onRetry={vi.fn()}
        onSave={onSave}
        onReload={vi.fn()}
      />,
    )

    expect(
      screen.getByRole("switch", { name: "Enable workflow tool" }),
    ).toBeChecked()
    expect(retention).toHaveValue(60)
    await user.click(screen.getByRole("button", { name: "Save settings" }))

    expect(onSave).toHaveBeenCalledWith(
      expect.objectContaining({
        tool_enabled: true,
        retention_days: 60,
      }),
      "sha256:settings-2",
    )
  })
})

function renderSettings({
  settings = workflowSettings(),
  unavailable = false,
  onSave = vi.fn(),
  onReload = vi.fn(),
}: {
  settings?: WorkflowSettingsResponse
  unavailable?: boolean
  onSave?: (
    values: WorkflowSettingsResponse["configured"],
    expectedRevision: string,
  ) => void
  onReload?: () => void
} = {}) {
  return render(
    <WorkflowSettingsDialog
      open
      onOpenChange={vi.fn()}
      settings={settings}
      loading={false}
      unavailable={unavailable}
      saving={false}
      reloading={false}
      onRetry={vi.fn()}
      onSave={onSave}
      onReload={onReload}
    />,
  )
}

function workflowSettings(
  overrides: Partial<WorkflowSettingsResponse> = {},
): WorkflowSettingsResponse {
  return {
    configured: {
      enabled: true,
      tool_enabled: false,
      definitions_dir: "",
      max_concurrent_runs: 0,
      default_timeout_seconds: 0,
      max_call_depth: 0,
      retention_days: 0,
    },
    effective: {
      enabled: true,
      tool_enabled: false,
      definitions_dir: "workflows",
      max_concurrent_runs: 4,
      default_timeout_seconds: 300,
      max_call_depth: 8,
      retention_days: 30,
    },
    config_revision: "sha256:settings-1",
    effects: {
      launcher_effect: "applied",
      catalog_effect: "applied",
      gateway_effect: "applied",
    },
    ...overrides,
  }
}
