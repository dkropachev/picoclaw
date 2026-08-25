import {
  IconAlertTriangle,
  IconExternalLink,
  IconLoader2,
  IconPlayerPlay,
  IconRefresh,
} from "@tabler/icons-react"
import { useCallback, useEffect, useMemo, useState } from "react"

import {
  type EvaluationConfigInput,
  type EvaluationCorpusPage,
  type EvaluationModelOption,
  type EvaluationProfileOption,
  type EvaluationProfileSnapshot,
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
import { Field } from "@/components/shared-form"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

import { ModelEvaluationReportContent } from "./model-evaluation-report-page"

/* eslint-disable jsx-a11y/no-noninteractive-tabindex -- Horizontally scrollable data regions must be keyboard-focusable. */

const activeStatuses = new Set([
  "preflighting",
  "ready",
  "running",
  "judging",
  "analyzing",
  "canceling",
])
const corpusPageSize = 20

type ProbeRunTab = "status" | "languages" | "corpus" | "report"

interface EvaluationDraft {
  repository: string
  ref: string
  profileID: string
  candidateModels: string[]
}

function formatBytes(value: number): string {
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`
  return `${(value / (1024 * 1024)).toFixed(1)} MiB`
}

function formatProgressTime(value?: string): string {
  if (!value) return "not reported"
  const timestamp = new Date(value)
  if (Number.isNaN(timestamp.getTime())) return "not reported"
  return timestamp.toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  })
}

function formatProgressElapsed(value?: string): string {
  if (!value) return "unknown"
  const started = new Date(value).getTime()
  if (!Number.isFinite(started)) return "unknown"
  const elapsedSeconds = Math.max(0, Math.floor((Date.now() - started) / 1000))
  if (elapsedSeconds < 60) return `${elapsedSeconds}s`
  return `${Math.floor(elapsedSeconds / 60)}m ${elapsedSeconds % 60}s`
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

function profileAvailableAliases(
  profile: EvaluationProfileOption | undefined,
  models: EvaluationModelOption[],
): string[] {
  if (!profile) return []
  const configured = new Set(
    models.filter((model) => model.available).map((model) => model.alias),
  )
  return profile.available_models.filter((alias) => configured.has(alias))
}

function selectProfileCandidates(
  current: string[],
  profile: EvaluationProfileOption | undefined,
  models: EvaluationModelOption[],
  maximum: number,
): string[] {
  const available = profileAvailableAliases(profile, models)
  const allowed = new Set(available)
  const selected = current.filter((alias) => allowed.has(alias))
  if (
    profile?.reviewer_model &&
    allowed.has(profile.reviewer_model) &&
    !selected.includes(profile.reviewer_model)
  ) {
    selected.unshift(profile.reviewer_model)
  }
  for (const alias of available) {
    if (selected.length >= 2) break
    if (!selected.includes(alias)) selected.push(alias)
  }
  return selected.slice(0, maximum)
}

function emptyDraft(
  profiles: EvaluationProfileOption[] = [],
  models: EvaluationModelOption[] = [],
  maximum = 8,
): EvaluationDraft {
  const profile = profiles[0]
  return {
    repository: "",
    ref: "",
    profileID: profile?.id ?? "",
    candidateModels: selectProfileCandidates([], profile, models, maximum),
  }
}

function evaluationDraft(value: RepositoryModelEvaluation): EvaluationDraft {
  return {
    repository: value.repository,
    ref: value.ref === "HEAD" ? "" : value.ref,
    profileID: value.profile?.id ?? "",
    candidateModels: [...value.candidate_models],
  }
}

function draftInput(
  draft: EvaluationDraft,
  expectedVersion?: number,
): EvaluationConfigInput {
  return {
    repository: draft.repository.trim(),
    profile_id: draft.profileID,
    candidate_models: draft.candidateModels,
    ref: draft.ref.trim(),
    ...(expectedVersion == null ? {} : { expected_version: expectedVersion }),
  }
}

function draftsEqual(left: EvaluationDraft, right: EvaluationDraft): boolean {
  return JSON.stringify(draftInput(left)) === JSON.stringify(draftInput(right))
}

function profileAsOption(
  profile: EvaluationProfileSnapshot,
  candidateModels: string[],
): EvaluationProfileOption {
  return { ...profile, available_models: [...candidateModels] }
}

function ModelChecks({
  options,
  selected,
  disabled,
  maximum,
  requiredAlias,
  onChange,
}: {
  options: EvaluationModelOption[]
  selected: string[]
  disabled: boolean
  maximum: number
  requiredAlias?: string
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
                (checked && option.alias === requiredAlias) ||
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
              {option.alias === requiredAlias && (
                <span className="text-muted-foreground ml-2 text-xs">
                  required by profile
                </span>
              )}
              <span className="text-muted-foreground block truncate text-xs">
                {option.available
                  ? option.resolved_model
                  : option.blocked_reason || "Unavailable for this profile"}
              </span>
            </span>
          </label>
        )
      })}
    </div>
  )
}

function ProbeTabs({
  evaluation,
  active,
  onChange,
}: {
  evaluation: RepositoryModelEvaluation
  active: ProbeRunTab
  onChange: (tab: ProbeRunTab) => void
}) {
  const availability: Record<ProbeRunTab, boolean> = {
    status: true,
    languages: Object.keys(evaluation.progress.languages).length > 0,
    corpus: evaluation.progress.selected_files > 0,
    report:
      evaluation.status === "completed" &&
      (evaluation.comparisons.length > 0 ||
        (evaluation.work_sizing_results?.length ?? 0) > 0),
  }
  const tabs: Array<{ id: ProbeRunTab; label: string }> = [
    { id: "status", label: "Status" },
    { id: "languages", label: "Corpus by language" },
    { id: "corpus", label: "Corpus preview" },
    { id: "report", label: "Final report" },
  ]
  const focusTab = (tab: ProbeRunTab) => {
    onChange(tab)
    document.getElementById(`probe-tab-${evaluation.id}-${tab}`)?.focus()
  }
  return (
    <div
      role="tablist"
      aria-label="Probe run details"
      className="border-border bg-muted/40 flex gap-1 overflow-x-auto rounded-xl border p-1"
    >
      {tabs.map((tab) => (
        <button
          key={tab.id}
          id={`probe-tab-${evaluation.id}-${tab.id}`}
          type="button"
          role="tab"
          aria-selected={active === tab.id}
          aria-controls={
            active === tab.id
              ? `probe-panel-${evaluation.id}-${tab.id}`
              : undefined
          }
          tabIndex={active === tab.id ? 0 : -1}
          disabled={!availability[tab.id]}
          className="aria-selected:bg-background aria-selected:text-foreground text-muted-foreground shrink-0 rounded-lg px-3 py-2 text-sm font-medium disabled:cursor-not-allowed disabled:opacity-45 aria-selected:shadow-sm"
          onClick={() => onChange(tab.id)}
          onKeyDown={(event) => {
            const enabled = tabs.filter(
              (candidate) => availability[candidate.id],
            )
            const index = enabled.findIndex(
              (candidate) => candidate.id === tab.id,
            )
            let next: ProbeRunTab | undefined
            if (event.key === "ArrowRight") {
              next = enabled[(index + 1) % enabled.length]?.id
            } else if (event.key === "ArrowLeft") {
              next = enabled[(index - 1 + enabled.length) % enabled.length]?.id
            } else if (event.key === "Home") {
              next = enabled[0]?.id
            } else if (event.key === "End") {
              next = enabled.at(-1)?.id
            }
            if (next) {
              event.preventDefault()
              focusTab(next)
            }
          }}
        >
          {tab.label}
        </button>
      ))}
    </div>
  )
}

export function ModelEvaluationsPage({
  onOpenReport,
  initialEvaluationID = "",
}: {
  onOpenReport?: (evaluationID: string) => void
  initialEvaluationID?: string
} = {}) {
  const [evaluations, setEvaluations] = useState<
    RepositoryModelEvaluationSummary[]
  >([])
  const [selectedID, setSelectedID] = useState(initialEvaluationID)
  const [creatingNew, setCreatingNew] = useState(false)
  const [selected, setSelected] = useState<RepositoryModelEvaluation | null>(
    null,
  )
  const [models, setModels] = useState<EvaluationModelOption[]>([])
  const [repositories, setRepositories] = useState<
    EvaluationRepositoryOption[]
  >([])
  const [profiles, setProfiles] = useState<EvaluationProfileOption[]>([])
  const [profileCount, setProfileCount] = useState(0)
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
  const [activeRunTab, setActiveRunTab] = useState<ProbeRunTab>("status")

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
        const availableProfiles = options.profiles ?? []
        setProfiles(availableProfiles)
        setProfileCount(options.profile_count ?? availableProfiles.length)
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
                  availableProfiles,
                  options.models,
                  options.max_candidate_models,
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

  useEffect(() => setActiveRunTab("status"), [selectedID])

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

  const displayProfiles = useMemo(() => {
    if (!selected?.profile) return profiles
    if (
      selected.status === "draft" &&
      profiles.some((profile) => profile.id === selected.profile?.id)
    ) {
      return profiles
    }
    return [
      profileAsOption(selected.profile, selected.candidate_models),
      ...profiles.filter((profile) => profile.id !== selected.profile?.id),
    ]
  }, [profiles, selected])
  const selectedProfile = displayProfiles.find(
    (profile) => profile.id === draft.profileID,
  )
  const compatibleAliases = useMemo(
    () => new Set(profileAvailableAliases(selectedProfile, models)),
    [models, selectedProfile],
  )
  const displayModels = useMemo(
    () => [
      ...models.map((model) => ({
        ...model,
        available: model.available && compatibleAliases.has(model.alias),
        blocked_reason:
          model.available && compatibleAliases.has(model.alias)
            ? model.blocked_reason
            : !selectedProfile
              ? "Select a review profile first."
              : "Unavailable for this profile account.",
      })),
      ...draft.candidateModels
        .filter((alias) => !models.some((model) => model.alias === alias))
        .map((alias) => ({
          alias,
          resolved_model: "No longer configured",
          available: false,
          blocked_reason: "No longer present in the model catalog.",
        })),
    ],
    [compatibleAliases, draft.candidateModels, models, selectedProfile],
  )
  const validDraft =
    draft.repository.trim() !== "" &&
    Boolean(selectedProfile) &&
    draft.candidateModels.length >= 2 &&
    draft.candidateModels.length <= maxCandidateModels &&
    draft.candidateModels.every((alias) => compatibleAliases.has(alias)) &&
    draft.candidateModels.includes(selectedProfile?.reviewer_model ?? "")
  const configurationEditable = selected == null || selected.status === "draft"
  const configurationControlsDisabled =
    !configurationEditable || busy || loading
  const draftDirty = selected
    ? !draftsEqual(draft, evaluationDraft(selected))
    : false
  const unavailableModelSelected = draft.candidateModels.some(
    (alias) => !compatibleAliases.has(alias),
  )
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

  const openDedicatedReport = () => {
    if (!selected) return
    if (onOpenReport) {
      onOpenReport(selected.id)
    } else {
      window.location.assign(`/model-evaluations/${selected.id}/report`)
    }
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
            setActiveRunTab("status")
            setDraft(emptyDraft(profiles, models, maxCandidateModels))
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
              usage, and efficiency under one reusable review profile. They do
              not create repository findings or issue drafts.
            </div>

            <section className="border-border bg-card rounded-xl border p-4 sm:p-5">
              <div className="mb-4 flex flex-wrap items-center justify-between gap-2">
                <div>
                  <h2 className="font-semibold">Probe setup</h2>
                  <p className="text-muted-foreground text-xs">
                    The selected profile freezes scope, reviewer policy,
                    account, and the work-sizing sweep when the probe starts.
                  </p>
                </div>
                {selected && (
                  <Badge variant="secondary">{selected.status}</Badge>
                )}
              </div>
              <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
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
                    The probe uses a managed fresh checkout and releases it
                    before model calls.
                  </p>
                </Field>
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
                <Field label="Review profile" required>
                  <select
                    aria-label="Review profile"
                    className="border-input bg-background h-9 w-full rounded-md border px-3 text-sm"
                    value={draft.profileID}
                    disabled={configurationControlsDisabled}
                    onChange={(event) => {
                      const profile = displayProfiles.find(
                        (item) => item.id === event.target.value,
                      )
                      setDraft({
                        ...draft,
                        profileID: event.target.value,
                        candidateModels: selectProfileCandidates(
                          draft.candidateModels,
                          profile,
                          models,
                          maxCandidateModels,
                        ),
                      })
                    }}
                  >
                    <option value="">Select profile</option>
                    {displayProfiles.map((profile) => (
                      <option key={profile.id} value={profile.id}>
                        {profile.name} · {profile.reviewer_model}
                      </option>
                    ))}
                  </select>
                  {profileCount === 0 && !selected?.profile && (
                    <p role="alert" className="text-destructive text-xs">
                      Create a repository review profile before running a probe.
                    </p>
                  )}
                  {profileCount > 0 &&
                    profiles.length === 0 &&
                    !selected?.profile && (
                      <p role="alert" className="text-destructive text-xs">
                        No review profile has a runnable reviewer plus a second
                        compatible model on its selected account.
                      </p>
                    )}
                  <a
                    className="text-primary text-xs underline underline-offset-2"
                    href="/repository-reviews/profiles"
                  >
                    Manage review profiles
                  </a>
                </Field>
              </div>

              {selectedProfile && (
                <div
                  aria-label="Frozen review profile"
                  className="border-border bg-muted/40 mt-4 rounded-lg border p-4"
                >
                  <div className="flex flex-wrap items-start justify-between gap-2">
                    <div>
                      <h3 className="font-medium">{selectedProfile.name}</h3>
                      <p className="text-muted-foreground mt-1 text-xs">
                        v{selectedProfile.version} · reviewer{" "}
                        <span className="font-mono">
                          {selectedProfile.reviewer_model}
                        </span>
                        {selectedProfile.account_ref
                          ? ` · account ${selectedProfile.account_ref}`
                          : " · default account"}
                      </p>
                    </div>
                    <Badge variant="outline">Profile policy</Badge>
                  </div>
                  <p className="mt-3 text-sm">{selectedProfile.review_focus}</p>
                  <dl className="mt-4 grid gap-3 text-xs sm:grid-cols-2 lg:grid-cols-4">
                    <div>
                      <dt className="text-muted-foreground">Code types</dt>
                      <dd className="mt-1 font-medium">
                        {selectedProfile.focus.code_types?.join(", ") || "None"}
                      </dd>
                    </div>
                    <div>
                      <dt className="text-muted-foreground">Scope folders</dt>
                      <dd className="mt-1 font-medium">
                        {selectedProfile.focus.include_folders?.join(", ") ||
                          "All eligible folders"}
                      </dd>
                    </div>
                    <div>
                      <dt className="text-muted-foreground">
                        Excluded folders
                      </dt>
                      <dd className="mt-1 font-medium">
                        {selectedProfile.focus.exclude_folders?.join(", ") ||
                          "None"}
                      </dd>
                    </div>
                    <div>
                      <dt className="text-muted-foreground">
                        Files per batch ceiling
                      </dt>
                      <dd className="mt-1 font-medium tabular-nums">
                        {selectedProfile.max_files_per_batch}
                      </dd>
                    </div>
                    <div>
                      <dt className="text-muted-foreground">
                        Content bytes per batch ceiling
                      </dt>
                      <dd className="mt-1 font-medium tabular-nums">
                        {formatBytes(
                          selectedProfile.max_content_bytes_per_batch,
                        )}
                      </dd>
                    </div>
                    <div>
                      <dt className="text-muted-foreground">
                        Parallel review workers
                      </dt>
                      <dd className="mt-1 font-medium tabular-nums">
                        {selectedProfile.max_parallel_children}
                      </dd>
                    </div>
                  </dl>
                  {selectedProfile.focus.free_text && (
                    <p className="text-muted-foreground mt-3 text-xs">
                      <strong className="text-foreground">
                        Additional scope guidance:
                      </strong>{" "}
                      {selectedProfile.focus.free_text}
                    </p>
                  )}
                  <p className="text-muted-foreground mt-3 text-xs">
                    The probe varies files and content bytes independently up to
                    these configured ceilings and records requested versus
                    observed batch sizes.
                  </p>
                </div>
              )}

              <div className="mt-5">
                <Field label="Candidate models" required>
                  <ModelChecks
                    options={displayModels}
                    selected={draft.candidateModels}
                    disabled={configurationControlsDisabled}
                    maximum={maxCandidateModels}
                    requiredAlias={selectedProfile?.reviewer_model}
                    onChange={(candidateModels) =>
                      setDraft({ ...draft, candidateModels })
                    }
                  />
                </Field>
                <p className="text-muted-foreground mt-2 text-xs">
                  Every compatible candidate is tested on the same immutable
                  corpus and sizing plan. The profile reviewer and a second
                  compatible model are selected automatically.
                </p>
              </div>
              {draft.candidateModels.length >= maxCandidateModels && (
                <p className="text-muted-foreground mt-3 text-xs">
                  Candidate-model limit reached ({maxCandidateModels}).
                </p>
              )}
              {unavailableModelSelected && (
                <p role="alert" className="text-destructive mt-3 text-sm">
                  Remove candidate models unavailable for the selected profile
                  account before running.
                </p>
              )}
              {selectedProfile && compatibleAliases.size < 2 && (
                <p role="alert" className="text-destructive mt-3 text-sm">
                  This profile account exposes fewer than two compatible models.
                  Update the profile account or model catalog before running a
                  comparison probe.
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
              <ProbeTabs
                evaluation={selected}
                active={activeRunTab}
                onChange={setActiveRunTab}
              />
            )}

            {selected && activeRunTab === "status" && (
              <div
                id={`probe-panel-${selected.id}-status`}
                role="tabpanel"
                aria-labelledby={`probe-tab-${selected.id}-status`}
              >
                <section className="border-border bg-card rounded-xl border p-4 sm:p-5">
                  <div
                    className="flex items-center justify-between gap-3"
                    aria-live="polite"
                    aria-atomic="true"
                  >
                    <div>
                      <h2 className="font-semibold">Progress and status</h2>
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
                    <span>
                      {selected.progress.selected_files} files selected
                    </span>
                    <span>
                      {selected.progress.completed_files} files analyzed
                    </span>
                    <span>
                      {selected.progress.completed_tasks}/
                      {selected.progress.total_tasks} tasks
                    </span>
                    <span>
                      {selected.usage.input_tokens +
                        selected.usage.output_tokens}{" "}
                      tokens
                    </span>
                  </div>
                  {(selected.progress.total_calls ?? 0) > 0 && (
                    <div
                      role="region"
                      aria-label="Candidate call progress"
                      className="border-border bg-muted/40 mt-4 rounded-lg border p-3"
                    >
                      <div className="grid gap-2 text-xs sm:grid-cols-4">
                        <span>
                          Batch {selected.progress.current_batch ?? 0}/
                          {selected.progress.total_batches ?? 0}
                        </span>
                        <span>
                          {selected.progress.completed_calls ?? 0}/
                          {selected.progress.total_calls ?? 0} candidate calls
                        </span>
                        <span>
                          {selected.progress.active_children?.length ?? 0}{" "}
                          active
                        </span>
                        <span>
                          {selected.progress.failed_calls ?? 0} failed
                        </span>
                      </div>
                      {(selected.progress.active_children?.length ?? 0) > 0 && (
                        <ul className="mt-3 space-y-2 text-xs">
                          {selected.progress.active_children?.map((child) => (
                            <li
                              key={child.index}
                              className="border-border flex flex-wrap justify-between gap-2 border-t pt-2 first:border-0 first:pt-0"
                            >
                              <span className="font-medium">
                                {child.label ||
                                  `Candidate call ${child.index} of ${selected.progress.total_calls ?? 0}`}
                              </span>
                              <span className="text-muted-foreground">
                                {child.model_alias || "resolved model"} ·{" "}
                                {child.scope_count} file
                                {child.scope_count === 1 ? "" : "s"} · started{" "}
                                {formatProgressTime(child.started_at)} · running{" "}
                                {formatProgressElapsed(child.started_at)}
                              </span>
                            </li>
                          ))}
                        </ul>
                      )}
                    </div>
                  )}
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
                  <p className="text-muted-foreground mt-3 text-xs">
                    Last progress update{" "}
                    <time dateTime={selected.progress.updated_at}>
                      {formatProgressTime(selected.progress.updated_at)}
                    </time>
                  </p>
                  {selected.failure && (
                    <p className="text-destructive mt-3 text-sm">
                      {selected.failure}
                    </p>
                  )}
                  {selected.warnings.map((warning) => (
                    <p key={warning} className="mt-2 text-sm text-amber-600">
                      <IconAlertTriangle
                        className="mr-1 inline size-4"
                        aria-hidden="true"
                      />
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
              </div>
            )}

            {selected && activeRunTab === "languages" && (
              <div
                id={`probe-panel-${selected.id}-languages`}
                role="tabpanel"
                aria-labelledby={`probe-tab-${selected.id}-languages`}
              >
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
                  <table className="mt-4 w-full min-w-[36rem] text-left text-sm">
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
                        <th scope="col">Completed</th>
                        <th scope="col">Bytes</th>
                        <th scope="col">Regions</th>
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
                          <td>{progress.completed_files}</td>
                          <td>{formatBytes(progress.selected_bytes)}</td>
                          <td>{progress.regions.length}</td>
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
              </div>
            )}

            {selected && activeRunTab === "corpus" && (
              <div
                id={`probe-panel-${selected.id}-corpus`}
                role="tabpanel"
                aria-labelledby={`probe-tab-${selected.id}-corpus`}
              >
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
                    {corpusPage && corpusPage.total > 0 && (
                      <span className="text-muted-foreground text-xs">
                        Showing {corpusOffset + 1}–
                        {Math.min(
                          corpusOffset + corpusPage.files.length,
                          corpusPage.total,
                        )}{" "}
                        of {corpusPage.total}
                      </span>
                    )}
                  </div>
                  {!corpusPage ? (
                    <p className="text-muted-foreground mt-4 text-sm">
                      Loading corpus references…
                    </p>
                  ) : corpusPage.total === 0 ? (
                    <p className="text-muted-foreground mt-4 text-sm">
                      No corpus references are available yet.
                    </p>
                  ) : (
                    <>
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
                            setCorpusOffset(
                              corpusPage.next_offset ?? corpusOffset,
                            )
                          }
                        >
                          Next corpus page
                        </Button>
                      </div>
                    </>
                  )}
                </section>
              </div>
            )}

            {selected && activeRunTab === "report" && (
              <div
                id={`probe-panel-${selected.id}-report`}
                role="tabpanel"
                aria-labelledby={`probe-tab-${selected.id}-report`}
                className="space-y-4"
              >
                <div className="flex justify-end">
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    onClick={openDedicatedReport}
                  >
                    Open dedicated report
                    <IconExternalLink aria-hidden="true" />
                  </Button>
                </div>
                <ModelEvaluationReportContent evaluation={selected} />
              </div>
            )}
          </div>
        </section>
      </div>
    </div>
  )
}
