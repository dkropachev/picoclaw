import {
  IconAlertTriangle,
  IconBrain,
  IconLoader2,
  IconPlayerPlay,
  IconRefresh,
} from "@tabler/icons-react"
import { useCallback, useEffect, useMemo, useState } from "react"

import {
  type EvaluationCodeType,
  type EvaluationConfigInput,
  type EvaluationCorpusPage,
  type EvaluationModelOption,
  type EvaluationRepositoryOption,
  ModelEvaluationAPIError,
  type RepositoryModelEvaluation,
  type RepositoryModelEvaluationSummary,
  getModelEvaluation,
  getModelEvaluationCorpus,
  getModelEvaluationOptions,
  listModelEvaluations,
  runModelEvaluation,
  runModelEvaluationAction,
  updateModelEvaluation,
} from "@/api/model-evaluations"
import { PageHeader } from "@/components/page-header"
import { ReviewAdvancedSection } from "@/components/repository-reviews/review-advanced-section"
import { Field } from "@/components/shared-form"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"

/* eslint-disable jsx-a11y/no-noninteractive-tabindex -- Horizontally scrollable data regions must be keyboard-focusable. */

const codeTypes: Array<{ value: EvaluationCodeType; label: string }> = [
  { value: "hotpath-code", label: "Hot-path code" },
  { value: "code", label: "Production code" },
  { value: "test", label: "Tests" },
  { value: "bench-test", label: "Benchmarks" },
]

const activeStatuses = new Set([
  "preflighting",
  "ready",
  "running",
  "judging",
  "analyzing",
  "canceling",
])
const corpusPageSize = 20

function lines(value: string): string[] {
  return [
    ...new Set(
      value
        .split(/[\n,]/)
        .map((item) => item.trim())
        .filter(Boolean),
    ),
  ]
}

