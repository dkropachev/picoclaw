import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import type {
  EvaluationOptions,
  EvaluationProfileOption,
  RepositoryModelEvaluation,
} from "@/api/model-evaluations"
import {
  createModelEvaluation,
  getModelEvaluationOptions,
} from "@/api/model-evaluations"

import { ModelEvaluationEditorPage } from "./model-evaluation-collection"

vi.mock("@/api/model-evaluations", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/model-evaluations")>()),
  createModelEvaluation: vi.fn(),
  getModelEvaluationOptions: vi.fn(),
}))

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}))

vi.mock("@/components/page-header", () => ({
  PageHeader: ({
    title,
    children,
  }: {
    title: string
    children?: ReactNode
  }) => (
    <header>
      <h1>{title}</h1>
      {children}
    </header>
  ),
}))

const focus = {
  code_types: ["hotpath-code" as const, "code" as const],
  include_folders: [],
  exclude_folders: [],
  free_text: "",
}

const defaultProfile: EvaluationProfileOption = {
  id: "rrpf_default",
  version: 4,
  name: "default",
  reviewer_model: "review",
  account_ref: "router-1",
  review_focus: "Find correctness, security, and reliability bugs.",
  focus,
  max_files_per_batch: 24,
  max_content_bytes_per_batch: 524_288,
  max_parallel_children: 16,
  available_models: ["review", "review-cheap", "review-cheap-2"],
}

const anotherProfile: EvaluationProfileOption = {
  ...defaultProfile,
  id: "rrpf_another",
  version: 1,
  name: "another",
  reviewer_model: "chat",
  max_parallel_children: 8,
  available_models: ["chat", "review", "review-cheap", "review-cheap-2"],
}

const options: EvaluationOptions = {
  models: [
    { alias: "chat", resolved_model: "gpt-chat", available: true },
    { alias: "review", resolved_model: "gpt-review", available: true },
    {
      alias: "review-cheap",
      resolved_model: "gpt-review-cheap",
      available: true,
    },
    {
      alias: "review-cheap-2",
      resolved_model: "gpt-review-cheap-2",
      available: true,
    },
  ],
  repositories: [
    {
      id: "gw-seastar",
      repository: "https://github.com/scylladb/seastar.git",
      label: "seastar",
    },
  ],
  profiles: [anotherProfile, defaultProfile],
  profile_count: 2,
  code_types: ["hotpath-code", "code", "test", "bench-test"],
  max_files_per_language: 20,
  default_files_per_language: 20,
  max_candidate_models: 8,
}

describe("ModelEvaluationEditorPage", () => {
  beforeAll(() => {
    Object.defineProperties(HTMLElement.prototype, {
      hasPointerCapture: {
        configurable: true,
        value: vi.fn(() => false),
      },
      setPointerCapture: { configurable: true, value: vi.fn() },
      releasePointerCapture: { configurable: true, value: vi.fn() },
      scrollIntoView: { configurable: true, value: vi.fn() },
    })
  })

  beforeEach(() => {
    vi.mocked(getModelEvaluationOptions).mockReset()
    vi.mocked(createModelEvaluation).mockReset()
    vi.mocked(getModelEvaluationOptions).mockResolvedValue(options)
    vi.mocked(createModelEvaluation).mockResolvedValue({
      id: "rme_seastar",
    } as RepositoryModelEvaluation)
  })

  it("includes the another profile reviewer when creating a seastar evaluation", async () => {
    const user = userEvent.setup()
    const onSaved = vi.fn()
    renderEditor(onSaved)

    expect(
      await screen.findByRole("combobox", { name: "Repository" }),
    ).toHaveTextContent("seastar")
    expect(
      screen.getByRole("combobox", { name: "Review profile" }),
    ).toHaveTextContent("another")
    expect(candidate("chat")).toBeChecked()
    expect(candidate("chat")).toBeDisabled()
    expect(candidate("review")).toBeChecked()

    await user.click(screen.getByRole("button", { name: "Save evaluation" }))

    await waitFor(() =>
      expect(createModelEvaluation).toHaveBeenCalledWith({
        repository: "https://github.com/scylladb/seastar.git",
        profile_id: anotherProfile.id,
        candidate_models: ["chat", "review"],
        ref: "HEAD",
      }),
    )
    expect(onSaved).toHaveBeenCalledWith("rme_seastar")
  })

  it("reconciles comparison models and locks the reviewer when the profile changes", async () => {
    const user = userEvent.setup()
    vi.mocked(getModelEvaluationOptions).mockResolvedValue({
      ...options,
      profiles: [defaultProfile, anotherProfile],
    })
    renderEditor()

    await screen.findByRole("combobox", { name: "Review profile" })
    expect(candidate("review")).toBeChecked()
    expect(candidate("review")).toBeDisabled()
    expect(candidate("chat")).toBeDisabled()
    await user.click(candidate("review-cheap-2"))

    await user.click(screen.getByRole("combobox", { name: "Review profile" }))
    await user.click(await screen.findByRole("option", { name: "another" }))

    expect(candidate("chat")).toBeChecked()
    expect(candidate("chat")).toBeDisabled()
    expect(candidate("review")).toBeChecked()
    expect(candidate("review-cheap")).toBeChecked()
    expect(candidate("review-cheap-2")).toBeChecked()

    await user.click(screen.getByRole("button", { name: "Save evaluation" }))

    await waitFor(() =>
      expect(createModelEvaluation).toHaveBeenCalledWith({
        repository: "https://github.com/scylladb/seastar.git",
        profile_id: anotherProfile.id,
        candidate_models: ["chat", "review", "review-cheap", "review-cheap-2"],
        ref: "HEAD",
      }),
    )
  })
})

function candidate(alias: string) {
  return screen.getByRole("checkbox", {
    name: `Select candidate model ${alias}`,
  })
}

function renderEditor(onSaved = vi.fn()) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <ModelEvaluationEditorPage onBack={vi.fn()} onSaved={onSaved} />
    </QueryClientProvider>,
  )
}
