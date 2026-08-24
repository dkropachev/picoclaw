import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  type DevelopmentWorkspace,
  confirmDevelopmentCharter,
  createDevelopmentRequestID,
  reconcileDevelopmentPublication,
  respondDevelopmentGate,
  saveDevelopmentCharter,
} from "@/api/development-workspaces"
import { DevelopmentActionPanel } from "@/components/development-workspaces/development-action-panel"

vi.mock("@/api/development-workspaces", async (importOriginal) => {
  const original =
    await importOriginal<typeof import("@/api/development-workspaces")>()
  return {
    ...original,
    confirmDevelopmentCharter: vi.fn(),
    createDevelopmentRequestID: vi.fn(),
    reconcileDevelopmentPublication: vi.fn(),
    respondDevelopmentGate: vi.fn(),
    saveDevelopmentCharter: vi.fn(),
  }
})

const workspaceID = `devw_${"1".repeat(32)}`
const gateID = `pgr_${"2".repeat(32)}`
const publicationID = `ppu_${"3".repeat(32)}`
const workspace: DevelopmentWorkspace = {
  id: workspaceID,
  intent: "implement_feature",
  source_kind: "issue",
  repository: "octo/repo",
  title: "Retry feedback",
  phase: "publication",
  execution_state: "waiting_user",
  version: 7,
  created_at: "2026-08-24T10:00:00Z",
  updated_at: "2026-08-24T10:05:00Z",
  source: { kind: "issue", url: "https://github.com/octo/repo/issues/7" },
  head_revision: "provider:7",
  changed_files: [],
  activity: [],
  validation_checks: [],
  gates: [
    {
      id: gateID,
      decision_point: "development.publish",
      state: "waiting_user",
      created_at: "2026-08-24T10:04:00Z",
      turns: [
        {
          stage_id: "stage-1",
          kind: "human",
          title: "Approve draft PR publication",
          status: "waiting_user",
          gate_form: {
            gate_ref: "publish",
            prompt: "Confirm publication intent.",
            fields: [
              {
                id: "reason",
                type: "short-text",
                label: "Approval note",
                required: true,
                min_selections: 0,
                max_selections: 0,
                options: [],
              },
            ],
          },
        },
      ],
    },
  ],
  publications: [],
}

function renderPanel(
  value: DevelopmentWorkspace,
  entityID?: string,
  panel: "charter" | "publication" = "publication",
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={queryClient}>
      <DevelopmentActionPanel
        workspace={value}
        requestedPanel={panel}
        requestedEntityID={entityID}
      />
    </QueryClientProvider>,
  )
}

