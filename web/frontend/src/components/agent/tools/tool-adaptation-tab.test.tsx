import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { useState } from "react"
import { beforeAll, describe, expect, it, vi } from "vitest"

import type {
  ToolAdaptationConfig,
  ToolAdaptationProbeTarget,
  ToolAdaptationProfileState,
} from "@/api/tools"
import { ToolAdaptationTab } from "@/components/agent/tools/tool-adaptation-tab"

const profile: ToolAdaptationProfileState = {
  id: "openai/gpt-5.4",
  label: "primary",
  source: "model alias",
  is_default: true,
  is_override: false,
  probe_available: true,
  probe_account_ref: "openai-work",
  probe_model_alias: "coding",
  resolved: {
    provider: "openai",
    model: "gpt-5.4",
    state_path: "/tmp/tool-adaptation.json",
    visible_tool_surface: "codex",
    pinned_tool_surface: "codex",
    surface_evidence: "heuristic",
    runtime_downgrade: false,
    runtime_promotion: false,
    apply_visible_changes: "next_session",
    cache_sensitive: true,
    cache_evidence: "heuristic",
  },
}

const baseDraft: ToolAdaptationConfig = {
  enabled: true,
  visible_tool_surface: "auto",
  learn_from_tool_calls: true,
  run_model_probes: true,
  allow_runtime_downgrade: "auto",
  allow_runtime_promotion: "auto",
  apply_visible_changes: "next_session",
  cache_sensitive_apis: "auto",
  cache_breaking_downgrade: false,
  profile_overrides: [],
  profiles: [profile],
}

