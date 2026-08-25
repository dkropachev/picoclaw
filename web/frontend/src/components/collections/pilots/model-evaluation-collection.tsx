import {
  IconEdit,
  IconFileAnalytics,
  IconLanguage,
  IconPlayerPlay,
  IconPlus,
  IconX,
} from "@tabler/icons-react"
import { useInfiniteQuery, useQuery } from "@tanstack/react-query"
import { type FormEvent, useMemo, useState } from "react"
import { toast } from "sonner"

import type { CollectionBulkDeleteResponse } from "@/api/collection"
import {
  type EvaluationConfigInput,
  type EvaluationModelOption,
  type EvaluationStatus,
  ModelEvaluationAPIError,
  type RepositoryModelEvaluation,
  type RepositoryModelEvaluationSummary,
  bulkDeleteModelEvaluations,
  createModelEvaluation,
  getModelEvaluation,
  getModelEvaluationCorpus,
  getModelEvaluationOptions,
  listModelEvaluationsPage,
  runModelEvaluationAction,
  updateModelEvaluation,
} from "@/api/model-evaluations"
import {
  type CollectionDefinition,
  CollectionDetailShell,
} from "@/components/collection"
import {
  profileAvailableAliases,
  selectProfileCandidates,
} from "@/components/model-evaluations/model-evaluation-candidates"
import { Field } from "@/components/shared-form"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  type CollectionRouteSearch,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

import {
  type PilotCollectionSearch,
  StandardPilotCollectionPage,
} from "./standard-pilot-collection-page"

const defaultQuery = "ORDER BY updated DESC"
const supportedViews = ["list", "table", "grid"] as const
const activeStatuses = new Set<EvaluationStatus>([
  "preflighting",
  "ready",
  "running",
  "judging",
  "analyzing",
  "canceling",
])

export function ModelEvaluationsCollectionPage({
  search,
  onSearchChange,
  onAdd,
  onOpen,
  onEdit,
}: {
  search: PilotCollectionSearch
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
  onAdd: () => void
  onOpen: (evaluation: RepositoryModelEvaluationSummary) => void
  onEdit: (evaluation: RepositoryModelEvaluationSummary) => void
}) {
  const activeQuery = normalizeCollectionRouteSearch(
    { ...search },
    { defaultQuery, supportedViews },
  ).q
  const query = useInfiniteQuery({
    queryKey: ["model-evaluations", activeQuery],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      listModelEvaluationsPage(
        { query: activeQuery, cursor: pageParam || undefined, limit: 50 },
        signal,
      ),
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    refetchInterval: (state) => {
      const pages = state.state.data?.pages ?? []
      return pages.some((page) =>
        page.evaluations.some((evaluation) =>
          activeStatuses.has(evaluation.status),
        ),
      )
        ? 2_000
        : false
    },
    retry: false,
  })
  const items = useMemo(
    () => query.data?.pages.flatMap((page) => page.evaluations) ?? [],
    [query.data?.pages],
  )
  const first = query.data?.pages[0]
  const byID = useMemo(
    () => new Map(items.map((item) => [item.id, item])),
    [items],
  )
  const definition = useMemo<
    CollectionDefinition<RepositoryModelEvaluationSummary>
  >(
    () => ({
      key: "model-evaluations",
      title: "Model evaluations",
      defaultQuery,
      supportedViews,
      defaultView: "list",
      getItemID: (item) => item.id,
      getItemLabel: (item) => item.repository || item.id,
      getItemIdentity: (item) => ({
        title: item.repository || "Repository model evaluation",
        description: item.ref,
        metadata: `${item.candidate_models.length} candidate models`,
      }),
      columns: [
        { id: "status", header: "Status", cell: (item) => item.status },
        {
          id: "progress",
          header: "Progress",
          cell: (item) => `${Math.round(item.progress.percent)}%`,
          className: "w-24 tabular-nums",
        },
        {
          id: "updated",
          header: "Updated",
          cell: (item) => formatTimestamp(item.updated_at),
          className: "w-44",
        },
      ],
      gridFacts: [
        { id: "status", label: "Status", value: (item) => item.status },
        {
          id: "progress",
          label: "Progress",
          value: (item) => `${Math.round(item.progress.percent)}%`,
        },
        {
          id: "models",
          label: "Models",
          value: (item) => item.candidate_models.length,
        },
        {
          id: "updated",
          label: "Updated",
          value: (item) => formatTimestamp(item.updated_at),
        },
      ],
      badges: [
        {
          id: "status",
          label: (item) => item.status,
          variant: "outline",
        },
      ],
      actions: [
        {
          id: "edit",
          label: "Edit evaluation",
          icon: <IconEdit />,
          hidden: (item) => item.status !== "draft",
          onSelect: onEdit,
        },
      ],
    }),
    [onEdit],
  )

  return (
    <StandardPilotCollectionPage
      definition={definition}
      search={search}
      onSearchChange={onSearchChange}
      items={items}
      total={first?.total}
      schema={first?.query_schema}
      canonicalQuery={first?.canonical_query}
      loading={query.isLoading}
      fetching={query.isFetching}
      error={query.error}
      onRefresh={query.refetch}
      hasNextPage={query.hasNextPage}
      loadingMore={query.isFetchingNextPage}
      onLoadMore={query.fetchNextPage}
      onOpenItem={onOpen}
      addAction={
        <Button type="button" size="sm" onClick={onAdd}>
          <IconPlus /> New evaluation
        </Button>
      }
      onBulkDelete={async (ids) => {
        const response = await bulkDeleteModelEvaluations(
          ids.map((id) => ({ id, version: byID.get(id)?.version ?? 0 })),
        )
        return response satisfies CollectionBulkDeleteResponse
      }}
      isItemSelectable={(evaluation) => evaluation.status === "draft"}
      afterBulkDelete={() => query.refetch()}
      emptyTitle="No model evaluations"
      emptyDescription="Create a draft evaluation to compare configured model aliases on a repository corpus."
    />
  )
}