function formatBytes(value: number): string {
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`
  return `${(value / (1024 * 1024)).toFixed(1)} MiB`
}

function formatDuration(value: number): string {
  if (value < 1000) return `${value} ms`
  return `${(value / 1000).toFixed(1)} s`
}

function statusTone(status: string): string {
  if (status === "completed") return "text-emerald-700 dark:text-emerald-400"
  if (status === "failed" || status === "canceled") return "text-destructive"
  if (activeStatuses.has(status)) return "text-sky-700 dark:text-sky-400"
  return "text-muted-foreground"
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError"
}

function clampLanguageLimit(
  raw: string,
  maximum: number,
  fallback: number,
): number {
  const parsed = Number(raw)
  if (!Number.isFinite(parsed)) return fallback
  return Math.min(maximum, Math.max(1, Math.trunc(parsed)))
}

function draftsEqual(left: EvaluationDraft, right: EvaluationDraft): boolean {
  return JSON.stringify(draftInput(left)) === JSON.stringify(draftInput(right))
}

interface EvaluationDraft {
  repository: string
  ref: string
  candidateModels: string[]
  selector: string
  judge: string
  codeTypes: EvaluationCodeType[]
  includeFolders: string
  excludeFolders: string
  freeText: string
  defaultFiles: number
  languageFiles: Record<string, number>
}

function emptyDraft(
  defaultFiles = 20,
  availableCodeTypes: EvaluationCodeType[] = codeTypes.map(
    (item) => item.value,
  ),
): EvaluationDraft {
  return {
    repository: "",
    ref: "",
    candidateModels: [],
    selector: "",
    judge: "",
    codeTypes: [...availableCodeTypes],
    includeFolders: "",
    excludeFolders: "",
    freeText: "",
    defaultFiles,
    languageFiles: {},
  }
}

function evaluationDraft(value: RepositoryModelEvaluation): EvaluationDraft {
  return {
    repository: value.repository,
    ref: value.ref,
    candidateModels: [...value.candidate_models],
    selector: value.selector_model_alias,
    judge: value.judge_model_alias,
    codeTypes: value.focus.code_types ?? [],
    includeFolders: (value.focus.include_folders ?? []).join("\n"),
    excludeFolders: (value.focus.exclude_folders ?? []).join("\n"),
    freeText: value.focus.free_text ?? "",
    defaultFiles: value.default_files_per_language,
    languageFiles: { ...value.files_per_language },
  }
}

function draftInput(
  draft: EvaluationDraft,
  expectedVersion?: number,
): EvaluationConfigInput {
  return {
    repository: draft.repository.trim(),
    ref: draft.ref.trim(),
    candidate_models: draft.candidateModels,
    selector_model_alias: draft.selector,
    judge_model_alias: draft.judge,
    focus: {
      code_types: draft.codeTypes,
      include_folders: lines(draft.includeFolders),
      exclude_folders: lines(draft.excludeFolders),
      free_text: draft.freeText.trim(),
    },
    default_files_per_language: draft.defaultFiles,
    files_per_language: draft.languageFiles,
    ...(expectedVersion == null ? {} : { expected_version: expectedVersion }),
  }
}

function modelProbeCandidates(
  draft: EvaluationDraft,
  candidateModels: string[],
  models: EvaluationModelOption[],
): EvaluationDraft {
  const available = models.filter((model) => model.available)
  const selector =
    draft.selector ||
    candidateModels.find((alias) =>
      available.some((model) => model.alias === alias),
    ) ||
    available.find((model) => model.default)?.alias ||
    available[0]?.alias ||
    ""
  const independentJudge = available.find(
    (model) => !candidateModels.includes(model.alias),
  )?.alias
  const judge =
    (!draft.judge || (candidateModels.includes(draft.judge) && independentJudge)
      ? independentJudge
      : draft.judge) ||
    candidateModels.find((alias) =>
      available.some((model) => model.alias === alias),
    ) ||
    ""
  return { ...draft, candidateModels, selector, judge }
}

function ModelChecks({
  options,
  selected,
  disabled,
  maximum,
  onChange,
}: {
  options: EvaluationModelOption[]
  selected: string[]
  disabled: boolean
  maximum: number
  onChange: (models: string[]) => void
}) {
  return (
    <div className="grid gap-2 sm:grid-cols-2">
      {options.map((option) => {
        const checked = selected.includes(option.alias)
        return (
          <label
            key={option.alias}
            className="border-border flex cursor-pointer gap-2 rounded-lg border p-3 text-sm"
          >
            <span className="sr-only">Select candidate model</span>
            <input
              aria-label={`Select candidate model ${option.alias}`}
              type="checkbox"
              checked={checked}
              disabled={
                disabled ||
                (!option.available && !checked) ||
                (!checked && selected.length >= maximum)
              }
              onChange={() =>
                onChange(
                  checked
                    ? selected.filter((item) => item !== option.alias)
                    : selected.length >= maximum
                      ? selected
                      : [...selected, option.alias],
                )
              }
            />
            <span className="min-w-0">
              <span className="font-mono">{option.alias}</span>
              <span className="text-muted-foreground block truncate text-xs">
                {option.available
                  ? option.resolved_model
                  : option.blocked_reason || "Unavailable"}
              </span>
            </span>
          </label>
        )
      })}
    </div>
  )
}

export function ModelEvaluationsPage() {
  const [evaluations, setEvaluations] = useState<
    RepositoryModelEvaluationSummary[]
  >([])
  const [selectedID, setSelectedID] = useState("")
  const [creatingNew, setCreatingNew] = useState(false)
  const [selected, setSelected] = useState<RepositoryModelEvaluation | null>(
    null,
  )
  const [models, setModels] = useState<EvaluationModelOption[]>([])
  const [repositories, setRepositories] = useState<
    EvaluationRepositoryOption[]
  >([])
  const [defaultFilesPerLanguage, setDefaultFilesPerLanguage] = useState(20)
  const [maxFilesPerLanguage, setMaxFilesPerLanguage] = useState(20)
  const [maxCandidateModels, setMaxCandidateModels] = useState(8)
  const [draft, setDraft] = useState<EvaluationDraft>(emptyDraft)
  const [busy, setBusy] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")
  const [conflict, setConflict] = useState(false)
  const [corpusPage, setCorpusPage] = useState<EvaluationCorpusPage | null>(
    null,
  )
  const [corpusOffset, setCorpusOffset] = useState(0)

  const load = useCallback(
    async (signal?: AbortSignal, newEditor = creatingNew) => {
      try {
        const [items, options] = await Promise.all([
          listModelEvaluations(signal),
          getModelEvaluationOptions(signal),
        ])
        setEvaluations(items)
        setModels(options.models)
        setRepositories(options.repositories)
        setDefaultFilesPerLanguage(options.default_files_per_language)
        setMaxFilesPerLanguage(options.max_files_per_language)
        setMaxCandidateModels(options.max_candidate_models)
        setSelectedID((current) => {
          if (newEditor) return ""
          if (current && items.some((item) => item.id === current)) {
            return current
          }
          return items[0]?.id ?? ""
        })
        if (newEditor || !items[0]) {
          setDraft((current) =>
            current.repository || current.candidateModels.length > 0
              ? current
              : emptyDraft(
                  options.default_files_per_language,
                  options.code_types,
                ),
          )
        }
        setError("")
      } catch (loadError) {
        if (isAbortError(loadError)) return
        setError(
          loadError instanceof Error
            ? loadError.message
            : "Failed to load model probes",
        )
      } finally {
        setLoading(false)
      }
    },
    [creatingNew],
  )

  const loadSelected = useCallback(
    async (signal?: AbortSignal) => {
      if (!selectedID) {
        setSelected(null)
        return
      }
      try {
        const evaluation = await getModelEvaluation(selectedID, signal)
        setSelected(evaluation)
        setDraft(evaluationDraft(evaluation))
        setError("")
        setConflict(false)
        setEvaluations((current) =>
          current.map((item) =>
            item.id === evaluation.id
              ? {
                  ...item,
                  version: evaluation.version,
                  status: evaluation.status,
                  progress: evaluation.progress,
                  usage: evaluation.usage,
                  warnings: evaluation.warnings,
                  failure: evaluation.failure,
                  updated_at: evaluation.updated_at,
                  finished_at: evaluation.finished_at,
                }
              : item,
          ),
        )
        return evaluation
      } catch (loadError) {
        if (isAbortError(loadError)) return
        setError(
          loadError instanceof Error
            ? loadError.message
            : "Failed to load model probe",
        )
      }
    },
    [selectedID],
  )
  const selectedStatus = selected?.status
  const selectedFileCount = selected?.progress.selected_files ?? 0

  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    return () => controller.abort()
  }, [load])
  useEffect(() => {
    const controller = new AbortController()
    void loadSelected(controller.signal)
    return () => controller.abort()
  }, [loadSelected])

  useEffect(() => {
    if (!selectedID || selectedFileCount === 0) {
      setCorpusPage(null)
      setCorpusOffset(0)
      return
    }
    const controller = new AbortController()
    void getModelEvaluationCorpus(
      selectedID,
      corpusOffset,
      corpusPageSize,
      controller.signal,
    )
      .then(setCorpusPage)
      .catch((corpusError: unknown) => {
        if (!isAbortError(corpusError)) {
          setError(
            corpusError instanceof Error
              ? corpusError.message
              : "Failed to load corpus identity",
          )
        }
      })
    return () => controller.abort()
  }, [corpusOffset, selectedFileCount, selectedID, selectedStatus])

  useEffect(() => {
    if (!selectedStatus || !activeStatuses.has(selectedStatus)) return
    let stopped = false
    let timer = 0
    let controller: AbortController | undefined
    const poll = async () => {
      controller = new AbortController()
      await loadSelected(controller.signal)
      if (!stopped) timer = window.setTimeout(() => void poll(), 2000)
    }
    timer = window.setTimeout(() => void poll(), 2000)
    return () => {
      stopped = true
      window.clearTimeout(timer)
      controller?.abort()
    }
  }, [loadSelected, selectedStatus])

  const mutate = async (
    operation: () => Promise<RepositoryModelEvaluation | void>,
  ): Promise<RepositoryModelEvaluation | void> => {
    setBusy(true)
    setError("")
    setConflict(false)
    try {
      const result = await operation()
      if (result) {
        setCreatingNew(false)
        if (result.id !== selectedID) setCorpusOffset(0)
        setSelected(result)
        setSelectedID(result.id)
        setDraft(evaluationDraft(result))
      }
      await load(undefined, false)
      return result
    } catch (mutationError) {
      if (isAbortError(mutationError)) return
      setConflict(
        mutationError instanceof ModelEvaluationAPIError &&
          mutationError.status === 409,
      )
      setError(
        mutationError instanceof Error
          ? mutationError.message
          : "Model probe action failed",
      )
    } finally {
      setBusy(false)
    }
  }

  const validDraft =
    draft.repository.trim() !== "" &&
    draft.candidateModels.length >= 2 &&
    draft.selector !== "" &&
    draft.judge !== "" &&
    draft.codeTypes.length > 0 &&
    draft.candidateModels.length <= maxCandidateModels &&
    draft.defaultFiles >= 1 &&
    draft.defaultFiles <= maxFilesPerLanguage &&
    Object.values(draft.languageFiles).every(
      (value) => value >= 1 && value <= maxFilesPerLanguage,
    ) &&
    draft.candidateModels.every((alias) =>
      models.some((model) => model.alias === alias && model.available),
    ) &&
    models.some((model) => model.alias === draft.selector && model.available) &&
    models.some((model) => model.alias === draft.judge && model.available)

  const configurationEditable = selected == null || selected.status === "draft"
  const configurationControlsDisabled =
    !configurationEditable || busy || loading
  const draftDirty = selected
    ? !draftsEqual(draft, evaluationDraft(selected))
    : false
  const displayModels = useMemo(
    () => [
      ...models,
      ...[...new Set([...draft.candidateModels, draft.selector, draft.judge])]
        .filter(Boolean)
        .filter((alias) => !models.some((model) => model.alias === alias))
        .map((alias) => ({
          alias,
          resolved_model: "No longer configured",
          available: false,
          blocked_reason: "No longer present in the model catalog.",
        })),
    ],
    [draft.candidateModels, draft.judge, draft.selector, models],
  )
  const unavailableModelSelected =
    !validDraft &&
    (draft.candidateModels.some(
      (alias) =>
        !models.some((model) => model.alias === alias && model.available),
    ) ||
      Boolean(
        draft.selector &&
        !models.some(
          (model) => model.alias === draft.selector && model.available,
        ),
      ) ||
      Boolean(
        draft.judge &&
        !models.some((model) => model.alias === draft.judge && model.available),
      ))

  const languages = useMemo(
    () =>
      Object.entries(selected?.progress.languages ?? {}).sort(([a], [b]) =>
        a.localeCompare(b),
      ),
    [selected],
  )

  const run = async () => {
    if (!validDraft || !configurationEditable) return
    await mutate(async () => {
      if (selected == null) return runModelEvaluation(draftInput(draft))
      const configured = draftDirty
        ? await updateModelEvaluation(
            selected.id,
            draftInput(draft, selected.version),
          )
        : selected
      return runModelEvaluationAction(configured.id, "run", configured.version)
    })
  }

  const action = async (
    name: "preflight" | "start" | "run" | "cancel" | "resume" | "restart",
  ) => {
    if (!selected) return
    await mutate(() =>
      runModelEvaluationAction(selected.id, name, selected.version),
    )
  }

  return (
    <div className="flex h-full flex-col">
      <PageHeader title="Model review probes">
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={busy || loading}
          onClick={() => {
            setSelectedID("")
            setSelected(null)
            setCreatingNew(true)
            setCorpusOffset(0)
            setDraft(emptyDraft(defaultFilesPerLanguage))
          }}
        >
          New probe
        </Button>
      </PageHeader>
      <div className="grid min-h-0 flex-1 lg:grid-cols-[18rem_minmax(0,1fr)]">
        <aside
          aria-label="Saved model probes"
          className="border-border max-h-48 min-h-0 overflow-y-auto border-b p-3 lg:max-h-none lg:border-r lg:border-b-0"
        >
          {loading ? (
            <div
              role="status"
              aria-label="Loading model probes"
              className="flex justify-center pt-8"
            >
              <IconLoader2 aria-hidden="true" className="size-5 animate-spin" />
            </div>
          ) : evaluations.length === 0 ? (
            <p className="text-muted-foreground p-3 text-sm">
              No model probes yet.
            </p>
          ) : (
            evaluations.map((evaluation) => (
              <button
                key={evaluation.id}
                type="button"
                disabled={busy}
                aria-pressed={selectedID === evaluation.id}
                className={`mb-2 w-full rounded-lg border p-3 text-left ${
                  selectedID === evaluation.id
                    ? "border-primary bg-muted"
                    : "border-border"
                }`}
                onClick={() => {
                  setCreatingNew(false)
                  setCorpusOffset(0)
                  setSelectedID(evaluation.id)
                }}
              >
                <span className="block truncate text-sm font-medium">
                  {evaluation.repository}
                </span>
                <span className={`text-xs ${statusTone(evaluation.status)}`}>
                  {evaluation.status}
                </span>
              </button>
            ))
          )}
        </aside>

        <section
          aria-label="Model probe workspace"
          className="min-h-0 overflow-y-auto p-4 sm:p-6"
        >
          {error && (
            <div
              role="alert"
              className="bg-destructive/10 text-destructive mb-4 flex flex-wrap items-center justify-between gap-2 rounded-lg p-3 text-sm"
            >
              <span>{error}</span>
              {conflict && selectedID && (
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  disabled={busy}
                  onClick={() => void loadSelected()}
                >
                  Reload latest
                </Button>
              )}
            </div>
          )}
          <div className="mx-auto max-w-6xl space-y-6">
            <div className="border-border bg-muted/40 rounded-lg border p-3 text-sm">
              Comparison-only flow. Probes assess model quality, reliability,
              usage, and efficiency on the same corpus. They do not create
              repository findings or issue drafts.
            </div>
            <section className="border-border bg-card rounded-xl border p-4 sm:p-5">
              <div className="mb-4 flex flex-wrap items-center justify-between gap-2">
                <div>
                  <h2 className="font-semibold">Probe repository</h2>
                  <p className="text-muted-foreground text-xs">
                    Blank advanced revision uses the repository default branch.
                  </p>
                </div>
                {selected && (
                  <Badge variant="secondary">{selected.status}</Badge>
                )}
              </div>
              <div>
                <Field label="Repository" required>
                  <Input
                    aria-label="Repository"
                    list="model-probe-workspace-repositories"
                    value={draft.repository}
                    onChange={(event) =>
                      setDraft({ ...draft, repository: event.target.value })
                    }
                    placeholder="owner/repository or safe Git URL"
                    disabled={configurationControlsDisabled}
                  />
                  <datalist id="model-probe-workspace-repositories">
                    {repositories.map((repository) => (
                      <option key={repository.id} value={repository.repository}>
                        {repository.label}
                      </option>
                    ))}
                  </datalist>
                  <p className="text-muted-foreground text-xs">
                    Choose a registered Git Workspace repository or enter
                    owner/repository. The probe acquires a managed fresh
                    checkout and releases it before model calls.
                  </p>
                </Field>
              </div>
            </section>

            <section className="border-border bg-card rounded-xl border p-4 sm:p-5">
              <h2 className="font-semibold">Models</h2>
              <p className="text-muted-foreground mb-4 text-xs">
                Every candidate receives identical immutable chunks. Judge
                scores are labeled AI judged.
              </p>
              <Field label="Candidate models" required>
                <ModelChecks
                  options={displayModels}
                  selected={draft.candidateModels}
                  disabled={configurationControlsDisabled}
                  maximum={maxCandidateModels}
                  onChange={(candidateModels) =>
                    setDraft(
                      modelProbeCandidates(
                        draft,
                        candidateModels,
                        displayModels,
                      ),
                    )
                  }
                />
              </Field>
              <div className="mt-4">
                <ReviewAdvancedSection description="revision, scope, quotas, selector, and judge">
                  <section className="space-y-4">
                    <h3 className="text-sm font-semibold">Repository scope</h3>
                    <Field
                      label="Revision"
                      hint="Optional branch, tag, or commit. Blank uses the repository default branch."
                    >
                      <Input
                        aria-label="Revision"
                        value={draft.ref}
                        onChange={(event) =>
                          setDraft({ ...draft, ref: event.target.value })
                        }
                        placeholder="Default repository branch"
                        disabled={configurationControlsDisabled}
                      />
                    </Field>
                    <div>
                      <p className="mb-2 text-sm font-medium">Code types</p>
                      <div className="flex flex-wrap gap-2">
                        {codeTypes.map((item) => {
                          const checked = draft.codeTypes.includes(item.value)
                          return (
                            <label
                              key={item.value}
                              className="border-border flex gap-2 rounded-lg border px-3 py-2 text-sm"
                            >
                              <input
                                aria-label={item.label}
                                type="checkbox"
                                checked={checked}
                                disabled={configurationControlsDisabled}
                                onChange={() =>
                                  setDraft({
                                    ...draft,
                                    codeTypes: checked
                                      ? draft.codeTypes.filter(
                                          (value) => value !== item.value,
                                        )
                                      : [...draft.codeTypes, item.value],
                                  })
                                }
                              />
                              {item.label}
                            </label>
                          )
                        })}
                      </div>
                    </div>
                    <div className="grid gap-4 md:grid-cols-2">
                      <Field
                        label="Include folders"
                        hint="One exact repository-relative prefix per line."
                      >
                        <Textarea
                          aria-label="Include folders"
                          value={draft.includeFolders}
                          onChange={(event) =>
                            setDraft({
                              ...draft,
                              includeFolders: event.target.value,
                            })
                          }
                          placeholder="pkg/core\nweb/frontend"
                          disabled={configurationControlsDisabled}
                        />
                      </Field>
                      <Field
                        label="Ignore folders"
                        hint="Exclusions always win."
                      >
                        <Textarea
                          aria-label="Ignore folders"
                          value={draft.excludeFolders}
                          onChange={(event) =>
                            setDraft({
                              ...draft,
                              excludeFolders: event.target.value,
                            })
                          }
                          placeholder="examples\ntestdata"
                          disabled={configurationControlsDisabled}
                        />
                      </Field>
                    </div>
                    <div className="grid gap-4 md:grid-cols-[1fr_12rem]">
                      <Field
                        label="Free-text scope"
                        hint="AI may narrow, never widen, the structured scope."
                      >
                        <Textarea
                          aria-label="Free-text scope"
                          value={draft.freeText}
                          onChange={(event) =>
                            setDraft({ ...draft, freeText: event.target.value })
                          }
                          placeholder="Focus on request routing and persistence boundaries."
                          disabled={configurationControlsDisabled}
                        />
                      </Field>
                      <Field
                        label="Files per language"
                        hint={`Default quota; hard maximum: ${maxFilesPerLanguage}.`}
                      >
                        <Input
                          aria-label="Files per language"
                          type="number"
                          min={1}
                          max={maxFilesPerLanguage}
                          value={draft.defaultFiles}
                          disabled={configurationControlsDisabled}
                          onChange={(event) =>
                            setDraft({
                              ...draft,
                              defaultFiles: clampLanguageLimit(
                                event.target.value,
                                maxFilesPerLanguage,
                                draft.defaultFiles,
                              ),
                            })
                          }
                        />
                      </Field>
                    </div>
                  </section>
                  <section className="space-y-4 border-t pt-4">
                    <h3 className="text-sm font-semibold">Model roles</h3>
                    <div className="grid gap-4 sm:grid-cols-2">
                      <Field label="File selector model" required>
                        <select
                          aria-label="File selector model"
                          className="border-input bg-background h-9 w-full rounded-md border px-3 text-sm"
                          value={draft.selector}
                          disabled={configurationControlsDisabled}
                          onChange={(event) =>
                            setDraft({ ...draft, selector: event.target.value })
                          }
                        >
                          <option value="">Select model</option>
                          {displayModels.map((model) => (
                            <option
                              key={model.alias}
                              value={model.alias}
                              disabled={!model.available}
                            >
                              {model.alias}
                            </option>
                          ))}
                        </select>
                      </Field>
                      <Field label="Judge and analyzer model" required>
                        <select
                          aria-label="Judge and analyzer model"
                          className="border-input bg-background h-9 w-full rounded-md border px-3 text-sm"
                          value={draft.judge}
                          disabled={configurationControlsDisabled}
                          onChange={(event) =>
                            setDraft({ ...draft, judge: event.target.value })
                          }
                        >
                          <option value="">Select model</option>
                          {displayModels.map((model) => (
                            <option
                              key={model.alias}
                              value={model.alias}
                              disabled={!model.available}
                            >
                              {model.alias}
                            </option>
                          ))}
                        </select>
                      </Field>
                    </div>
                    {draft.candidateModels.includes(draft.judge) &&
                      draft.judge && (
                        <p
                          role="status"
                          className="mt-3 flex items-center gap-2 text-sm text-amber-600"
                        >
                          <IconAlertTriangle className="size-4" /> Judge
                          overlaps a candidate; results will show a bias
                          warning.
                        </p>
                      )}
                  </section>
                </ReviewAdvancedSection>
              </div>
              {draft.candidateModels.length >= maxCandidateModels && (
                <p className="text-muted-foreground mt-3 text-xs">
                  Candidate-model limit reached ({maxCandidateModels}).
                </p>
              )}
              {unavailableModelSelected && (
                <p role="alert" className="text-destructive mt-3 text-sm">
                  Replace unavailable selector/judge models and remove any
                  unavailable candidate models before running.
                </p>
              )}
              <div className="mt-5 flex flex-wrap gap-2">
                {configurationEditable && (
                  <Button
                    type="button"
                    disabled={busy || loading || !validDraft}
                    onClick={() => void run()}
                  >
                    {busy ? (
                      <IconLoader2 className="size-4 animate-spin" />
                    ) : (
                      <IconPlayerPlay className="size-4" />
                    )}
                    Run probe
                  </Button>
                )}
                {selected &&
                  activeStatuses.has(selected.status) &&
                  selected.status !== "canceling" && (
                    <Button
                      type="button"
                      variant="destructive"
                      disabled={busy}
                      onClick={() => void action("cancel")}
                    >
                      Cancel
                    </Button>
                  )}
                {selected?.status === "failed" && (
                  <Button
                    type="button"
                    variant="outline"
                    disabled={busy}
                    onClick={() => void action("resume")}
                  >
                    <IconRefresh className="size-4" /> Restart
                  </Button>
                )}
                {selected?.status === "failed" && (
                  <Button
                    type="button"
                    variant="outline"
                    disabled={busy}
                    onClick={() => void action("restart")}
                  >
                    <IconRefresh className="size-4" /> Start over
                  </Button>
                )}
              </div>
            </section>

            {selected && (
              <section
                className="border-border bg-card rounded-xl border p-4 sm:p-5"
                aria-live="polite"
                aria-atomic="false"
              >
                <div className="flex items-center justify-between gap-3">
                  <div>
                    <h2 className="font-semibold">Progress</h2>
                    <p className="text-muted-foreground text-xs">
                      {selected.progress.message || selected.progress.stage}
                    </p>
                  </div>
                  <span className="font-mono text-sm">
                    {selected.progress.percent.toFixed(0)}%
                  </span>
                </div>
                <div
                  className="bg-muted mt-3 h-2 overflow-hidden rounded-full"
                  role="progressbar"
                  aria-label="Model probe progress"
                  aria-valuemin={0}
                  aria-valuemax={100}
                  aria-valuenow={Math.min(
                    100,
                    Math.max(0, selected.progress.percent),
                  )}
                >
                  {/* ui-rule-allow dynamic-style: width reflects durable evaluation progress. */}
                  <div
                    className="bg-primary h-full"
                    style={{
                      width: `${Math.min(100, Math.max(0, selected.progress.percent))}%`,
                    }}
                  />
                </div>
                <div className="mt-3 grid gap-2 text-xs sm:grid-cols-4">
                  <span>{selected.progress.selected_files} files selected</span>
                  <span>
                    {selected.progress.completed_files} files analyzed
                  </span>
                  <span>
                    {selected.progress.completed_tasks}/
                    {selected.progress.total_tasks} tasks
                  </span>
                  <span>
                    {selected.usage.input_tokens + selected.usage.output_tokens}{" "}
                    tokens
                  </span>
                </div>
                {(selected.progress.current_model ||
                  selected.progress.current_path) && (
                  <p className="text-muted-foreground mt-3 text-xs">
                    {selected.progress.current_model
                      ? `Model ${selected.progress.current_model}`
                      : ""}
                    {selected.progress.current_model &&
                    selected.progress.current_path
                      ? " · "
                      : ""}
                    {selected.progress.current_path
                      ? `File ${selected.progress.current_path}`
                      : ""}
                  </p>
                )}
                {selected.failure && (
                  <p className="text-destructive mt-3 text-sm">
                    {selected.failure}
                  </p>
                )}
                {selected.warnings.map((warning) => (
                  <p key={warning} className="mt-2 text-sm text-amber-600">
                    {warning}
                  </p>
                ))}
                {selected.run_ids.length > 0 && (
                  <details className="border-border mt-4 rounded-lg border p-3 text-xs">
                    <summary className="cursor-pointer font-medium">
                      Run history ({selected.run_ids.length})
                    </summary>
                    <ul className="text-muted-foreground mt-2 space-y-1">
                      {selected.run_ids
                        .slice()
                        .reverse()
                        .map((runID) => (
                          <li key={runID}>
                            <a
                              className="text-primary underline underline-offset-2"
                              href={`/agent/workflows?mode=operate&run=${encodeURIComponent(runID)}`}
                            >
                              {runID}
                            </a>
                          </li>
                        ))}
                    </ul>
                  </details>
                )}
              </section>
            )}

            {selected && languages.length > 0 && (
              <section
                aria-label="Corpus by language"
                tabIndex={0}
                className="border-border bg-card overflow-x-auto rounded-xl border p-4 sm:p-5"
              >
                <h2 className="font-semibold">Corpus by language</h2>
                {selected.corpus?.selection_rationale && (
                  <p className="text-muted-foreground mt-1 text-xs">
                    {selected.corpus.selection_rationale}
                  </p>
                )}
                <table className="mt-4 w-full min-w-[42rem] text-left text-sm">
                  <caption className="sr-only">
                    Selected model probe corpus grouped by language
                  </caption>
                  <thead>
                    <tr className="border-border border-b">
                      <th scope="col" className="py-2">
                        Language
                      </th>
                      <th scope="col">Available</th>
                      <th scope="col">Selected</th>
                      <th scope="col">Bytes</th>
                      <th scope="col">Regions</th>
                      <th scope="col">Per-language limit</th>
                    </tr>
                  </thead>
                  <tbody>
                    {languages.map(([language, progress]) => (
                      <tr
                        key={language}
                        className="border-border border-b last:border-0"
                      >
                        <th
                          scope="row"
                          className="py-2 text-left font-mono font-normal"
                        >
                          {language}
                          {progress.limited && (
                            <Badge className="ml-2" variant="outline">
                              limited
                            </Badge>
                          )}
                        </th>
                        <td>{progress.available_files}</td>
                        <td>{progress.selected_files}</td>
                        <td>{formatBytes(progress.selected_bytes)}</td>
                        <td>{progress.regions.length}</td>
                        <td>
                          <div className="flex min-w-40 items-center gap-2">
                            <Input
                              aria-label={`${language} files`}
                              className="w-20"
                              type="number"
                              min={1}
                              max={maxFilesPerLanguage}
                              value={
                                draft.languageFiles[language] ??
                                draft.defaultFiles
                              }
                              disabled={configurationControlsDisabled}
                              onChange={(event) =>
                                setDraft({
                                  ...draft,
                                  languageFiles: {
                                    ...draft.languageFiles,
                                    [language]: clampLanguageLimit(
                                      event.target.value,
                                      maxFilesPerLanguage,
                                      draft.languageFiles[language] ??
                                        draft.defaultFiles,
                                    ),
                                  },
                                })
                              }
                            />
                            {Object.hasOwn(draft.languageFiles, language) ? (
                              <Button
                                type="button"
                                size="sm"
                                variant="ghost"
                                disabled={configurationControlsDisabled}
                                aria-label={`Use default quota for ${language}`}
                                onClick={() => {
                                  const languageFiles = {
                                    ...draft.languageFiles,
                                  }
                                  delete languageFiles[language]
                                  setDraft({ ...draft, languageFiles })
                                }}
                              >
                                Default
                              </Button>
                            ) : (
                              <span className="text-muted-foreground text-xs">
                                default
                              </span>
                            )}
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                {(selected.corpus || corpusPage?.commit_sha) && (
                  <p className="text-muted-foreground mt-3 font-mono text-xs">
                    Commit{" "}
                    {selected.corpus?.commit_sha ?? corpusPage?.commit_sha} ·
                    inventory{" "}
                    {selected.corpus?.inventory_hash ??
                      corpusPage?.inventory_hash}
                    {corpusPage ? ` · ${corpusPage.total} files` : ""}
                  </p>
                )}
              </section>
            )}

            {selected && corpusPage && corpusPage.total > 0 && (
              <section
                aria-label="Corpus preview"
                tabIndex={0}
                className="border-border bg-card overflow-x-auto rounded-xl border p-4 sm:p-5"
              >
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div>
                    <h2 className="font-semibold">Corpus preview</h2>
                    <p className="text-muted-foreground text-xs">
                      Safe immutable references only; source content and local
                      checkout paths are never returned.
                    </p>
                  </div>
                  <span className="text-muted-foreground text-xs">
                    Showing {corpusOffset + 1}–
                    {Math.min(
                      corpusOffset + corpusPage.files.length,
                      corpusPage.total,
                    )}{" "}
                    of {corpusPage.total}
                  </span>
                </div>
                <table className="mt-4 w-full min-w-[48rem] text-left text-sm">
                  <caption className="sr-only">
                    Paged immutable model probe corpus references
                  </caption>
                  <thead>
                    <tr className="border-border border-b">
                      <th scope="col" className="py-2">
                        Path
                      </th>
                      <th scope="col">Language</th>
                      <th scope="col">Code type</th>
                      <th scope="col">Region</th>
                      <th scope="col">Size</th>
                    </tr>
                  </thead>
                  <tbody>
                    {corpusPage.files.map((file) => (
                      <tr
                        key={file.candidate_id}
                        className="border-border border-b last:border-0"
                      >
                        <th
                          scope="row"
                          className="py-2 text-left font-mono font-normal"
                        >
                          {file.path}
                        </th>
                        <td>{file.language}</td>
                        <td>{file.code_type}</td>
                        <td>{file.region}</td>
                        <td>{formatBytes(file.size_bytes)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                <div className="mt-3 flex justify-end gap-2">
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    disabled={busy || corpusOffset === 0}
                    onClick={() =>
                      setCorpusOffset(
                        Math.max(0, corpusOffset - corpusPageSize),
                      )
                    }
                  >
                    Previous corpus page
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    disabled={busy || corpusPage.next_offset == null}
                    onClick={() =>
                      setCorpusOffset(corpusPage.next_offset ?? corpusOffset)
                    }
                  >
                    Next corpus page
                  </Button>
                </div>
              </section>
            )}

            {selected && selected.comparisons.length > 0 && (
              <section
                aria-label="AI-judged comparison"
                tabIndex={0}
                className="border-border bg-card overflow-x-auto rounded-xl border p-4 sm:p-5"
              >
                <div className="flex items-center gap-2">
                  <IconBrain className="size-5" />
                  <h2 className="font-semibold">AI-judged comparison</h2>
                </div>
                <p className="text-muted-foreground mt-1 text-xs">
                  Quality scores are comparative AI judgments, not ground-truth
                  benchmark measurements.
                </p>
                <table className="mt-4 w-full min-w-[70rem] text-left text-sm">
                  <caption className="sr-only">
                    AI-judged model quality, coverage, reliability, usage, and
                    cost comparison
                  </caption>
                  <thead>
                    <tr className="border-border border-b">
                      <th scope="col" className="py-2">
                        Rank
                      </th>
                      <th scope="col">Model alias</th>
                      <th scope="col">Concrete models</th>
                      <th scope="col">Completion</th>
                      <th scope="col">Overall</th>
                      <th scope="col">Correctness</th>
                      <th scope="col">Evidence</th>
                      <th scope="col">Coverage</th>
                      <th scope="col">Actionability</th>
                      <th scope="col">Files / bytes / regions / languages</th>
                      <th scope="col">Findings / unsupported</th>
                      <th scope="col">Requests / latency</th>
                      <th scope="col">Tokens / cost</th>
                      <th scope="col">AI-judged verdict</th>
                    </tr>
                  </thead>
                  <tbody>
                    {[...selected.comparisons]
                      .sort((a, b) => (a.rank || 99) - (b.rank || 99))
                      .map((row) => (
                        <tr
                          key={row.model_alias}
                          className="border-border border-b align-top last:border-0"
                        >
                          <td className="py-3">{row.rank || "—"}</td>
                          <th
                            scope="row"
                            className="text-left font-mono font-normal"
                          >
                            {row.model_alias}
                          </th>
                          <td>
                            {Object.entries(row.concrete_models)
                              .map(([model, count]) => `${model} (${count})`)
                              .join(", ") || "unknown"}
                          </td>
                          <td>
                            <Badge
                              variant={
                                row.completion === "failed"
                                  ? "destructive"
                                  : "outline"
                              }
                            >
                              {row.completion}
                            </Badge>
                            {row.failures > 0 && (
                              <span className="text-destructive mt-1 block text-xs">
                                {row.failures} failed task
                                {row.failures === 1 ? "" : "s"}
                              </span>
                            )}
                          </td>
                          <td>{row.overall_score?.toFixed(1) ?? "—"}</td>
                          <td>{row.scores.correctness?.toFixed(1) ?? "—"}</td>
                          <td>{row.scores.evidence?.toFixed(1) ?? "—"}</td>
                          <td>{row.scores.coverage?.toFixed(1) ?? "—"}</td>
                          <td>{row.scores.actionability?.toFixed(1) ?? "—"}</td>
                          <td>
                            {row.files_analyzed} /{" "}
                            {formatBytes(row.bytes_analyzed)} /{" "}
                            {row.regions.length} / {row.languages.length}
                          </td>
                          <td>
                            {row.confirmed_findings} / {row.unsupported_files}
                          </td>
                          <td>
                            {row.usage.requests} /{" "}
                            {formatDuration(row.usage.duration_millis)}
                          </td>
                          <td>
                            {row.usage.input_tokens + row.usage.output_tokens} /{" "}
                            {row.usage.estimated_cost_usd == null
                              ? "unknown"
                              : `$${row.usage.estimated_cost_usd.toFixed(4)}`}
                          </td>
                          <td className="max-w-md">
                            <p>{row.verdict || row.summary || "No verdict."}</p>
                            {row.failure && (
                              <p className="text-destructive mt-1 text-xs">
                                Failure: {row.failure}
                              </p>
                            )}
                            {(row.strengths ?? []).length > 0 && (
                              <p className="mt-1 text-xs text-emerald-600">
                                Strengths: {row.strengths?.join("; ")}
                              </p>
                            )}
                            {(row.limitations ?? []).length > 0 && (
                              <p className="mt-1 text-xs text-amber-600">
                                Limitations: {row.limitations?.join("; ")}
                              </p>
                            )}
                          </td>
                        </tr>
                      ))}
                  </tbody>
                </table>
              </section>
            )}
          </div>
        </section>
      </div>
    </div>
  )
}