describe("ToolAdaptationTab", () => {
  beforeAll(() => {
    Object.defineProperty(window, "scrollTo", {
      writable: true,
      value: vi.fn(),
    })
    Object.defineProperties(HTMLElement.prototype, {
      hasPointerCapture: {
        configurable: true,
        value: vi.fn(() => false),
      },
      setPointerCapture: {
        configurable: true,
        value: vi.fn(),
      },
      releasePointerCapture: {
        configurable: true,
        value: vi.fn(),
      },
      scrollIntoView: {
        configurable: true,
        value: vi.fn(),
      },
    })
  })

  it("runs a probe for the selected profile", async () => {
    const user = userEvent.setup()
    const onRunProbe = vi.fn()

    renderTab({ onRunProbe })
    await user.click(
      screen.getByRole("button", {
        name: "Run probe for openai / gpt-5.4",
      }),
    )

    expect(onRunProbe).toHaveBeenCalledWith({
      account_ref: "openai-work",
      model_alias: "coding",
    })
  })

  it("suppresses the embedded page heading inside a routed detail shell", () => {
    renderTab({ showHeader: false })

    expect(
      screen.queryByRole("heading", { level: 1, name: "Adaptation" }),
    ).toBeNull()
    expect(screen.getByRole("button", { name: "Save Changes" })).toBeVisible()
  })

  it("shows an accessible probing state for the selected profile", () => {
    renderTab({
      isProbing: true,
      probingProfile: {
        account_ref: "openai-work",
        model_alias: "coding",
      },
    })

    const probeButton = screen.getByRole("button", {
      name: "Probing openai / gpt-5.4",
    })
    expect(probeButton).toBeDisabled()
    expect(probeButton).toHaveAttribute("aria-busy", "true")
    expect(probeButton).toHaveTextContent("Probing")
    expect(probeButton).toHaveAccessibleDescription(
      "Probe is running for this profile.",
    )
  })

  it("shows when a profile cannot be probed", () => {
    renderTab({
      draft: {
        ...baseDraft,
        profiles: [{ ...profile, probe_available: false }],
      },
    })

    const probeButton = screen.getByRole("button", {
      name: "Probe unavailable for openai / gpt-5.4",
    })
    expect(probeButton).toBeDisabled()
    expect(probeButton).toHaveTextContent("Probe unavailable")
    expect(probeButton).toHaveAccessibleDescription(
      "No configured credentials or endpoint are available for this profile.",
    )
  })

  it.each([
    {
      state: "unsaved settings",
      draft: baseDraft,
      isDirty: true,
      isSaving: false,
      isProbing: false,
      probingProfile: null,
      reason: "Save changes before running a probe.",
    },
    {
      state: "disabled adaptation",
      draft: { ...baseDraft, enabled: false },
      isDirty: false,
      isSaving: false,
      isProbing: false,
      probingProfile: null,
      reason: "Enable tool adaptation before running probes.",
    },
    {
      state: "disabled model probes",
      draft: { ...baseDraft, run_model_probes: false },
      isDirty: false,
      isSaving: false,
      isProbing: false,
      probingProfile: null,
      reason: "Enable model probes before running a probe.",
    },
    {
      state: "saving",
      draft: baseDraft,
      isDirty: true,
      isSaving: true,
      isProbing: false,
      probingProfile: null,
      reason: "Saving adaptation changes.",
    },
    {
      state: "another active probe",
      draft: baseDraft,
      isDirty: false,
      isSaving: false,
      isProbing: true,
      probingProfile: {
        account_ref: "anthropic-work",
        model_alias: "coding",
      },
      reason: "Another profile probe is running.",
    },
  ])(
    "shows why probing is disabled for $state",
    ({ draft, isDirty, isSaving, isProbing, probingProfile, reason }) => {
      renderTab({
        draft,
        isDirty,
        isSaving,
        isProbing,
        probingProfile,
      })

      const probeButton = screen.getByRole("button", {
        name: "Run probe for openai / gpt-5.4",
      })
      expect(probeButton).toBeDisabled()
      expect(probeButton).toHaveAccessibleDescription(reason)
      expect(screen.getByText(reason)).toBeVisible()
    },
  )

  it("does not assume a legacy resolved profile is probeable", () => {
    renderTab({
      draft: {
        ...baseDraft,
        profiles: undefined,
        resolved: profile.resolved,
      },
    })

    const probeButton = screen.getByRole("button", {
      name: "Probe unavailable for openai / gpt-5.4",
    })
    expect(probeButton).toBeDisabled()
    expect(probeButton).toHaveAccessibleDescription(
      "No configured credentials or endpoint are available for this profile.",
    )
  })

  it("exposes compact surface and cache state for mobile rows", () => {
    renderTab()

    const metrics = screen.getByTestId(
      "adaptation-profile-mobile-metrics-openai/gpt-5.4",
    )
    expect(metrics).toHaveClass("sm:hidden")
    expect(within(metrics).getByText("Surface")).toBeVisible()
    expect(within(metrics).getByText("codex")).toBeVisible()
    expect(within(metrics).getByText("Cache")).toBeVisible()
    expect(within(metrics).getByText("Sensitive")).toBeVisible()
  })

  it.each([
    { state: "saving", isSaving: true, isProbing: false },
    { state: "probing", isSaving: false, isProbing: true },
  ])(
    "disables every adaptation mutation while $state",
    ({ isSaving, isProbing }) => {
      renderTab({
        isDirty: true,
        isSaving,
        isProbing,
        probingProfile: isProbing
          ? { account_ref: "openai-work", model_alias: "coding" }
          : null,
      })

      expect(
        screen.getByRole("button", { name: "Save Changes" }),
      ).toBeDisabled()
      for (const control of screen.getAllByRole("switch")) {
        expect(control).toBeDisabled()
      }
      for (const control of screen.getAllByRole("combobox")) {
        expect(control).toBeDisabled()
      }
      expect(
        screen.getByRole("button", { name: "Add profile override" }),
      ).toBeDisabled()
      expect(
        screen.getByRole("button", {
          name: "Add override for openai / gpt-5.4",
        }),
      ).toBeDisabled()
    },
  )

  it("disables adding when every configured profile has an override", () => {
    renderTab({
      draft: {
        ...baseDraft,
        profile_overrides: [
          {
            provider: "openai",
            model: "gpt-5.4",
            visible_tool_surface: "simple",
          },
        ],
      },
    })

    expect(
      screen.getByRole("button", { name: "Add profile override" }),
    ).toBeDisabled()
  })

  it("adds an override from a configured profile row", async () => {
    const user = userEvent.setup()
    let updatedDraft: ToolAdaptationConfig | undefined
    const onUpdateDraft = vi.fn(
      (updater: (current: ToolAdaptationConfig) => ToolAdaptationConfig) => {
        updatedDraft = updater(baseDraft)
      },
    )

    renderTab({ onUpdateDraft })
    await user.click(
      screen.getByRole("button", {
        name: "Add override for openai / gpt-5.4",
      }),
    )
    await user.click(
      screen.getByRole("combobox", { name: "Visible Tool Surface" }),
    )
    await user.click(screen.getByRole("option", { name: "Simple" }))
    await user.click(screen.getByRole("button", { name: "Add" }))

    expect(updatedDraft?.profile_overrides).toEqual([
      {
        provider: "openai",
        model: "gpt-5.4",
        visible_tool_surface: "simple",
      },
    ])
  })

  it("shows a pending override value before save", async () => {
    const user = userEvent.setup()
    render(<ControlledTab />)

    await user.click(
      screen.getByRole("button", {
        name: "Add override for openai / gpt-5.4",
      }),
    )
    await user.click(
      screen.getByRole("combobox", { name: "Visible Tool Surface" }),
    )
    await user.click(screen.getByRole("option", { name: "Simple" }))
    await user.click(screen.getByRole("button", { name: "Add" }))

    expect(screen.getAllByText("Pending override")).not.toHaveLength(0)
    expect(screen.getAllByText("simple")).not.toHaveLength(0)
  })

  it("removes an override without deleting the derived profile", async () => {
    const user = userEvent.setup()
    const draft: ToolAdaptationConfig = {
      ...baseDraft,
      profile_overrides: [
        {
          provider: "openai",
          model: "gpt-5.4",
          visible_tool_surface: "simple",
        },
      ],
    }
    let updatedDraft: ToolAdaptationConfig | undefined
    const onUpdateDraft = vi.fn(
      (updater: (current: ToolAdaptationConfig) => ToolAdaptationConfig) => {
        updatedDraft = updater(draft)
      },
    )

    renderTab({ draft, onUpdateDraft })
    await user.click(
      screen.getByRole("button", {
        name: "Remove override for openai / gpt-5.4",
      }),
    )

    expect(updatedDraft?.profile_overrides).toEqual([])
    expect(screen.getByText("openai / gpt-5.4")).toBeInTheDocument()
  })

  it("shows a pending override removal before save", async () => {
    const user = userEvent.setup()
    render(<ControlledTab initialOverride />)

    await user.click(
      screen.getByRole("button", {
        name: "Remove override for openai / gpt-5.4",
      }),
    )

    expect(screen.getAllByText("Pending removal")).not.toHaveLength(0)
    expect(screen.getByText("openai / gpt-5.4")).toBeInTheDocument()
  })
})