export function ModelEvaluationDetailPage({
  id,
  onBack,
  onEdit,
  onLanguages,
  onCorpus,
  onReport,
}: {
  id: string
  onBack: () => void
  onEdit: () => void
  onLanguages: () => void
  onCorpus: () => void
  onReport: () => void
}) {
  const query = useEvaluationDetail(id)
  const evaluation = query.data
  const notFound =
    query.error instanceof ModelEvaluationAPIError && query.error.status === 404
  const [acting, setActing] = useState("")
  const action = lifecycleAction(evaluation?.status)
  const runAction = async () => {
    if (!evaluation || !action) return
    setActing(action)
    try {
      await runModelEvaluationAction(evaluation.id, action, evaluation.version)
      await query.refetch()
      toast.success(`Evaluation action “${action}” started.`)
    } catch (error) {
      toast.error(
        error instanceof Error ? error.message : "Evaluation action failed.",
      )
    } finally {
      setActing("")
    }
  }
  return (
    <CollectionDetailShell
      title={evaluation?.repository || "Model evaluation"}
      identity={<span className="font-mono text-xs">{id}</span>}
      status={
        evaluation ? (
          <Badge variant="outline">{evaluation.status}</Badge>
        ) : undefined
      }
      actions={
        evaluation ? (
          <>
            {evaluation.status === "draft" && (
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={onEdit}
              >
                <IconEdit /> Edit
              </Button>
            )}
            {action && (
              <Button
                type="button"
                size="sm"
                onClick={() => void runAction()}
                disabled={Boolean(acting)}
              >
                {action === "cancel" ? <IconX /> : <IconPlayerPlay />}
                {acting ? "Working…" : actionLabel(action)}
              </Button>
            )}
          </>
        ) : undefined
      }
      loading={query.isLoading}
      error={!notFound ? query.error?.message : undefined}
      notFound={notFound}
      onBack={onBack}
      onRetry={() => void query.refetch()}
      backLabel="All model evaluations"
    >
      {evaluation && (
        <div className="space-y-6">
          <DetailRows
            rows={[
              ["Repository", evaluation.repository || "Local repository"],
              ["Reference", evaluation.ref],
              ["Candidate models", evaluation.candidate_models.join(", ")],
              ["Profile", evaluation.profile?.name ?? "—"],
              [
                "Progress",
                `${Math.round(evaluation.progress.percent)}% · ${evaluation.progress.stage}`,
              ],
              ["Updated", formatTimestamp(evaluation.updated_at)],
            ]}
          />
          {evaluation.progress.message && (
            <p className="border-border bg-muted/30 rounded-lg border px-3 py-3 text-sm">
              {evaluation.progress.message}
            </p>
          )}
          <div className="grid gap-3 sm:grid-cols-3">
            <RelatedButton
              icon={<IconLanguage />}
              label="Languages"
              detail={`${Object.keys(evaluation.progress.languages).length} tracked`}
              onClick={onLanguages}
            />
            <RelatedButton
              icon={<IconFileAnalytics />}
              label="Corpus"
              detail={`${evaluation.progress.selected_files} selected files`}
              onClick={onCorpus}
            />
            <RelatedButton
              icon={<IconFileAnalytics />}
              label="Report"
              detail={
                evaluation.status === "completed"
                  ? "View comparison"
                  : "Available after completion"
              }
              disabled={evaluation.status !== "completed"}
              onClick={onReport}
            />
          </div>
        </div>
      )}
    </CollectionDetailShell>
  )
}

