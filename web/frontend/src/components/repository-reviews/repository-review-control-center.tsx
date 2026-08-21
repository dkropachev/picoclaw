import {
  IconAlertTriangle,
  IconBolt,
  IconChartBar,
  IconEdit,
  IconPlayerPause,
  IconPlayerPlay,
  IconPlus,
  IconRefresh,
  IconRotateClockwise,
  IconTrash,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { type FormEvent, useState } from "react"

import {
  type RepositoryReviewAutomation,
  type RepositoryReviewAutomationConfig,
  type RepositoryReviewCodeType,
  type RepositoryReviewModelStats,
  type ReviewAccountLimitEntry,
  type ReviewAccountOption,
  type ReviewModelOption,
  createRepositoryReviewAutomation,
  deleteRepositoryReviewAutomation,
  getRepositoryReviewAutomationOptions,
  listRepositoryReviewAutomations,
  pauseRepositoryReviewAutomation,
  restartRepositoryReviewAutomation,
  resumeRepositoryReviewAutomation,
  startRepositoryReviewAutomation,
  updateRepositoryReviewAutomation,
} from "@/api/repository-reviews"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"

const automationKey = ["repository-review-automations"] as const
const automationOptionsKey = ["repository-review-automation-options"] as const
const runningPollMilliseconds = 2_000
const pausedPollMilliseconds = 15_000
const scopeCodeTypeOptions: Array<{
  value: RepositoryReviewCodeType
  label: string
  description: string
}> = [
  {
    value: "hotpath-code",
    label: "Hot-path production code",
    description: "Runtime entry points and high-impact execution paths.",
  },
  {
    value: "code",
    label: "Production code",
    description: "Normal application and library implementation files.",
  },
  {
    value: "test",
    label: "Tests",
    description: "Test suites and their supporting code.",
  },
  {
    value: "bench-test",
    label: "Benchmarks",
    description: "Benchmark and performance-test files.",
  },
]

type FormState = Omit<RepositoryReviewAutomationConfig, "model_prices"> & {
  model_prices: Record<
    string,
    {
      input_price_per_1m: string
      output_price_per_1m: string
      subscription?: boolean
      equivalent_model?: string
      price_known?: boolean
    }
  >
}

const emptyForm: FormState = {
  name: "",
  repository: "",
  ref: "HEAD",
  target: "all",
  review_focus: "Find correctness, security, and reliability bugs.",
  scope_policy: {
    code_types: ["hotpath-code", "code"],
    include_folders: [],
    exclude_folders: [],
    free_text: "",
  },
  reviewer_models: [],
  compare_models: false,
  force: false,
  max_files_per_run: 24,
  max_content_bytes: 524_288,
  max_parallel_children: 1,
  estimated_output_tokens: 4_096,
  auto_continue: true,
  model_prices: {},
  budget: {
    max_total_tokens: 250_000,
    max_estimated_cost_usd: 25,
    account_ids: [],
    min_remaining_percent: 0,
    min_remaining_percent_by_window: {},
    auto_resume: true,
    pause_on_unknown: false,
    check_interval_seconds: 900,
  },
}

export function RepositoryReviewControlCenter() {
  const queryClient = useQueryClient()
  const [editor, setEditor] = useState<FormState | null>(null)
  const [editingAutomation, setEditingAutomation] =
    useState<RepositoryReviewAutomation | null>(null)
  const [actionError, setActionError] = useState("")
  const [editorError, setEditorError] = useState("")

  const automationsQuery = useQuery({
    queryKey: automationKey,
    queryFn: ({ signal }) => listRepositoryReviewAutomations(signal),
    refetchInterval: (query) => {
      const automations = query.state.data?.automations ?? []
      if (
        automations.some(
          (automation) =>
            automation.status === "running" ||
            automation.status === "stopping" ||
            isQueuedHandoff(automation),
        )
      ) {
        return runningPollMilliseconds
      }
      if (
        automations.some(
          (automation) =>
            automation.status === "paused" &&
            automation.budget.auto_resume &&
            isAutoResumePause(automation.pause_reason),
        )
      ) {
        return pausedPollMilliseconds
      }
      return false
    },
  })
  const optionsQuery = useQuery({
    queryKey: automationOptionsKey,
    queryFn: ({ signal }) => getRepositoryReviewAutomationOptions(signal),
  })
  const automations = automationsQuery.data?.automations ?? []
  const options = optionsQuery.data ?? { models: [], accounts: [] }

  const saveMutation = useMutation({
    mutationFn: async ({
      form,
      automation,
    }: {
      form: FormState
      automation: RepositoryReviewAutomation | null
    }) => {
      const config = formPayload(form)
      return automation
        ? updateRepositoryReviewAutomation(automation.id, {
            ...config,
            expected_version: automation.version,
          })
        : createRepositoryReviewAutomation(config)
    },
    onSuccess: (automation) => {
      updateCachedAutomation(queryClient, automation)
      setEditor(null)
      setEditingAutomation(null)
      setActionError("")
      setEditorError("")
    },
    onError: (error) => {
      const message = errorMessage(error)
      setEditorError(
        isConflictError(error)
          ? `${message} The profile changed on the server; close and reopen the editor to load the latest version.`
          : message,
      )
      void automationsQuery.refetch()
    },
  })

  const actionMutation = useMutation({
    mutationFn: async ({
      automation,
      action,
      resetBudget,
    }: {
      automation: RepositoryReviewAutomation
      action: "start" | "pause" | "resume" | "restart"
      resetBudget?: boolean
    }) => {
      const input = { expected_version: automation.version }
      switch (action) {
        case "start":
          return startRepositoryReviewAutomation(automation.id, input)
        case "pause":
          return pauseRepositoryReviewAutomation(automation.id, input)
        case "resume":
          return resumeRepositoryReviewAutomation(automation.id, {
            ...input,
            ...(resetBudget ? { reset_budget: true } : {}),
          })
        case "restart":
          return restartRepositoryReviewAutomation(automation.id, {
            ...input,
            ...(resetBudget ? { reset_budget: true } : {}),
          })
      }
    },
    onSuccess: (automation) => {
      updateCachedAutomation(queryClient, automation)
      setActionError("")
    },
    onError: (error) => {
      setActionError(errorMessage(error))
      void automationsQuery.refetch()
    },
  })

  const deleteMutation = useMutation({
    mutationFn: (automation: RepositoryReviewAutomation) =>
      deleteRepositoryReviewAutomation(automation.id, {
        expected_version: automation.version,
      }),
    onSuccess: (_result, removed) => {
      queryClient.setQueryData<{ automations: RepositoryReviewAutomation[] }>(
        automationKey,
        (current) => ({
          automations: (current?.automations ?? []).filter(
            (automation) => automation.id !== removed.id,
          ),
        }),
      )
      setActionError("")
    },
    onError: (error) => {
      setActionError(errorMessage(error))
      void automationsQuery.refetch()
    },
  })

  const openNew = () => {
    setEditingAutomation(null)
    setEditorError("")
    setEditor(copyForm(emptyForm))
  }
  const openEdit = (automation: RepositoryReviewAutomation) => {
    setEditingAutomation(automation)
    setEditorError("")
    setEditor(automationForm(automation))
  }

  return (
    <section
      aria-labelledby="review-automation-heading"
      className="mx-auto w-full max-w-[96rem] space-y-4 py-4"
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <IconBolt className="text-primary size-5" />
            <h2
              id="review-automation-heading"
              className="text-lg font-semibold"
            >
              Review control center
            </h2>
          </div>
          <p className="text-muted-foreground mt-1 max-w-3xl text-sm">
            Configure persistent repository reviews, compare models, and stop
            safely before token, cost, or account limits are exhausted.
          </p>
        </div>
        <div className="flex gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={automationsQuery.isFetching}
            onClick={() => void automationsQuery.refetch()}
          >
            <IconRefresh /> Refresh
          </Button>
          <Button type="button" size="sm" onClick={openNew}>
            <IconPlus /> New review profile
          </Button>
        </div>
      </div>

      {actionError && (
        <div
          role="alert"
          className="border-destructive/30 bg-destructive/5 text-destructive flex items-start gap-2 rounded-lg border p-3 text-sm"
        >
          <IconAlertTriangle className="mt-0.5 size-4 shrink-0" />
          {actionError}
        </div>
      )}

      {automationsQuery.isPending ? (
        <Card size="sm">
          <CardContent className="text-muted-foreground py-8 text-center">
            Loading review profiles…
          </CardContent>
        </Card>
      ) : automationsQuery.isError ? (
        <Card size="sm">
          <CardContent className="space-y-3 py-8 text-center">
            <p>Review profiles could not be loaded.</p>
            <Button
              type="button"
              variant="outline"
              onClick={() => void automationsQuery.refetch()}
            >
              Retry
            </Button>
          </CardContent>
        </Card>
      ) : automations.length === 0 ? (
        <Card size="sm" className="border-dashed">
          <CardContent className="flex flex-col items-center gap-3 py-10 text-center">
            <div className="bg-muted rounded-full p-3">
              <IconChartBar className="size-6" />
            </div>
            <div>
              <p className="font-medium">Set up your first pre-review</p>
              <p className="text-muted-foreground mt-1 max-w-xl text-sm">
                Pick a repository and models, then add spend and account
                guardrails before kicking it off.
              </p>
            </div>
            <Button type="button" onClick={openNew}>
              <IconPlus /> Configure pre-review
            </Button>
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-4 xl:grid-cols-2">
          {automations.map((automation) => (
            <AutomationCard
              key={automation.id}
              automation={automation}
              busy={actionMutation.isPending || deleteMutation.isPending}
              onAction={(action, resetBudget) =>
                actionMutation.mutate({ automation, action, resetBudget })
              }
              onEdit={() => openEdit(automation)}
              onDelete={() => deleteMutation.mutate(automation)}
            />
          ))}
        </div>
      )}

      <Dialog
        open={editor !== null}
        onOpenChange={(open) => {
          if (!open) {
            setEditor(null)
            setEditingAutomation(null)
            setEditorError("")
          }
        }}
      >
        <DialogContent className="max-h-[92vh] overflow-y-auto sm:max-w-4xl">
          <DialogHeader>
            <DialogTitle>
              {editingAutomation ? "Edit review profile" : "New review profile"}
            </DialogTitle>
            <DialogDescription>
              This profile persists between runs. Guardrails are checked before
              more work is admitted.
            </DialogDescription>
          </DialogHeader>
          {editor && (
            <AutomationForm
              value={editor}
              models={options.models}
              accounts={options.accounts}
              limitsError={optionsQuery.data?.limits_error}
              optionsError={
                optionsQuery.isError
                  ? errorMessage(optionsQuery.error)
                  : undefined
              }
              optionsLoading={optionsQuery.isPending}
              busy={saveMutation.isPending}
              error={editorError}
              onChange={setEditor}
              onRetryOptions={() => void optionsQuery.refetch()}
              onSubmit={() =>
                saveMutation.mutate({
                  form: editor,
                  automation: editingAutomation,
                })
              }
              onCancel={() => {
                setEditor(null)
                setEditingAutomation(null)
                setEditorError("")
              }}
            />
          )}
        </DialogContent>
      </Dialog>
    </section>
  )
}

function AutomationForm({
  value,
  models,
  accounts,
  limitsError,
  optionsError,
  optionsLoading,
  busy,
  error,
  onChange,
  onRetryOptions,
  onSubmit,
  onCancel,
}: {
  value: FormState
  models: ReviewModelOption[]
  accounts: ReviewAccountOption[]
  limitsError?: string
  optionsError?: string
  optionsLoading: boolean
  busy: boolean
  error: string
  onChange: (value: FormState) => void
  onRetryOptions: () => void
  onSubmit: () => void
  onCancel: () => void
}) {
  const [newWindow, setNewWindow] = useState("")
  const set = <K extends keyof FormState>(key: K, next: FormState[K]) =>
    onChange({ ...value, [key]: next })
  const setBudget = <K extends keyof FormState["budget"]>(
    key: K,
    next: FormState["budget"][K],
  ) => onChange({ ...value, budget: { ...value.budget, [key]: next } })
  const displayModels: ReviewModelOption[] = [
    ...models,
    ...value.reviewer_models
      .filter((alias) => !models.some((model) => model.alias === alias))
      .map((alias) => ({
        alias,
        resolved_model: "No longer configured",
        provider: "unknown",
        available: false,
        blocked_reason: "No longer present in the model catalog.",
        price_known: Object.hasOwn(value.model_prices, alias),
        input_price_per_1m: 0,
        output_price_per_1m: 0,
      })),
  ]
  const unavailableSelectedModel = value.reviewer_models.some(
    (alias) =>
      !models.some((model) => model.alias === alias && model.available),
  )
  const costBudgetAvailable = hasBillableModelPrices(value)
  const displayAccounts: ReviewAccountOption[] = [
    ...accounts,
    ...value.budget.account_ids
      .filter((id) => !accounts.some((account) => account.id === id))
      .map((id) => ({
        id,
        provider: "unknown",
        label: id,
        status: "no longer available",
        entries: [],
      })),
  ]
  const customWindowKeys = Array.from(
    new Set([
      ...Object.keys(value.budget.min_remaining_percent_by_window),
      ...displayAccounts
        .filter((account) => value.budget.account_ids.includes(account.id))
        .flatMap((account) =>
          account.entries.flatMap((entry) =>
            typeof entry.window === "string" ? [entry.window] : [],
          ),
        ),
    ]),
  )
    .map((window) => window.trim().toLowerCase())
    .filter(
      (window) =>
        window &&
        window !== "default" &&
        window !== "daily" &&
        window !== "weekly",
    )
    .sort()
  const submit = (event: FormEvent) => {
    event.preventDefault()
    onSubmit()
  }
  const scopeError = repositoryReviewScopeError(value)

  return (
    <form className="space-y-6" onSubmit={submit}>
      <fieldset className="grid gap-4 md:grid-cols-2">
        <legend className="sr-only">Repository review profile</legend>
        <TextField
          id="automation-name"
          label="Profile name"
          required
          value={value.name}
          placeholder="Weekly core review"
          onChange={(next) => set("name", next)}
        />
        <TextField
          id="automation-repository"
          label="Repository"
          required
          value={value.repository}
          placeholder="owner/repository or /workspace/repository"
          onChange={(next) => set("repository", next)}
        />
        <TextField
          id="automation-ref"
          label="Git ref"
          required
          value={value.ref}
          onChange={(next) => set("ref", next)}
        />
        <TextField
          id="automation-target"
          label="Review target"
          required
          value={value.target}
          placeholder="all, changed, or a path"
          onChange={(next) => set("target", next)}
        />
        <div className="space-y-2 md:col-span-2">
          <Label htmlFor="automation-focus">Review focus</Label>
          <Textarea
            id="automation-focus"
            required
            value={value.review_focus}
            onChange={(event) => set("review_focus", event.target.value)}
          />
        </div>
      </fieldset>

      <fieldset className="space-y-4 rounded-lg border p-4">
        <legend className="px-1 font-medium">Review scope</legend>
        <p className="text-muted-foreground text-xs">
          Choose one or more inventory code types. Folder prefixes are exact,
          repository-relative paths; exclusions always win over category and
          inclusion matches.
        </p>
        <div className="grid gap-2 sm:grid-cols-2">
          {scopeCodeTypeOptions.map((option) => (
            <div
              key={option.value}
              className="flex items-start gap-2 rounded-md border p-3"
            >
              <input
                id={`scope-code-type-${option.value}`}
                aria-label={option.label}
                type="checkbox"
                className="mt-1"
                checked={value.scope_policy.code_types.includes(option.value)}
                onChange={(event) => {
                  const codeTypes = event.target.checked
                    ? [...value.scope_policy.code_types, option.value]
                    : value.scope_policy.code_types.filter(
                        (codeType) => codeType !== option.value,
                      )
                  set("scope_policy", {
                    ...value.scope_policy,
                    code_types: codeTypes,
                  })
                }}
              />
              <span>
                <span className="block font-medium">{option.label}</span>
                <span className="text-muted-foreground block text-xs">
                  {option.description}
                </span>
              </span>
            </div>
          ))}
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-2">
            <Label htmlFor="scope-include-folders">
              Include folder prefixes
            </Label>
            <Textarea
              id="scope-include-folders"
              value={value.scope_policy.include_folders.join("\n")}
              placeholder={"cmd\ninternal/runtime"}
              onChange={(event) =>
                set("scope_policy", {
                  ...value.scope_policy,
                  include_folders: event.target.value.split(/\r?\n/u),
                })
              }
            />
            <p className="text-muted-foreground text-xs">
              One canonical repository-relative folder per line. Empty means
              every folder for the selected code types.
            </p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="scope-exclude-folders">
              Exclude folder prefixes
            </Label>
            <Textarea
              id="scope-exclude-folders"
              value={value.scope_policy.exclude_folders.join("\n")}
              placeholder={"vendor\ninternal/generated"}
              onChange={(event) =>
                set("scope_policy", {
                  ...value.scope_policy,
                  exclude_folders: event.target.value.split(/\r?\n/u),
                })
              }
            />
            <p className="text-muted-foreground text-xs">
              Excluded prefixes win even when the same path is included above.
            </p>
          </div>
        </div>
        <div className="space-y-2">
          <Label htmlFor="scope-free-text">Additional scope guidance</Label>
          <Textarea
            id="scope-free-text"
            maxLength={16_384}
            value={value.scope_policy.free_text}
            placeholder="Prioritize request authorization and recovery paths."
            onChange={(event) =>
              set("scope_policy", {
                ...value.scope_policy,
                free_text: event.target.value,
              })
            }
          />
        </div>
        {scopeError && (
          <p role="alert" className="text-destructive text-xs">
            {scopeError}
          </p>
        )}
      </fieldset>

      <fieldset className="space-y-3 rounded-lg border p-4">
        <legend className="px-1 font-medium">
          Models and price comparison
        </legend>
        <p className="text-muted-foreground text-xs">
          Select one or more available aliases. Override prices when the catalog
          is unknown or your contract differs.
        </p>
        {optionsError && (
          <div
            role="alert"
            className="border-destructive/30 bg-destructive/5 text-destructive flex flex-wrap items-center justify-between gap-2 rounded-md border p-3 text-sm"
          >
            <span>
              Model and account options could not be loaded: {optionsError}
            </span>
            <Button
              type="button"
              size="sm"
              variant="outline"
              onClick={onRetryOptions}
            >
              Retry options
            </Button>
          </div>
        )}
        {optionsLoading ? (
          <p className="text-muted-foreground text-sm">Loading models…</p>
        ) : displayModels.length === 0 ? (
          <p className="text-muted-foreground text-sm">
            No model aliases are currently available.
          </p>
        ) : (
          <div className="grid gap-2 md:grid-cols-2">
            {displayModels.map((model) => {
              const checked = value.reviewer_models.includes(model.alias)
              const prices = value.model_prices[model.alias] ?? {
                input_price_per_1m: String(model.input_price_per_1m || ""),
                output_price_per_1m: String(model.output_price_per_1m || ""),
                subscription: model.subscription,
                equivalent_model: model.equivalent_model,
                price_known: model.price_known,
              }
              return (
                <div
                  key={model.alias}
                  className="space-y-3 rounded-lg border p-3"
                >
                  <label className="flex items-start gap-2">
                    <input
                      type="checkbox"
                      className="mt-1"
                      checked={checked}
                      disabled={!model.available && !checked}
                      onChange={(event) => {
                        const reviewerModels = event.target.checked
                          ? [...value.reviewer_models, model.alias]
                          : value.reviewer_models.filter(
                              (alias) => alias !== model.alias,
                            )
                        const modelPrices = { ...value.model_prices }
                        if (event.target.checked) {
                          modelPrices[model.alias] = prices
                        } else {
                          delete modelPrices[model.alias]
                        }
                        onChange({
                          ...value,
                          reviewer_models: reviewerModels,
                          model_prices: modelPrices,
                          compare_models: reviewerModels.length > 1,
                          budget: {
                            ...value.budget,
                            max_estimated_cost_usd: reviewerModels.every(
                              (alias) => billableModelPrice(modelPrices[alias]),
                            )
                              ? value.budget.max_estimated_cost_usd
                              : 0,
                          },
                        })
                      }}
                    />
                    <span className="min-w-0">
                      <span className="block font-medium">{model.alias}</span>
                      <span className="text-muted-foreground block truncate text-xs">
                        {model.provider} · {model.resolved_model}
                        {model.equivalent_model
                          ? ` · equivalent ${model.equivalent_model}`
                          : ""}
                        {!model.available && model.blocked_reason
                          ? ` · ${model.blocked_reason}`
                          : ""}
                      </span>
                    </span>
                    {!model.available && (
                      <Badge className="ml-auto" variant="outline">
                        unavailable
                      </Badge>
                    )}
                    {model.available && !model.price_known && (
                      <Badge className="ml-auto" variant="outline">
                        price unknown
                      </Badge>
                    )}
                  </label>
                  {checked && (
                    <div className="grid grid-cols-2 gap-2">
                      <PriceField
                        id={`input-price-${model.alias}`}
                        label="Input / 1M ($)"
                        value={prices.input_price_per_1m}
                        onChange={(next) =>
                          set("model_prices", {
                            ...value.model_prices,
                            [model.alias]: {
                              ...prices,
                              input_price_per_1m: next,
                            },
                          })
                        }
                      />
                      <PriceField
                        id={`output-price-${model.alias}`}
                        label="Output / 1M ($)"
                        value={prices.output_price_per_1m}
                        onChange={(next) =>
                          set("model_prices", {
                            ...value.model_prices,
                            [model.alias]: {
                              ...prices,
                              output_price_per_1m: next,
                            },
                          })
                        }
                      />
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        )}
        {unavailableSelectedModel && (
          <p role="alert" className="text-destructive text-xs">
            Remove unavailable selected models before saving this profile.
          </p>
        )}
        <SwitchRow
          id="compare-models"
          label="Model comparison"
          description="Enabled automatically when multiple models are selected; use one model for a normal review."
          checked={value.reviewer_models.length > 1}
          disabled
          onCheckedChange={() => undefined}
        />
      </fieldset>

      <fieldset className="grid gap-4 rounded-lg border p-4 sm:grid-cols-2 lg:grid-cols-4">
        <legend className="px-1 font-medium">Work sizing</legend>
        <NumberField
          id="max-files"
          label="Files per run"
          min={1}
          max={100_000}
          value={value.max_files_per_run}
          onChange={(next) => set("max_files_per_run", next)}
        />
        <NumberField
          id="max-content"
          label="Content bytes"
          min={1}
          max={524_288}
          value={value.max_content_bytes}
          onChange={(next) => set("max_content_bytes", next)}
        />
        <NumberField
          id="max-parallel"
          label="Parallel reviewers"
          min={1}
          max={formHasActiveGuardrails(value) ? 1 : 64}
          disabled={formHasActiveGuardrails(value)}
          value={
            formHasActiveGuardrails(value) ? 1 : value.max_parallel_children
          }
          onChange={(next) => set("max_parallel_children", next)}
        />
        <NumberField
          id="estimated-output"
          label="Est. output tokens"
          min={1}
          max={65_536}
          value={value.estimated_output_tokens}
          onChange={(next) => set("estimated_output_tokens", next)}
        />
        <p className="text-muted-foreground text-xs sm:col-span-2 lg:col-span-4">
          Token and account stops take effect after in-flight responses reach a
          safe checkpoint. Active guardrails enforce one reviewer request at a
          time, bounding overshoot to one provider response.
        </p>
        <div className="space-y-3 sm:col-span-2 lg:col-span-4">
          <SwitchRow
            id="auto-continue"
            label="Automatically continue batches"
            description="Queue the next batch while budget and account checks pass."
            checked={value.auto_continue}
            onCheckedChange={(checked) => set("auto_continue", checked)}
          />
          <SwitchRow
            id="force-review"
            label="Force re-review"
            description="Review unchanged files again instead of using the durable ledger."
            checked={value.force}
            onCheckedChange={(checked) => set("force", checked)}
          />
        </div>
      </fieldset>

      <fieldset className="space-y-4 rounded-lg border p-4">
        <legend className="px-1 font-medium">
          Spend and account guardrails
        </legend>
        <p className="text-muted-foreground text-xs">
          The default remaining percentage is a floor. Daily, weekly, and custom
          windows can only make that floor stricter.
        </p>
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <NumberField
            id="max-total-tokens"
            label="Maximum total tokens"
            min={0}
            max={1_000_000_000_000}
            value={value.budget.max_total_tokens}
            onChange={(next) => setBudget("max_total_tokens", next)}
          />
          <NumberField
            id="max-cost"
            label="Maximum estimated cost ($)"
            min={0}
            max={1_000_000_000}
            step="0.01"
            disabled={!costBudgetAvailable}
            value={value.budget.max_estimated_cost_usd}
            onChange={(next) => setBudget("max_estimated_cost_usd", next)}
          />
          {!costBudgetAvailable && value.reviewer_models.length > 0 && (
            <p className="text-muted-foreground self-end text-xs lg:col-span-3">
              The cost guardrail is disabled until every selected model has a
              positive input or output price. Token and account guardrails still
              apply.
            </p>
          )}
          <NumberField
            id="default-threshold"
            label="Default remaining (%)"
            min={0}
            max={100}
            disabled={value.budget.account_ids.length === 0}
            value={value.budget.min_remaining_percent}
            onChange={(next) =>
              onChange({
                ...value,
                budget: {
                  ...value.budget,
                  min_remaining_percent: next,
                  min_remaining_percent_by_window: Object.fromEntries(
                    Object.entries(
                      value.budget.min_remaining_percent_by_window,
                    ).map(([window, percent]) => [
                      window,
                      Math.max(next, percent),
                    ]),
                  ),
                },
              })
            }
          />
          <NumberField
            id="daily-threshold"
            label="Daily remaining (%)"
            min={value.budget.min_remaining_percent}
            max={100}
            disabled={value.budget.account_ids.length === 0}
            value={
              value.budget.min_remaining_percent_by_window.daily ??
              value.budget.min_remaining_percent
            }
            onChange={(next) =>
              setBudget("min_remaining_percent_by_window", {
                ...value.budget.min_remaining_percent_by_window,
                daily: next,
              })
            }
          />
          <NumberField
            id="weekly-threshold"
            label="Weekly remaining (%)"
            min={value.budget.min_remaining_percent}
            max={100}
            disabled={value.budget.account_ids.length === 0}
            value={
              value.budget.min_remaining_percent_by_window.weekly ??
              value.budget.min_remaining_percent
            }
            onChange={(next) =>
              setBudget("min_remaining_percent_by_window", {
                ...value.budget.min_remaining_percent_by_window,
                weekly: next,
              })
            }
          />
          <NumberField
            id="check-interval"
            label="Quota check interval (seconds)"
            min={15}
            max={3_600}
            disabled={value.budget.account_ids.length === 0}
            value={value.budget.check_interval_seconds}
            onChange={(next) => setBudget("check_interval_seconds", next)}
          />
          {customWindowKeys.map((window, index) => (
            <div key={window} className="space-y-2">
              <NumberField
                id={`window-threshold-${fieldID(window)}-${index}`}
                label={`${window} remaining (%)`}
                min={value.budget.min_remaining_percent}
                max={100}
                disabled={value.budget.account_ids.length === 0}
                value={
                  value.budget.min_remaining_percent_by_window[window] ??
                  value.budget.min_remaining_percent
                }
                onChange={(next) =>
                  setBudget("min_remaining_percent_by_window", {
                    ...value.budget.min_remaining_percent_by_window,
                    [window]: next,
                  })
                }
              />
              {Object.hasOwn(
                value.budget.min_remaining_percent_by_window,
                window,
              ) && (
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  disabled={value.budget.account_ids.length === 0}
                  aria-label={`Remove ${window} limit window`}
                  onClick={() => {
                    const windows = {
                      ...value.budget.min_remaining_percent_by_window,
                    }
                    delete windows[window]
                    setBudget("min_remaining_percent_by_window", windows)
                  }}
                >
                  Remove window
                </Button>
              )}
            </div>
          ))}
        </div>

        <div className="flex flex-wrap items-end gap-2">
          <div className="min-w-52 flex-1 space-y-2">
            <Label htmlFor="new-limit-window">Another limit window</Label>
            <Input
              id="new-limit-window"
              maxLength={64}
              disabled={value.budget.account_ids.length === 0}
              value={newWindow}
              placeholder="monthly, 5h, premium"
              onChange={(event) => setNewWindow(event.target.value)}
            />
          </div>
          <Button
            type="button"
            variant="outline"
            disabled={
              value.budget.account_ids.length === 0 ||
              !newWindow.trim() ||
              Object.keys(value.budget.min_remaining_percent_by_window)
                .length >= 32
            }
            onClick={() => {
              const window = newWindow.trim().toLowerCase()
              setBudget("min_remaining_percent_by_window", {
                ...value.budget.min_remaining_percent_by_window,
                [window]:
                  value.budget.min_remaining_percent_by_window[window] ??
                  value.budget.min_remaining_percent,
              })
              setNewWindow("")
            }}
          >
            Add window
          </Button>
        </div>

        <div>
          <p className="mb-2 text-sm font-medium">Accounts to monitor</p>
          {limitsError && (
            <p
              role="alert"
              className="border-destructive/30 bg-destructive/5 text-destructive mb-2 rounded-md border p-2 text-xs"
            >
              Account limit telemetry is unavailable: {limitsError}. Reviews can
              still run with account guardrails disabled.
            </p>
          )}
          {displayAccounts.length === 0 ? (
            <p className="text-muted-foreground text-sm">
              No limit-aware accounts are connected.
            </p>
          ) : (
            <div className="grid gap-2 sm:grid-cols-2">
              {displayAccounts.map((account) => (
                <label
                  key={account.id}
                  htmlFor={`review-account-${account.id}`}
                  className="flex items-center gap-2 rounded-md border p-2"
                >
                  <span className="sr-only">{account.label}</span>
                  <input
                    id={`review-account-${account.id}`}
                    type="checkbox"
                    checked={value.budget.account_ids.includes(account.id)}
                    onChange={(event) => {
                      const accountIDs = event.target.checked
                        ? [...value.budget.account_ids, account.id]
                        : value.budget.account_ids.filter(
                            (id) => id !== account.id,
                          )
                      const enabling =
                        accountIDs.length > 0 &&
                        value.budget.account_ids.length === 0
                      onChange({
                        ...value,
                        budget: {
                          ...value.budget,
                          account_ids: accountIDs,
                          min_remaining_percent:
                            accountIDs.length === 0
                              ? 0
                              : enabling
                                ? 10
                                : value.budget.min_remaining_percent,
                          min_remaining_percent_by_window:
                            accountIDs.length === 0
                              ? {}
                              : enabling
                                ? { daily: 15, weekly: 10 }
                                : value.budget.min_remaining_percent_by_window,
                          pause_on_unknown:
                            accountIDs.length > 0 &&
                            (enabling ? true : value.budget.pause_on_unknown),
                        },
                      })
                    }}
                  />
                  <span>
                    <span className="block font-medium">{account.label}</span>
                    <span className="text-muted-foreground text-xs">
                      {account.provider} · {account.status}
                    </span>
                  </span>
                </label>
              ))}
            </div>
          )}
        </div>

        <div className="grid gap-3 sm:grid-cols-2">
          <SwitchRow
            id="pause-unknown"
            label="Fail closed on unknown limits"
            description="Pause if an account cannot report a reliable remaining balance."
            checked={value.budget.pause_on_unknown}
            disabled={value.budget.account_ids.length === 0}
            onCheckedChange={(checked) =>
              setBudget("pause_on_unknown", checked)
            }
          />
          <SwitchRow
            id="auto-resume"
            label="Automatically resume"
            description="Retry paused work when account limits recover above every threshold."
            checked={value.budget.auto_resume}
            onCheckedChange={(checked) => setBudget("auto_resume", checked)}
          />
        </div>
      </fieldset>

      {error && (
        <div
          role="alert"
          className="border-destructive/30 bg-destructive/5 text-destructive rounded-md border p-3 text-sm"
        >
          {error}
        </div>
      )}
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <Button
          type="submit"
          disabled={
            busy ||
            value.reviewer_models.length === 0 ||
            unavailableSelectedModel ||
            Boolean(scopeError) ||
            !value.name.trim() ||
            !value.repository.trim()
          }
        >
          {busy ? "Saving…" : "Save review profile"}
        </Button>
      </DialogFooter>
    </form>
  )
}

function AutomationCard({
  automation,
  busy,
  onAction,
  onEdit,
  onDelete,
}: {
  automation: RepositoryReviewAutomation
  busy: boolean
  onAction: (
    action: "start" | "pause" | "resume" | "restart",
    resetBudget?: boolean,
  ) => void
  onEdit: () => void
  onDelete: () => void
}) {
  const handoffQueued = isQueuedHandoff(automation)
  const running =
    automation.status === "running" ||
    automation.status === "stopping" ||
    handoffQueued
  const pausedForBudget =
    automation.pause_reason === "token_budget" ||
    automation.pause_reason === "cost_budget"
  const autoResumeEligible = isAutoResumePause(automation.pause_reason)
  const incompleteCost = automation.model_stats.some(
    (stats) => !modelComparisonPriceKnown(automation.model_prices, stats.model),
  )
  const tokenPercent = budgetPercent(
    automation.usage.total_tokens,
    automation.budget.max_total_tokens,
  )
  const costPercent = budgetPercent(
    automation.estimated_cost_usd,
    automation.budget.max_estimated_cost_usd,
  )

  return (
    <Card size="sm" data-testid={`automation-${automation.id}`}>
      <CardHeader>
        <div className="flex min-w-0 items-start justify-between gap-3">
          <div className="min-w-0">
            <CardTitle className="flex flex-wrap items-center gap-2">
              <span className="truncate">{automation.name}</span>
              {handoffQueued ? (
                <Badge variant="secondary">continuing</Badge>
              ) : (
                <StatusBadge status={automation.status} />
              )}
            </CardTitle>
            <CardDescription className="mt-1 truncate">
              {automation.repository} · {automation.ref || "HEAD"} · target{" "}
              {automation.target}
            </CardDescription>
          </div>
          <div className="flex shrink-0 gap-1">
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label={`Edit ${automation.name}`}
              disabled={running || busy}
              onClick={onEdit}
            >
              <IconEdit />
            </Button>
            <DeleteButton
              name={automation.name}
              disabled={running || busy}
              onDelete={onDelete}
            />
          </div>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          <SmallMetric
            label="Stage"
            value={automation.progress.stage || "waiting"}
          />
          <SmallMetric
            label="Batches"
            value={`${automation.progress.completed_batches}/${automation.progress.total_batches || "?"}`}
          />
          <SmallMetric
            label="Reviewed"
            value={automation.progress.reviewed_files}
          />
          <SmallMetric label="Findings" value={automation.progress.findings} />
        </div>

        {automation.scope_plan?.summary && (
          <div className="border-border bg-muted/30 rounded-lg border p-3 text-sm">
            <p className="font-medium">Scope preflight</p>
            <p className="mt-1">{automation.scope_plan.summary}</p>
            <p className="text-muted-foreground mt-1 text-xs">
              Commit {automation.scope_plan.commit_sha} · selected{" "}
              {automation.scope_plan.counts.selected_files} of{" "}
              {automation.scope_plan.counts.total_files} files
            </p>
            {automation.scope_plan.rationale && (
              <p className="text-muted-foreground mt-2 text-xs">
                {automation.scope_plan.rationale}
              </p>
            )}
            {automation.scope_plan.warnings.length > 0 && (
              <ul className="text-warning-foreground mt-2 list-disc space-y-1 pl-4 text-xs">
                {automation.scope_plan.warnings.map((warning) => (
                  <li key={warning}>{warning}</li>
                ))}
              </ul>
            )}
          </div>
        )}

        <div className="space-y-3">
          <BudgetBar
            label="Tokens"
            value={`${formatInteger(automation.usage.total_tokens)} / ${formatLimit(automation.budget.max_total_tokens)}`}
            percent={tokenPercent}
          />
          <BudgetBar
            label={
              incompleteCost ? "Estimated cost (partial)" : "Estimated cost"
            }
            value={`${formatMoney(automation.estimated_cost_usd)} / ${formatMoneyLimit(automation.budget.max_estimated_cost_usd)}`}
            percent={costPercent}
          />
        </div>

        {(automation.status === "paused" || automation.status === "failed") && (
          <div className="border-border bg-muted/40 rounded-lg border p-3 text-sm">
            <p className="font-medium">
              {automation.status === "failed" ? "Failed" : "Paused"}:{" "}
              {pauseReasonLabel(automation.pause_reason)}
            </p>
            {automation.pause_detail && (
              <p className="text-muted-foreground mt-1">
                {automation.pause_detail}
              </p>
            )}
            {automation.status === "paused" &&
              automation.budget.auto_resume &&
              autoResumeEligible && (
                <p className="text-muted-foreground mt-2 text-xs">
                  Auto-resume is enabled
                  {automation.next_check_at
                    ? ` · next account check ${formatTimestamp(automation.next_check_at)}`
                    : "."}
                </p>
              )}
          </div>
        )}

        <div className="text-muted-foreground flex flex-wrap gap-x-3 gap-y-1 text-xs">
          <span>{automation.progress.remaining_files} files remaining</span>
          <span>{automation.progress.unsupported_files} unsupported</span>
          <span>{automation.scope_policy.code_types.join(", ")}</span>
          <span>{automation.reviewer_models.join(", ")}</span>
          {automation.active_run_id && (
            <a
              href={`/agent/workflows?mode=operate&run=${encodeURIComponent(automation.active_run_id)}`}
              className="text-primary underline underline-offset-2"
            >
              Active run {automation.active_run_id}
            </a>
          )}
        </div>

        <div className="text-muted-foreground flex flex-wrap gap-x-3 gap-y-1 text-xs">
          {automation.started_at && (
            <span>Started {formatTimestamp(automation.started_at)}</span>
          )}
          {automation.completed_at && (
            <span>Completed {formatTimestamp(automation.completed_at)}</span>
          )}
          <span>Updated {formatTimestamp(automation.updated_at)}</span>
        </div>
        {automation.run_ids.length > 0 && (
          <details className="rounded-lg border p-3 text-xs">
            <summary className="cursor-pointer font-medium">
              Run history ({automation.run_ids.length})
            </summary>
            <ul className="text-muted-foreground mt-2 space-y-1">
              {automation.run_ids
                .slice()
                .reverse()
                .map((runID) => (
                  <li key={runID}>
                    <a
                      href={`/agent/workflows?mode=operate&run=${encodeURIComponent(runID)}`}
                      className="text-primary break-all underline underline-offset-2"
                    >
                      {runID}
                    </a>
                  </li>
                ))}
            </ul>
          </details>
        )}

        {automation.compare_models && automation.model_stats.length > 0 && (
          <ModelComparison
            stats={automation.model_stats}
            prices={automation.model_prices}
          />
        )}
        {automation.account_limits.length > 0 && (
          <AccountSnapshots accounts={automation.account_limits} />
        )}

        <div className="flex flex-wrap gap-2 border-t pt-4">
          {automation.status === "idle" && !handoffQueued && (
            <Button
              type="button"
              size="sm"
              disabled={busy}
              onClick={() => onAction("start")}
            >
              <IconPlayerPlay /> Start review
            </Button>
          )}
          {(automation.status === "completed" ||
            automation.status === "failed") && (
            <RestartButton
              name={automation.name}
              disabled={busy}
              onRestart={() => onAction("restart")}
            />
          )}
          {automation.status === "running" && (
            <PauseButton
              name={automation.name}
              disabled={busy}
              onPause={() => onAction("pause")}
            />
          )}
          {automation.status === "stopping" && (
            <Button type="button" size="sm" variant="outline" disabled>
              Stopping safely…
            </Button>
          )}
          {automation.status === "paused" && (
            <>
              {!pausedForBudget && (
                <Button
                  type="button"
                  size="sm"
                  disabled={busy}
                  onClick={() => onAction("resume")}
                >
                  <IconPlayerPlay /> Resume
                </Button>
              )}
              {pausedForBudget && (
                <AlertDialog>
                  <AlertDialogTrigger asChild>
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      disabled={busy}
                    >
                      Resume and reset budget
                    </Button>
                  </AlertDialogTrigger>
                  <AlertDialogContent>
                    <AlertDialogHeader>
                      <AlertDialogTitle>
                        Reset accumulated budget?
                      </AlertDialogTitle>
                      <AlertDialogDescription>
                        This clears the profile&apos;s accumulated token and
                        cost counters before admitting more work. Provider
                        account limits still apply.
                      </AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                      <AlertDialogCancel>Keep budget</AlertDialogCancel>
                      <AlertDialogAction
                        onClick={() => onAction("resume", true)}
                      >
                        Reset and resume
                      </AlertDialogAction>
                    </AlertDialogFooter>
                  </AlertDialogContent>
                </AlertDialog>
              )}
              <RestartButton
                name={automation.name}
                disabled={busy}
                onRestart={() => onAction("restart")}
              />
            </>
          )}
        </div>
      </CardContent>
    </Card>
  )
}

function ModelComparison({
  stats,
  prices,
}: {
  stats: RepositoryReviewModelStats[]
  prices: RepositoryReviewAutomation["model_prices"]
}) {
  const successfulKnown = stats.filter(
    (row) =>
      modelComparisonPriceKnown(prices, row.model) &&
      Math.max(0, row.requests - row.failures) > 0,
  )
  const cheapest = successfulKnown.reduce<
    RepositoryReviewModelStats | undefined
  >(
    (best, row) => (!best || compareModelValue(row, best) < 0 ? row : best),
    undefined,
  )
  return (
    <div className="overflow-x-auto rounded-lg border">
      <table className="w-full min-w-[48rem] text-left text-xs">
        <caption className="border-b px-3 py-2 text-left font-medium">
          Model comparison
        </caption>
        <thead className="bg-muted/50 text-muted-foreground">
          <tr>
            <th className="px-3 py-2 font-medium">Model</th>
            <th className="px-3 py-2 font-medium">Tokens</th>
            <th className="px-3 py-2 font-medium">Cost</th>
            <th className="px-3 py-2 font-medium">Requests / failures</th>
            <th className="px-3 py-2 font-medium">
              Files (approx.) / findings
            </th>
            <th className="px-3 py-2 font-medium">Avg. latency</th>
            <th className="px-3 py-2 font-medium">Cost / finding</th>
          </tr>
        </thead>
        <tbody>
          {stats.map((row) => {
            const isCheapest = cheapest?.model === row.model
            const priceKnown = modelComparisonPriceKnown(prices, row.model)
            return (
              <tr
                key={row.model}
                className={isCheapest ? "bg-emerald-500/10" : "border-t"}
              >
                <td className="px-3 py-2 font-medium">
                  {row.model}{" "}
                  {isCheapest && (
                    <Badge variant="outline" className="text-emerald-700">
                      cheapest
                    </Badge>
                  )}
                </td>
                <td className="px-3 py-2">
                  <span className="block">
                    {formatInteger(row.total_tokens)}
                  </span>
                  <span className="text-muted-foreground block text-[0.68rem]">
                    {formatInteger(row.prompt_tokens)} in ·{" "}
                    {formatInteger(row.completion_tokens)} out
                    {row.cached_tokens > 0
                      ? ` · ${formatInteger(row.cached_tokens)} cached`
                      : ""}
                  </span>
                </td>
                <td className="px-3 py-2">
                  {priceKnown ? formatMoney(row.estimated_cost_usd) : "unknown"}
                </td>
                <td className="px-3 py-2">
                  {row.requests} / {row.failures}
                </td>
                <td className="px-3 py-2">
                  {row.reviewed_files} / {row.findings}
                </td>
                <td className="px-3 py-2">
                  {row.requests > 0
                    ? formatDuration(row.latency_ms / row.requests)
                    : "—"}
                </td>
                <td className="px-3 py-2">
                  {priceKnown && row.findings > 0
                    ? formatMoney(row.estimated_cost_usd / row.findings)
                    : "—"}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

function modelComparisonPriceKnown(
  prices: RepositoryReviewAutomation["model_prices"],
  model: string,
): boolean {
  const price = prices[model]
  return Boolean(
    price && (price.input_price_per_1m > 0 || price.output_price_per_1m > 0),
  )
}

function AccountSnapshots({
  accounts,
}: {
  accounts: RepositoryReviewAutomation["account_limits"]
}) {
  return (
    <details className="rounded-lg border p-3">
      <summary className="cursor-pointer font-medium">Account limits</summary>
      <div className="mt-3 grid gap-2 sm:grid-cols-2">
        {accounts.map((account) => (
          <div key={account.id} className="bg-muted/30 rounded-md p-2 text-xs">
            <p className="font-medium">
              {account.label || account.id} · {account.status}
            </p>
            <ul className="text-muted-foreground mt-1 space-y-1">
              {account.entries.map((entry, index) => (
                <li key={`${entry.window || "default"}-${index}`}>
                  {entry.label || entry.window || "Limit"}:{" "}
                  {remainingLabel(entry)}
                  {entry.reset_at
                    ? ` · resets ${formatTimestamp(entry.reset_at)}`
                    : ""}
                </li>
              ))}
            </ul>
            {(account.refreshed_at || account.entries[0]?.refreshed_at) && (
              <p className="text-muted-foreground mt-2">
                Refreshed{" "}
                {formatTimestamp(
                  account.refreshed_at || account.entries[0].refreshed_at || "",
                )}
              </p>
            )}
          </div>
        ))}
      </div>
    </details>
  )
}

function PauseButton({
  name,
  disabled,
  onPause,
}: {
  name: string
  disabled: boolean
  onPause: () => void
}) {
  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        <Button type="button" size="sm" variant="outline" disabled={disabled}>
          <IconPlayerPause /> Pause safely
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Pause {name}?</AlertDialogTitle>
          <AlertDialogDescription>
            No new review batches will start. In-flight work is allowed to reach
            a safe checkpoint before the profile becomes paused.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Keep running</AlertDialogCancel>
          <AlertDialogAction onClick={onPause}>Pause review</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

function RestartButton({
  name,
  disabled,
  onRestart,
}: {
  name: string
  disabled: boolean
  onRestart: () => void
}) {
  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        <Button type="button" size="sm" variant="outline" disabled={disabled}>
          <IconRotateClockwise /> Restart
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Restart {name}?</AlertDialogTitle>
          <AlertDialogDescription>
            Restarting begins a new campaign and resets accumulated token, cost,
            progress, and model comparison counters. Account guardrails are
            checked again before work starts.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Keep current campaign</AlertDialogCancel>
          <AlertDialogAction onClick={onRestart}>
            Reset and restart
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

function DeleteButton({
  name,
  disabled,
  onDelete,
}: {
  name: string
  disabled: boolean
  onDelete: () => void
}) {
  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          aria-label={`Delete ${name}`}
          disabled={disabled}
        >
          <IconTrash />
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete {name}?</AlertDialogTitle>
          <AlertDialogDescription>
            The persistent profile and its controller history will be removed.
            Completed repository findings are not deleted.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction variant="destructive" onClick={onDelete}>
            Delete profile
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

function StatusBadge({
  status,
}: {
  status: RepositoryReviewAutomation["status"]
}) {
  const variant =
    status === "failed"
      ? "destructive"
      : status === "running"
        ? "default"
        : "secondary"
  return <Badge variant={variant}>{status}</Badge>
}

function TextField({
  id,
  label,
  value,
  placeholder,
  required,
  onChange,
}: {
  id: string
  label: string
  value: string
  placeholder?: string
  required?: boolean
  onChange: (value: string) => void
}) {
  return (
    <div className="space-y-2">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        required={required}
        value={value}
        placeholder={placeholder}
        onChange={(event) => onChange(event.target.value)}
      />
    </div>
  )
}

function NumberField({
  id,
  label,
  value,
  min,
  max,
  step,
  disabled,
  onChange,
}: {
  id: string
  label: string
  value: number
  min?: number
  max?: number
  step?: string
  disabled?: boolean
  onChange: (value: number) => void
}) {
  return (
    <div className="space-y-2">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        type="number"
        min={min}
        max={max}
        step={step ?? "1"}
        disabled={disabled}
        value={value}
        onChange={(event) => onChange(Number(event.target.value) || 0)}
      />
    </div>
  )
}

function PriceField({
  id,
  label,
  value,
  onChange,
}: {
  id: string
  label: string
  value: string
  onChange: (value: string) => void
}) {
  return (
    <div className="space-y-1">
      <Label className="text-xs" htmlFor={id}>
        {label}
      </Label>
      <Input
        id={id}
        type="number"
        min="0"
        max="1000000"
        step="0.0001"
        value={value}
        onChange={(event) => onChange(event.target.value)}
      />
    </div>
  )
}

function SwitchRow({
  id,
  label,
  description,
  checked,
  disabled,
  onCheckedChange,
}: {
  id: string
  label: string
  description: string
  checked: boolean
  disabled?: boolean
  onCheckedChange: (checked: boolean) => void
}) {
  return (
    <div className="flex items-start justify-between gap-4 rounded-md border p-3">
      <div>
        <Label htmlFor={id}>{label}</Label>
        <p className="text-muted-foreground mt-1 text-xs">{description}</p>
      </div>
      <Switch
        id={id}
        checked={checked}
        disabled={disabled}
        aria-label={label}
        onCheckedChange={onCheckedChange}
      />
    </div>
  )
}

function SmallMetric({
  label,
  value,
}: {
  label: string
  value: string | number
}) {
  return (
    <div className="bg-muted/40 rounded-md p-2">
      <span className="text-muted-foreground block text-[0.68rem] tracking-wide uppercase">
        {label}
      </span>
      <span className="mt-1 block truncate font-medium">{value}</span>
    </div>
  )
}

function BudgetBar({
  label,
  value,
  percent,
}: {
  label: string
  value: string
  percent: number | null
}) {
  const widthClass = progressWidthClass(percent)
  return (
    <div>
      <div className="mb-1 flex justify-between gap-3 text-xs">
        <span className="font-medium">{label}</span>
        <span className="text-muted-foreground">{value}</span>
      </div>
      <div
        className="bg-muted h-2 overflow-hidden rounded-full"
        role="progressbar"
        aria-label={`${label} budget used`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percent ?? 0}
      >
        <div className={`bg-primary h-full rounded-full ${widthClass}`} />
      </div>
    </div>
  )
}

function formPayload(value: FormState): RepositoryReviewAutomationConfig {
  const budgetGuarded = formHasActiveGuardrails(value)
  return {
    ...value,
    name: value.name.trim(),
    repository: value.repository.trim(),
    ref: value.ref.trim(),
    target: value.target.trim(),
    review_focus: value.review_focus.trim(),
    scope_policy: {
      code_types: [...value.scope_policy.code_types],
      include_folders: normalizeScopeFolderPrefixes(
        value.scope_policy.include_folders,
      ),
      exclude_folders: normalizeScopeFolderPrefixes(
        value.scope_policy.exclude_folders,
      ),
      free_text: value.scope_policy.free_text.trim(),
    },
    compare_models: value.reviewer_models.length > 1,
    max_parallel_children: budgetGuarded ? 1 : value.max_parallel_children,
    budget: {
      ...value.budget,
      max_estimated_cost_usd: hasBillableModelPrices(value)
        ? value.budget.max_estimated_cost_usd
        : 0,
      min_remaining_percent:
        value.budget.account_ids.length > 0
          ? value.budget.min_remaining_percent
          : 0,
      min_remaining_percent_by_window:
        value.budget.account_ids.length > 0
          ? Object.fromEntries(
              Object.entries(value.budget.min_remaining_percent_by_window).map(
                ([window, percent]) => [
                  window,
                  Math.max(value.budget.min_remaining_percent, percent),
                ],
              ),
            )
          : {},
      pause_on_unknown:
        value.budget.account_ids.length > 0 && value.budget.pause_on_unknown,
    },
    model_prices: Object.fromEntries(
      value.reviewer_models.flatMap((model) => {
        const price = value.model_prices[model]
        const known = billableModelPrice(price) || price?.subscription
        return known
          ? [
              [
                model,
                {
                  input_price_per_1m: Number(price?.input_price_per_1m) || 0,
                  output_price_per_1m: Number(price?.output_price_per_1m) || 0,
                  ...(price?.subscription ? { subscription: true } : {}),
                  ...(price?.equivalent_model
                    ? { equivalent_model: price.equivalent_model }
                    : {}),
                },
              ] as const,
            ]
          : []
      }),
    ),
  }
}

function formHasActiveGuardrails(value: FormState): boolean {
  return (
    value.budget.max_total_tokens > 0 ||
    (hasBillableModelPrices(value) &&
      value.budget.max_estimated_cost_usd > 0) ||
    value.budget.account_ids.length > 0 ||
    value.budget.min_remaining_percent > 0 ||
    Object.values(value.budget.min_remaining_percent_by_window).some(
      (threshold) => threshold > 0,
    ) ||
    value.budget.pause_on_unknown
  )
}

function hasBillableModelPrices(value: FormState): boolean {
  return (
    value.reviewer_models.length > 0 &&
    value.reviewer_models.every((model) =>
      billableModelPrice(value.model_prices[model]),
    )
  )
}

function billableModelPrice(
  price: FormState["model_prices"][string] | undefined,
): boolean {
  return (
    (Number(price?.input_price_per_1m) || 0) > 0 ||
    (Number(price?.output_price_per_1m) || 0) > 0
  )
}

function automationForm(automation: RepositoryReviewAutomation): FormState {
  return {
    ...automation,
    reviewer_models: [...automation.reviewer_models],
    scope_policy: {
      ...automation.scope_policy,
      code_types: [...automation.scope_policy.code_types],
      include_folders: [...automation.scope_policy.include_folders],
      exclude_folders: [...automation.scope_policy.exclude_folders],
    },
    model_prices: Object.fromEntries(
      Object.entries(automation.model_prices).map(([model, price]) => [
        model,
        {
          input_price_per_1m: String(price.input_price_per_1m),
          output_price_per_1m: String(price.output_price_per_1m),
          subscription: price.subscription,
          equivalent_model: price.equivalent_model,
          price_known: true,
        },
      ]),
    ),
    budget: {
      ...automation.budget,
      account_ids: [...automation.budget.account_ids],
      min_remaining_percent_by_window: {
        ...automation.budget.min_remaining_percent_by_window,
      },
    },
  }
}

function copyForm(form: FormState): FormState {
  return {
    ...form,
    reviewer_models: [...form.reviewer_models],
    scope_policy: {
      ...form.scope_policy,
      code_types: [...form.scope_policy.code_types],
      include_folders: [...form.scope_policy.include_folders],
      exclude_folders: [...form.scope_policy.exclude_folders],
    },
    model_prices: { ...form.model_prices },
    budget: {
      ...form.budget,
      account_ids: [...form.budget.account_ids],
      min_remaining_percent_by_window: {
        ...form.budget.min_remaining_percent_by_window,
      },
    },
  }
}

function repositoryReviewScopeError(value: FormState): string {
  if (value.scope_policy.code_types.length === 0) {
    return "Select at least one code type."
  }
  for (const [label, prefixes] of [
    ["include", value.scope_policy.include_folders],
    ["exclude", value.scope_policy.exclude_folders],
  ] as const) {
    const normalized = normalizeScopeFolderPrefixes(prefixes)
    if (normalized.length > 64) {
      return `Use at most 64 ${label} folder prefixes.`
    }
    if (normalized.some((prefix) => !validScopeFolderPrefix(prefix))) {
      return `${label === "include" ? "Include" : "Exclude"} folders must be canonical repository-relative prefixes.`
    }
    if (new Set(normalized).size !== normalized.length) {
      return `${label === "include" ? "Include" : "Exclude"} folder prefixes must be unique.`
    }
  }
  return ""
}

function normalizeScopeFolderPrefixes(prefixes: string[]): string[] {
  return prefixes.map((prefix) => prefix.trim()).filter(Boolean)
}

function validScopeFolderPrefix(prefix: string): boolean {
  if (
    prefix.length === 0 ||
    prefix.length > 1_024 ||
    prefix.startsWith("/") ||
    prefix.endsWith("/") ||
    prefix.includes("\\") ||
    prefix.includes("\0")
  ) {
    return false
  }
  return prefix
    .split("/")
    .every((segment) => segment !== "" && segment !== "." && segment !== "..")
}

function updateCachedAutomation(
  queryClient: ReturnType<typeof useQueryClient>,
  next: RepositoryReviewAutomation,
) {
  queryClient.setQueryData<{ automations: RepositoryReviewAutomation[] }>(
    automationKey,
    (current) => ({
      automations: current?.automations.some(
        (automation) => automation.id === next.id,
      )
        ? current.automations.map((automation) =>
            automation.id === next.id ? next : automation,
          )
        : [next, ...(current?.automations ?? [])],
    }),
  )
}

function budgetPercent(value: number, limit: number): number | null {
  if (limit <= 0) return null
  return Math.min(100, Math.max(0, Math.round((value / limit) * 100)))
}

function compareModelValue(
  left: RepositoryReviewModelStats,
  right: RepositoryReviewModelStats,
): number {
  const leftSuccessful = Math.max(1, left.requests - left.failures)
  const rightSuccessful = Math.max(1, right.requests - right.failures)
  const leftUnitCost =
    left.reviewed_files > 0
      ? left.estimated_cost_usd / left.reviewed_files
      : left.estimated_cost_usd / leftSuccessful
  const rightUnitCost =
    right.reviewed_files > 0
      ? right.estimated_cost_usd / right.reviewed_files
      : right.estimated_cost_usd / rightSuccessful
  if (leftUnitCost !== rightUnitCost) return leftUnitCost - rightUnitCost
  return (
    left.estimated_cost_usd / leftSuccessful -
    right.estimated_cost_usd / rightSuccessful
  )
}

function progressWidthClass(percent: number | null): string {
  if (!percent || percent <= 0) return "w-0"
  if (percent <= 10) return "w-[10%]"
  if (percent <= 20) return "w-[20%]"
  if (percent <= 30) return "w-[30%]"
  if (percent <= 40) return "w-[40%]"
  if (percent <= 50) return "w-1/2"
  if (percent <= 60) return "w-[60%]"
  if (percent <= 70) return "w-[70%]"
  if (percent <= 80) return "w-4/5"
  if (percent <= 90) return "w-[90%]"
  return "w-full"
}

function pauseReasonLabel(
  reason: RepositoryReviewAutomation["pause_reason"],
): string {
  switch (reason) {
    case "manual":
      return "manual checkpoint"
    case "token_budget":
      return "token budget reached"
    case "cost_budget":
      return "cost budget reached"
    case "account_limit":
      return "account limit guardrail"
    case "run_failed":
      return "review run failed"
    case "service_restart":
      return "service restarted"
    default:
      return "guardrail"
  }
}

function isAutoResumePause(
  reason: RepositoryReviewAutomation["pause_reason"],
): boolean {
  return reason === "account_limit" || reason === "service_restart"
}

function isQueuedHandoff(automation: RepositoryReviewAutomation): boolean {
  return (
    automation.status === "idle" &&
    automation.auto_continue &&
    automation.progress.stage.trim().toLowerCase() === "next batch queued"
  )
}

function remainingLabel(entry: ReviewAccountLimitEntry): string {
  if (typeof entry.remaining_percent === "number") {
    return `${entry.remaining_percent.toFixed(1)}% remaining`
  }
  if (typeof entry.used_percent === "number") {
    return `${Math.max(0, 100 - entry.used_percent).toFixed(1)}% remaining`
  }
  return entry.status || "unknown"
}

function formatInteger(value: number): string {
  return new Intl.NumberFormat().format(value)
}

function formatLimit(value: number): string {
  return value > 0 ? formatInteger(value) : "unlimited"
}

function formatMoney(value: number): string {
  return new Intl.NumberFormat(undefined, {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: value < 0.01 ? 4 : 2,
    maximumFractionDigits: value < 0.01 ? 4 : 2,
  }).format(value)
}

function formatMoneyLimit(value: number): string {
  return value > 0 ? formatMoney(value) : "unlimited"
}

function formatDuration(milliseconds: number): string {
  if (milliseconds <= 0) return "—"
  if (milliseconds < 1_000) return `${milliseconds} ms`
  return `${(milliseconds / 1_000).toFixed(1)} s`
}

function formatTimestamp(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

function fieldID(value: string): string {
  return value.replace(/[^a-z0-9_-]+/giu, "-")
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "Review automation failed."
}

function isConflictError(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    "status" in error &&
    error.status === 409
  )
}