function renderTab({
  draft = baseDraft,
  isDirty = false,
  isSaving = false,
  isProbing = false,
  probingProfile = null,
  onRunProbe = vi.fn(),
  onUpdateDraft = vi.fn(),
  showHeader,
}: {
  draft?: ToolAdaptationConfig
  isDirty?: boolean
  isSaving?: boolean
  isProbing?: boolean
  probingProfile?: ToolAdaptationProbeTarget | null
  onRunProbe?: (profile: ToolAdaptationProbeTarget) => void
  onUpdateDraft?: (
    updater: (current: ToolAdaptationConfig) => ToolAdaptationConfig,
  ) => void
  showHeader?: boolean
} = {}) {
  return render(
    <ToolAdaptationTab
      showHeader={showHeader}
      draft={draft}
      isLoading={false}
      hasError={false}
      isSaving={isSaving}
      isProbing={isProbing}
      probingProfile={probingProfile}
      isDirty={isDirty}
      onSave={vi.fn()}
      onRunProbe={onRunProbe}
      onUpdateDraft={onUpdateDraft}
    />,
  )
}

function ControlledTab({
  initialOverride = false,
}: {
  initialOverride?: boolean
}) {
  const [draft, setDraft] = useState<ToolAdaptationConfig>({
    ...baseDraft,
    profile_overrides: initialOverride
      ? [
          {
            provider: "openai",
            model: "gpt-5.4",
            visible_tool_surface: "simple",
          },
        ]
      : [],
    profiles: [
      {
        ...profile,
        is_override: initialOverride,
        resolved: initialOverride
          ? {
              ...profile.resolved,
              visible_tool_surface: "simple",
              pinned_tool_surface: "simple",
              surface_evidence: "config",
            }
          : profile.resolved,
      },
    ],
  })
  const [isDirty, setIsDirty] = useState(false)

  return (
    <ToolAdaptationTab
      draft={draft}
      isLoading={false}
      hasError={false}
      isSaving={false}
      isProbing={false}
      probingProfile={null}
      isDirty={isDirty}
      onSave={vi.fn()}
      onRunProbe={vi.fn()}
      onUpdateDraft={(updater) => {
        setDraft((current) => updater(current))
        setIsDirty(true)
      }}
    />
  )
}