export function ModelEvaluationLanguagesPage({
  id,
  onBack,
}: {
  id: string
  onBack: () => void
}) {
  const query = useEvaluationDetail(id)
  const evaluation = query.data
  const notFound =
    query.error instanceof ModelEvaluationAPIError && query.error.status === 404
  return (
    <CollectionDetailShell
      title="Evaluation languages"
      identity={<span className="font-mono text-xs">{id}</span>}
      loading={query.isLoading}
      error={!notFound ? query.error?.message : undefined}
      notFound={notFound}
      onBack={onBack}
      onRetry={() => void query.refetch()}
      backLabel="Evaluation details"
    >
      {evaluation && (
        <div className="border-border divide-border rounded-lg border">
          {Object.entries(evaluation.progress.languages).map(
            ([language, progress]) => (
              <div
                key={language}
                className="grid gap-2 border-b px-3 py-3 last:border-b-0 sm:grid-cols-[minmax(8rem,1fr)_repeat(3,7rem)] sm:items-center"
              >
                <span className="font-medium">{language}</span>
                <span className="text-muted-foreground text-xs tabular-nums">
                  {progress.selected_files} selected
                </span>
                <span className="text-muted-foreground text-xs tabular-nums">
                  {progress.completed_files} complete
                </span>
                <span className="text-muted-foreground text-xs tabular-nums">
                  {progress.selected_bytes} bytes
                </span>
              </div>
            ),
          )}
          {Object.keys(evaluation.progress.languages).length === 0 && (
            <p className="text-muted-foreground px-3 py-10 text-center text-sm">
              Language selection is not available yet.
            </p>
          )}
        </div>
      )}
    </CollectionDetailShell>
  )
}