describe("development required action", () => {
  beforeEach(() => {
    vi.mocked(createDevelopmentRequestID).mockReset()
    vi.mocked(confirmDevelopmentCharter).mockReset()
    vi.mocked(respondDevelopmentGate).mockReset()
    vi.mocked(saveDevelopmentCharter).mockReset()
    vi.mocked(reconcileDevelopmentPublication).mockReset()
    vi.mocked(createDevelopmentRequestID).mockReturnValue(
      `devq_${"4".repeat(32)}`,
    )
    vi.mocked(respondDevelopmentGate).mockResolvedValue({
      ...workspace,
      execution_state: "running",
      version: 8,
      gates: [],
    })
  })

  it("submits a revision-fenced response to the deep-linked human gate", async () => {
    const user = userEvent.setup()
    renderPanel(workspace, gateID)

    expect(screen.getByText("Approve draft PR publication")).toBeVisible()
    const submit = screen.getByRole("button", { name: "Submit response" })
    expect(submit).toBeDisabled()
    await user.type(screen.getByLabelText("Approval note *"), "Approved.")
    await user.click(submit)

    await waitFor(() =>
      expect(respondDevelopmentGate).toHaveBeenCalledWith(workspaceID, gateID, {
        expected_version: 7,
        request_id: `devq_${"4".repeat(32)}`,
        field_values: { reason: "Approved." },
      }),
    )
  })

  it("reconciles an unknown provider outcome without retrying publication", async () => {
    const user = userEvent.setup()
    const withUnknown: DevelopmentWorkspace = {
      ...workspace,
      execution_state: "unknown",
      gates: [],
      publications: [
        {
          id: publicationID,
          kind: "branch_push",
          state: "unknown",
          updated_at: "2026-08-24T10:05:00Z",
        },
      ],
    }
    vi.mocked(reconcileDevelopmentPublication).mockResolvedValue({
      ...withUnknown,
      execution_state: "running",
      version: 8,
      publications: [],
    })
    renderPanel(withUnknown, publicationID)
    await user.click(screen.getByRole("button", { name: "Reconcile outcome" }))

    await waitFor(() =>
      expect(reconcileDevelopmentPublication).toHaveBeenCalledWith(
        workspaceID,
        publicationID,
        {
          expected_version: 7,
          expected_head_revision: "provider:7",
          request_id: `devq_${"4".repeat(32)}`,
        },
      ),
    )
  })

  it("shows charter ambiguity and saves a clarified revision", async () => {
    const user = userEvent.setup()
    const charterID = `pcr_${"5".repeat(32)}`
    const ambiguous: DevelopmentWorkspace = {
      ...workspace,
      phase: "charter",
      gates: [],
      charter: {
        id: charterID,
        revision: 1,
        type: "feature",
        goal: "Add notifications",
        acceptance_criteria: ["Deliver an inbox"],
        included_areas: ["mobile"],
        excluded_areas: ["email"],
        non_goals: [],
        clarification_needed: true,
        clarification_question: "Which mobile platforms are required?",
        confirmed: false,
      },
    }
    vi.mocked(saveDevelopmentCharter).mockResolvedValue({
      ...ambiguous,
      version: 8,
      charter: { ...ambiguous.charter!, clarification_needed: false },
    })
    renderPanel(ambiguous, charterID, "charter")
    expect(
      screen.getByText("Which mobile platforms are required?"),
    ).toBeVisible()
    const goal = screen.getByLabelText("Goal")
    await user.clear(goal)
    await user.type(goal, "Add iOS and Android notifications")
    await user.click(
      screen.getByRole("button", { name: "Save clarified charter" }),
    )
    await waitFor(() =>
      expect(saveDevelopmentCharter).toHaveBeenCalledWith(workspaceID, {
        expected_version: 7,
        expected_head_revision: "provider:7",
        request_id: `devq_${"4".repeat(32)}`,
        charter: {
          type: "feature",
          goal: "Add iOS and Android notifications",
          acceptance_criteria: ["Deliver an inbox"],
          included_areas: ["mobile"],
          excluded_areas: ["email"],
          non_goals: [],
        },
      }),
    )
  })

  it("allows an explicit acceptance of the ambiguous draft", async () => {
    const user = userEvent.setup()
    const charterID = `pcr_${"6".repeat(32)}`
    const ambiguous: DevelopmentWorkspace = {
      ...workspace,
      phase: "charter",
      gates: [],
      charter: {
        id: charterID,
        revision: 2,
        type: "feature",
        goal: "Add notifications",
        acceptance_criteria: ["Deliver an inbox"],
        included_areas: [],
        excluded_areas: [],
        non_goals: [],
        clarification_needed: true,
        clarification_question: "Use native push?",
        confirmed: false,
      },
    }
    vi.mocked(confirmDevelopmentCharter).mockResolvedValue({
      ...ambiguous,
      version: 8,
      phase: "planning",
      execution_state: "queued",
      charter: { ...ambiguous.charter!, confirmed: true },
    })
    renderPanel(ambiguous, charterID, "charter")
    await user.click(screen.getByRole("button", { name: "Accept draft as-is" }))
    await waitFor(() =>
      expect(confirmDevelopmentCharter).toHaveBeenCalledWith(workspaceID, {
        expected_version: 7,
        expected_charter_revision: 2,
        request_id: `devq_${"4".repeat(32)}`,
      }),
    )
  })
})