export function ModelEvaluationCorpusPage({
  id,
  onBack,
}: {
  id: string
  onBack: () => void
}) {
  const query = useInfiniteQuery({
    queryKey: ["model-evaluation-corpus", id],
    initialPageParam: 0,
    queryFn: ({ pageParam, signal }) =>
      getModelEvaluationCorpus(id, pageParam, 200, signal),
    getNextPageParam: (lastPage) => lastPage.next_offset,
    retry: false,
  })
  const firstPage = query.data?.pages[0]
  const files = query.data?.pages.flatMap((page) => page.files) ?? []
  const notFound =
    query.error instanceof ModelEvaluationAPIError && query.error.status === 404
  return (
    <CollectionDetailShell
      title="Evaluation corpus"
      identity={<span className="font-mono text-xs">{id}</span>}
      loading={query.isLoading}
      error={!notFound ? query.error?.message : undefined}
      notFound={notFound}
      onBack={onBack}
      onRetry={() => void query.refetch()}
      backLabel="Evaluation details"
    >
      {firstPage && (
        <div className="space-y-3">
          <p className="text-muted-foreground text-sm">
            {firstPage.total} files in the pinned corpus.
          </p>
          <div className="border-border divide-border rounded-lg border">
            {files.map((file) => (
              <div
                key={file.candidate_id}
                className="flex min-w-0 items-center gap-3 border-b px-3 py-3 text-sm last:border-b-0"
              >
                <span className="min-w-0 flex-1 truncate font-mono text-xs">
                  {file.path}
                </span>
                <Badge variant="outline">{file.language}</Badge>
                <span className="text-muted-foreground text-xs tabular-nums">
                  {file.size_bytes} B
                </span>
              </div>
            ))}
          </div>
          {query.hasNextPage && (
            <div className="flex justify-center">
              <Button
                type="button"
                variant="outline"
                disabled={query.isFetchingNextPage}
                onClick={() => void query.fetchNextPage()}
              >
                {query.isFetchingNextPage ? "Loading…" : "Load more files"}
              </Button>
            </div>
          )}
        </div>
      )}
    </CollectionDetailShell>
  )
}

export function ModelEvaluationEditorPage({
  id,
  onBack,
  onSaved,
}: {
  id?: string
  onBack: () => void
  onSaved: (id: string) => void
}) {
  const detail = useQuery({
    queryKey: ["model-evaluation", id],
    queryFn: ({ signal }) => getModelEvaluation(id ?? "", signal),
    enabled: Boolean(id),
    retry: false,
  })
  const options = useQuery({
    queryKey: ["model-evaluation-options"],
    queryFn: ({ signal }) => getModelEvaluationOptions(signal),
    retry: false,
  })
  const notFound =
    detail.error instanceof ModelEvaluationAPIError &&
    detail.error.status === 404
  const immutable = Boolean(detail.data && detail.data.status !== "draft")
  return (
    <CollectionDetailShell
      title={id ? "Edit model evaluation" : "New model evaluation"}
      identity={
        id ? <span className="font-mono text-xs">{id}</span> : undefined
      }
      loading={(Boolean(id) && detail.isLoading) || options.isLoading}
      error={
        !notFound
          ? (detail.error?.message ?? options.error?.message)
          : undefined
      }
      notFound={notFound}
      onBack={onBack}
      onRetry={() => void Promise.all([detail.refetch(), options.refetch()])}
      backLabel="All model evaluations"
    >
      {immutable && detail.data ? (
        <div
          role="status"
          className="border-border mx-auto max-w-2xl rounded-lg border border-dashed p-8 text-center"
        >
          <h2 className="font-semibold">This evaluation is read-only</h2>
          <p className="text-muted-foreground mt-2 text-sm">
            Only draft evaluations can be edited. This evaluation is currently{" "}
            <span className="font-medium">{detail.data.status}</span>.
          </p>
          <Button
            type="button"
            variant="outline"
            className="mt-4"
            onClick={onBack}
          >
            Return to evaluation
          </Button>
        </div>
      ) : options.data && (!id || detail.data) ? (
        <EvaluationForm
          initial={detail.data}
          options={options.data}
          onCancel={onBack}
          onSaved={onSaved}
        />
      ) : null}
    </CollectionDetailShell>
  )
}

function EvaluationForm({
  initial,
  options,
  onCancel,
  onSaved,
}: {
  initial?: RepositoryModelEvaluation
  options: Awaited<ReturnType<typeof getModelEvaluationOptions>>
  onCancel: () => void
  onSaved: (id: string) => void
}) {
  const initialProfileID = initial?.profile?.id ?? options.profiles[0]?.id ?? ""
  const initialProfile = options.profiles.find(
    (profile) => profile.id === initialProfileID,
  )
  const [repository, setRepository] = useState(
    initial?.repository ?? options.repositories[0]?.repository ?? "",
  )
  const [profileID, setProfileID] = useState(initialProfileID)
  const [ref, setRef] = useState(initial?.ref ?? "HEAD")
  const [models, setModels] = useState<string[]>(
    initial?.candidate_models ??
      selectProfileCandidates(
        [],
        initialProfile,
        options.models,
        options.max_candidate_models,
      ),
  )
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState("")
  const selectedProfile = options.profiles.find(
    (profile) => profile.id === profileID,
  )
  const compatibleAliases = new Set(
    profileAvailableAliases(selectedProfile, options.models),
  )
  const toggleModel = (alias: string, checked: boolean) =>
    setModels((current) =>
      checked
        ? [...new Set([...current, alias])]
        : current.filter((item) => item !== alias),
    )
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!repository.trim() || !selectedProfile) {
      setError(
        "Repository, profile, and at least two candidate models are required.",
      )
      return
    }
    if (
      models.length < 2 ||
      models.length > options.max_candidate_models ||
      !models.includes(selectedProfile.reviewer_model) ||
      models.some((alias) => !compatibleAliases.has(alias))
    ) {
      setError(
        "Select the profile reviewer and at least one other compatible candidate model.",
      )
      return
    }
    const input: EvaluationConfigInput = {
      repository: repository.trim(),
      profile_id: profileID,
      candidate_models: models,
      ref: ref.trim() || "HEAD",
      ...(initial ? { expected_version: initial.version } : {}),
    }
    setSaving(true)
    setError("")
    try {
      const saved = initial
        ? await updateModelEvaluation(initial.id, input)
        : await createModelEvaluation(input)
      toast.success(initial ? "Evaluation saved." : "Evaluation draft created.")
      onSaved(saved.id)
    } catch (saveError) {
      setError(saveError instanceof Error ? saveError.message : "Save failed")
    } finally {
      setSaving(false)
    }
  }
  return (
    <form
      className="mx-auto max-w-3xl space-y-6"
      onSubmit={(event) => void submit(event)}
    >
      <Field label="Repository" required>
        {options.repositories.length > 0 ? (
          <Select
            value={repository}
            disabled={saving || Boolean(initial)}
            onValueChange={setRepository}
          >
            <SelectTrigger aria-label="Repository">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {options.repositories.map((item) => (
                <SelectItem key={item.id} value={item.repository}>
                  {item.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        ) : (
          <Input
            value={repository}
            aria-label="Repository"
            disabled={saving || Boolean(initial)}
            onChange={(event) => setRepository(event.target.value)}
          />
        )}
      </Field>
      <Field label="Repository reference">
        <Input
          value={ref}
          aria-label="Repository reference"
          disabled={saving}
          className="font-mono"
          onChange={(event) => setRef(event.target.value)}
        />
      </Field>
      <Field label="Review profile" required>
        <Select
          value={profileID}
          disabled={saving}
          onValueChange={(nextProfileID) => {
            const profile = options.profiles.find(
              (item) => item.id === nextProfileID,
            )
            setProfileID(nextProfileID)
            setModels((current) =>
              selectProfileCandidates(
                current,
                profile,
                options.models,
                options.max_candidate_models,
              ),
            )
          }}
        >
          <SelectTrigger aria-label="Review profile">
            <SelectValue placeholder="Choose a profile" />
          </SelectTrigger>
          <SelectContent>
            {options.profiles.map((profile) => (
              <SelectItem key={profile.id} value={profile.id}>
                {profile.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      <fieldset className="space-y-2">
        <legend className="text-sm font-medium">Candidate models *</legend>
        <div className="border-border divide-border rounded-lg border">
          {options.models.map((model) => (
            <CandidateModelOption
              key={model.alias}
              model={model}
              selected={models}
              reviewerModel={selectedProfile?.reviewer_model}
              compatible={compatibleAliases.has(model.alias)}
              maximum={options.max_candidate_models}
              saving={saving}
              onToggle={toggleModel}
            />
          ))}
        </div>
      </fieldset>
      {error && (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      )}
      <div className="border-border flex justify-end gap-2 border-t pt-4">
        <Button
          type="button"
          variant="outline"
          disabled={saving}
          onClick={onCancel}
        >
          Cancel
        </Button>
        <Button type="submit" disabled={saving}>
          {saving ? "Saving…" : "Save evaluation"}
        </Button>
      </div>
    </form>
  )
}

function CandidateModelOption({
  model,
  selected,
  reviewerModel,
  compatible,
  maximum,
  saving,
  onToggle,
}: {
  model: EvaluationModelOption
  selected: string[]
  reviewerModel?: string
  compatible: boolean
  maximum: number
  saving: boolean
  onToggle: (alias: string, checked: boolean) => void
}) {
  const checked = selected.includes(model.alias)
  const required = model.alias === reviewerModel
  const available = model.available && compatible
  return (
    <label className="flex items-center gap-3 border-b px-3 py-3 text-sm last:border-b-0">
      <Checkbox
        checked={checked}
        aria-label={`Select candidate model ${model.alias}`}
        disabled={
          saving ||
          (checked && required) ||
          (!available && !checked) ||
          (!checked && selected.length >= maximum)
        }
        onCheckedChange={(nextChecked) =>
          onToggle(model.alias, nextChecked === true)
        }
      />
      <span className="min-w-0 flex-1">
        <span className="font-medium">{model.alias}</span>
        {required && (
          <span className="text-muted-foreground ml-2 text-xs">
            required by profile
          </span>
        )}
        <span className="text-muted-foreground ml-2 font-mono text-xs">
          {model.resolved_model}
        </span>
      </span>
      {!available && <Badge variant="outline">Unavailable</Badge>}
    </label>
  )
}

function useEvaluationDetail(id: string) {
  return useQuery({
    queryKey: ["model-evaluation", id],
    queryFn: ({ signal }) => getModelEvaluation(id, signal),
    retry: false,
    refetchInterval: (query) =>
      query.state.data && activeStatuses.has(query.state.data.status)
        ? 2_000
        : false,
  })
}

function lifecycleAction(status?: EvaluationStatus) {
  if (status === "draft") return "preflight" as const
  if (status === "ready") return "start" as const
  if (status === "failed") return "resume" as const
  if (status && activeStatuses.has(status) && status !== "canceling")
    return "cancel" as const
  return undefined
}

function actionLabel(action: NonNullable<ReturnType<typeof lifecycleAction>>) {
  if (action === "preflight") return "Preflight"
  if (action === "start") return "Start"
  if (action === "resume") return "Resume"
  return "Cancel"
}

function RelatedButton({
  icon,
  label,
  detail,
  disabled,
  onClick,
}: {
  icon: React.ReactNode
  label: string
  detail: string
  disabled?: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      disabled={disabled}
      onClick={onClick}
      className="border-border hover:bg-muted/30 flex min-w-0 items-center gap-3 rounded-lg border p-3 text-left disabled:opacity-50"
    >
      <span className="text-muted-foreground [&_svg]:size-5">{icon}</span>
      <span className="min-w-0">
        <span className="block text-sm font-medium">{label}</span>
        <span className="text-muted-foreground block truncate text-xs">
          {detail}
        </span>
      </span>
    </button>
  )
}

function DetailRows({ rows }: { rows: Array<[string, string]> }) {
  return (
    <dl className="border-border divide-border rounded-lg border text-sm">
      {rows.map(([label, value]) => (
        <div
          key={label}
          className="grid grid-cols-[minmax(8rem,auto)_minmax(0,1fr)] gap-4 border-b px-3 py-3 last:border-b-0"
        >
          <dt className="text-muted-foreground">{label}</dt>
          <dd className="min-w-0 text-right break-words">{value || "—"}</dd>
        </div>
      ))}
    </dl>
  )
}

function formatTimestamp(value?: string) {
  if (!value) return "—"
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}
