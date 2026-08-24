import {
  IconAlertTriangle,
  IconEdit,
  IconPlus,
  IconRefresh,
  IconTrash,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useState } from "react"

import {
  type RepositoryReviewAutomationBudget,
  type RepositoryReviewCodeType,
  type RepositoryReviewProfile,
  type RepositoryReviewProfileConfig,
  type ReviewModelOption,
  type ReviewModelPrice,
  createRepositoryReviewProfile,
  deleteRepositoryReviewProfile,
  getRepositoryReviewAutomationOptions,
  listRepositoryReviewProfiles,
  updateRepositoryReviewProfile,
} from "@/api/repository-reviews"
import { PageHeader } from "@/components/page-header"
import { ReviewAdvancedSection } from "@/components/repository-reviews/review-advanced-section"
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
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"

const profilesKey = ["repository-review-profiles"] as const
const optionsKey = ["repository-review-automation-options"] as const
const codeTypeOptions: Array<{
  value: RepositoryReviewCodeType
  label: string
}> = [
  { value: "hotpath-code", label: "Hot-path production code" },
  { value: "code", label: "Production code" },
  { value: "test", label: "Tests" },
  { value: "bench-test", label: "Benchmarks" },
]

interface ProfileEditor {
  profile: RepositoryReviewProfile | null
  value: RepositoryReviewProfileConfig
  includeFolders: string
  excludeFolders: string
  windowLimits: string
}

const emptyProfile: RepositoryReviewProfileConfig = {
  name: "",
  reviewer_model: "",
  review_focus: "Find correctness, security, and reliability bugs.",
  model_price: { input_price_per_1m: 0, output_price_per_1m: 0 },
  force: false,
  auto_continue: true,
  max_files_per_run: 24,
  max_content_bytes: 524_288,
  max_parallel_children: 1,
  estimated_output_tokens: 4_096,
  scope_policy: {
    code_types: ["hotpath-code", "code"],
    include_folders: [],
    exclude_folders: [],
    free_text: "",
  },
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

export function RepositoryReviewProfilesPage() {
  const queryClient = useQueryClient()
  const [editor, setEditor] = useState<ProfileEditor | null>(null)
  const [actionError, setActionError] = useState("")
  const profilesQuery = useQuery({
    queryKey: profilesKey,
    queryFn: ({ signal }) => listRepositoryReviewProfiles(signal),
  })
  const optionsQuery = useQuery({
    queryKey: optionsKey,
    queryFn: ({ signal }) => getRepositoryReviewAutomationOptions(signal),
  })
  const profiles = profilesQuery.data?.profiles ?? []
  const options = optionsQuery.data ?? { models: [], accounts: [] }

  const saveMutation = useMutation({
    mutationFn: ({
      profile,
      value,
      includeFolders,
      excludeFolders,
      windowLimits,
    }: ProfileEditor) => {
      const config: RepositoryReviewProfileConfig = {
        ...value,
        name: value.name.trim(),
        review_focus: value.review_focus.trim(),
        scope_policy: {
          ...value.scope_policy,
          include_folders: lines(includeFolders),
          exclude_folders: lines(excludeFolders),
          free_text: value.scope_policy.free_text.trim(),
        },
        budget: {
          ...value.budget,
          min_remaining_percent_by_window: parseWindowLimits(windowLimits),
        },
      }
      if (profileGuardrailsActive(config.budget)) {
        config.max_parallel_children = 1
      }
      return profile
        ? updateRepositoryReviewProfile(profile.id, {
            ...config,
            expected_version: profile.version,
          })
        : createRepositoryReviewProfile(config)
    },
    onSuccess: (saved) => {
      queryClient.setQueryData<{ profiles: RepositoryReviewProfile[] }>(
        profilesKey,
        (current) => ({
          profiles: current?.profiles.some((item) => item.id === saved.id)
            ? current.profiles.map((item) =>
                item.id === saved.id ? saved : item,
              )
            : [saved, ...(current?.profiles ?? [])],
        }),
      )
      setEditor(null)
      setActionError("")
    },
    onError: (error) => {
      setActionError(errorMessage(error))
      void profilesQuery.refetch()
    },
  })
  const deleteMutation = useMutation({
    mutationFn: (profile: RepositoryReviewProfile) =>
      deleteRepositoryReviewProfile(profile.id, {
        expected_version: profile.version,
      }),
    onSuccess: (_result, removed) => {
      queryClient.setQueryData<{ profiles: RepositoryReviewProfile[] }>(
        profilesKey,
        (current) => ({
          profiles: (current?.profiles ?? []).filter(
            (item) => item.id !== removed.id,
          ),
        }),
      )
      setActionError("")
    },
    onError: (error) => {
      setActionError(errorMessage(error))
      void profilesQuery.refetch()
    },
  })

  const openNew = () => {
    const model = options.models.find((item) => item.available)
    const value = copyProfile(emptyProfile)
    if (model) {
      value.reviewer_model = model.alias
      value.model_price = modelPrice(model)
      if (!model.price_known) value.budget.max_estimated_cost_usd = 0
    }
    setEditor({
      profile: null,
      value,
      includeFolders: "",
      excludeFolders: "",
      windowLimits: "",
    })
  }
  const openEdit = (profile: RepositoryReviewProfile) => {
    const value = copyProfile(profile)
    if (!modelPriceBillable(value.model_price)) {
      value.budget.max_estimated_cost_usd = 0
    }
    if (profileGuardrailsActive(value.budget)) {
      value.max_parallel_children = 1
    }
    setEditor({
      profile,
      value,
      includeFolders: profile.scope_policy.include_folders.join("\n"),
      excludeFolders: profile.scope_policy.exclude_folders.join("\n"),
      windowLimits: formatWindowLimits(
        profile.budget.min_remaining_percent_by_window,
      ),
    })
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader title="Review profiles">
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={profilesQuery.isFetching || optionsQuery.isFetching}
          onClick={() => {
            void profilesQuery.refetch()
            void optionsQuery.refetch()
          }}
        >
          <IconRefresh /> Refresh
        </Button>
        <Button type="button" size="sm" onClick={openNew}>
          <IconPlus /> New profile
        </Button>
      </PageHeader>
      <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-8 md:px-6">
        <div className="mx-auto max-w-5xl space-y-4">
          <p className="text-muted-foreground text-sm">
            Reusable review behavior. Every profile selects one reviewer model;
            repository assignment happens separately.
          </p>
          {actionError && (
            <div
              role="alert"
              className="text-destructive flex items-center gap-2 text-sm"
            >
              <IconAlertTriangle className="size-4" /> {actionError}
            </div>
          )}
          {profilesQuery.isPending ? (
            <Empty text="Loading review profiles…" />
          ) : profilesQuery.isError ? (
            <Empty text="Review profiles could not be loaded." />
          ) : profiles.length === 0 ? (
            <Empty text="No review profile yet." />
          ) : (
            <div className="grid gap-4 lg:grid-cols-2">
              {profiles.map((profile) => (
                <Card key={profile.id} size="sm">
                  <CardHeader>
                    <div className="flex items-start justify-between gap-3">
                      <div className="min-w-0">
                        <CardTitle className="truncate">
                          {profile.name}
                        </CardTitle>
                        <CardDescription className="mt-1 font-mono">
                          {profile.reviewer_model}
                        </CardDescription>
                      </div>
                      <Badge variant="secondary">v{profile.version}</Badge>
                    </div>
                  </CardHeader>
                  <CardContent className="space-y-3">
                    <p className="line-clamp-2 text-sm">
                      {profile.review_focus}
                    </p>
                    <div className="text-muted-foreground grid gap-1 text-xs sm:grid-cols-2">
                      <span>
                        {formatLimit(profile.budget.max_total_tokens)} token cap
                      </span>
                      <span>
                        {formatMoney(profile.budget.max_estimated_cost_usd)}{" "}
                        cost cap
                      </span>
                      <span>{profile.max_files_per_run} files per batch</span>
                      <span>{profile.scope_policy.code_types.join(", ")}</span>
                    </div>
                    <div className="flex gap-2 border-t pt-3">
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        disabled={
                          saveMutation.isPending || deleteMutation.isPending
                        }
                        onClick={() => openEdit(profile)}
                      >
                        <IconEdit /> Edit
                      </Button>
                      <AlertDialog>
                        <AlertDialogTrigger asChild>
                          <Button
                            type="button"
                            size="sm"
                            variant="ghost"
                            disabled={
                              saveMutation.isPending || deleteMutation.isPending
                            }
                          >
                            <IconTrash /> Delete
                          </Button>
                        </AlertDialogTrigger>
                        <AlertDialogContent>
                          <AlertDialogHeader>
                            <AlertDialogTitle>
                              Delete {profile.name}?
                            </AlertDialogTitle>
                            <AlertDialogDescription>
                              Assigned profiles cannot be deleted. Reassign any
                              repositories first.
                            </AlertDialogDescription>
                          </AlertDialogHeader>
                          <AlertDialogFooter>
                            <AlertDialogCancel>Cancel</AlertDialogCancel>
                            <AlertDialogAction
                              onClick={() => deleteMutation.mutate(profile)}
                            >
                              Delete profile
                            </AlertDialogAction>
                          </AlertDialogFooter>
                        </AlertDialogContent>
                      </AlertDialog>
                    </div>
                  </CardContent>
                </Card>
              ))}
            </div>
          )}
        </div>
      </div>

      <Dialog
        open={editor !== null}
        onOpenChange={(open) => !open && setEditor(null)}
      >
        <DialogContent className="max-h-[92vh] overflow-y-auto sm:max-w-3xl">
          <DialogHeader>
            <DialogTitle>
              {editor?.profile ? "Edit profile" : "New review profile"}
            </DialogTitle>
            <DialogDescription>
              Basic limits stay visible. Scope, sizing, pricing, and account
              guardrails remain under Advanced until needed.
            </DialogDescription>
          </DialogHeader>
          {editor && (
            <ProfileForm
              editor={editor}
              models={options.models}
              accounts={options.accounts}
              busy={saveMutation.isPending}
              onChange={setEditor}
              onCancel={() => setEditor(null)}
              onSave={() => saveMutation.mutate(editor)}
            />
          )}
        </DialogContent>
      </Dialog>
    </div>
  )
}

function ProfileForm({
  editor,
  models,
  accounts,
  busy,
  onChange,
  onCancel,
  onSave,
}: {
  editor: ProfileEditor
  models: Awaited<
    ReturnType<typeof getRepositoryReviewAutomationOptions>
  >["models"]
  accounts: Awaited<
    ReturnType<typeof getRepositoryReviewAutomationOptions>
  >["accounts"]
  busy: boolean
  onChange: (editor: ProfileEditor) => void
  onCancel: () => void
  onSave: () => void
}) {
  const { value } = editor
  const setValue = <K extends keyof RepositoryReviewProfileConfig>(
    key: K,
    next: RepositoryReviewProfileConfig[K],
  ) => onChange({ ...editor, value: { ...value, [key]: next } })
  const setBudget = <K extends keyof RepositoryReviewProfileConfig["budget"]>(
    key: K,
    next: RepositoryReviewProfileConfig["budget"][K],
  ) => {
    const budget = { ...value.budget, [key]: next }
    onChange({
      ...editor,
      value: {
        ...value,
        budget,
        ...(profileGuardrailsActive(budget)
          ? { max_parallel_children: 1 }
          : {}),
      },
    })
  }
  const setScope = <
    K extends keyof RepositoryReviewProfileConfig["scope_policy"],
  >(
    key: K,
    next: RepositoryReviewProfileConfig["scope_policy"][K],
  ) => setValue("scope_policy", { ...value.scope_policy, [key]: next })
  const valid =
    value.name.trim() !== "" &&
    value.reviewer_model !== "" &&
    value.scope_policy.code_types.length > 0 &&
    value.max_files_per_run >= 1 &&
    value.max_content_bytes >= 1 &&
    value.max_parallel_children >= 1 &&
    value.estimated_output_tokens >= 1 &&
    value.budget.check_interval_seconds >= 15
  const costBudgetAvailable = modelPriceBillable(value.model_price)
  const guardrailsActive = profileGuardrailsActive({
    ...value.budget,
    min_remaining_percent_by_window: parseWindowLimits(editor.windowLimits),
  })

  return (
    <div className="space-y-5">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Profile name">
          <Input
            aria-label="Profile name"
            value={value.name}
            onChange={(event) => setValue("name", event.target.value)}
          />
        </Field>
        <Field label="Reviewer model">
          <select
            aria-label="Reviewer model"
            className="border-input bg-background h-9 w-full rounded-md border px-3 text-sm"
            value={value.reviewer_model}
            onChange={(event) => {
              const model = models.find(
                (item) => item.alias === event.target.value,
              )
              onChange({
                ...editor,
                value: {
                  ...value,
                  reviewer_model: event.target.value,
                  model_price: modelPrice(model),
                  budget: {
                    ...value.budget,
                    ...(model?.price_known
                      ? {}
                      : { max_estimated_cost_usd: 0 }),
                  },
                },
              })
            }}
          >
            <option value="">Select model</option>
            {models.map((model) => (
              <option
                key={model.alias}
                value={model.alias}
                disabled={!model.available}
              >
                {model.alias}
                {model.available ? "" : " (unavailable)"}
              </option>
            ))}
          </select>
        </Field>
      </div>
      <Field label="Review focus">
        <Textarea
          aria-label="Review focus"
          value={value.review_focus}
          onChange={(event) => setValue("review_focus", event.target.value)}
        />
      </Field>
      <div className="grid gap-4 sm:grid-cols-2">
        <NumberField
          label="Maximum total tokens"
          max={1_000_000_000_000}
          value={value.budget.max_total_tokens}
          onChange={(next) => setBudget("max_total_tokens", next)}
        />
        <NumberField
          label="Maximum estimated cost ($)"
          value={value.budget.max_estimated_cost_usd}
          step="0.01"
          max={1_000_000_000}
          disabled={!costBudgetAvailable}
          onChange={(next) => setBudget("max_estimated_cost_usd", next)}
        />
      </div>

      <ReviewAdvancedSection description="scope, sizing, pricing, and quotas">
        <section className="space-y-3">
          <h3 className="text-sm font-semibold">Scope</h3>
          <div className="flex flex-wrap gap-2">
            {codeTypeOptions.map((item) => {
              const checked = value.scope_policy.code_types.includes(item.value)
              return (
                <label
                  key={item.value}
                  className="flex items-center gap-2 rounded-md border px-3 py-2 text-sm"
                >
                  <input
                    type="checkbox"
                    checked={checked}
                    onChange={() =>
                      setScope(
                        "code_types",
                        checked
                          ? value.scope_policy.code_types.filter(
                              (candidate) => candidate !== item.value,
                            )
                          : [...value.scope_policy.code_types, item.value],
                      )
                    }
                  />
                  {item.label}
                </label>
              )
            })}
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Include folders">
              <Textarea
                aria-label="Include folders"
                value={editor.includeFolders}
                onChange={(event) =>
                  onChange({ ...editor, includeFolders: event.target.value })
                }
                placeholder="cmd\ninternal/review"
              />
            </Field>
            <Field label="Exclude folders">
              <Textarea
                aria-label="Exclude folders"
                value={editor.excludeFolders}
                onChange={(event) =>
                  onChange({ ...editor, excludeFolders: event.target.value })
                }
                placeholder="generated\ntestdata"
              />
            </Field>
          </div>
          <Field label="Additional scope guidance">
            <Textarea
              aria-label="Additional scope guidance"
              value={value.scope_policy.free_text}
              onChange={(event) => setScope("free_text", event.target.value)}
            />
          </Field>
        </section>

        <section className="space-y-3 border-t pt-4">
          <h3 className="text-sm font-semibold">Work sizing</h3>
          <div className="grid gap-4 sm:grid-cols-2">
            <NumberField
              label="Files per batch"
              min={1}
              max={100_000}
              value={value.max_files_per_run}
              onChange={(next) => setValue("max_files_per_run", next)}
            />
            <NumberField
              label="Content bytes per batch"
              min={1}
              max={524_288}
              value={value.max_content_bytes}
              onChange={(next) => setValue("max_content_bytes", next)}
            />
            <NumberField
              label="Parallel review workers"
              min={1}
              max={64}
              disabled={guardrailsActive}
              value={guardrailsActive ? 1 : value.max_parallel_children}
              onChange={(next) => setValue("max_parallel_children", next)}
            />
            <NumberField
              label="Estimated output tokens"
              min={1}
              max={65_536}
              value={value.estimated_output_tokens}
              onChange={(next) => setValue("estimated_output_tokens", next)}
            />
          </div>
          <div className="flex flex-wrap gap-5 text-sm">
            <Check
              label="Continue through batches"
              checked={value.auto_continue}
              onChange={(next) => setValue("auto_continue", next)}
            />
            <Check
              label="Force re-review unchanged files"
              checked={value.force}
              onChange={(next) => setValue("force", next)}
            />
          </div>
        </section>

        <section className="space-y-3 border-t pt-4">
          <h3 className="text-sm font-semibold">Model price metadata</h3>
          <div className="grid gap-4 sm:grid-cols-2">
            <NumberField
              label="Input price / 1M ($)"
              max={1_000_000}
              value={value.model_price.input_price_per_1m}
              step="0.0001"
              onChange={(next) =>
                setModelPrice(
                  editor,
                  { ...value.model_price, input_price_per_1m: next },
                  onChange,
                )
              }
            />
            <NumberField
              label="Output price / 1M ($)"
              max={1_000_000}
              value={value.model_price.output_price_per_1m}
              step="0.0001"
              onChange={(next) =>
                setModelPrice(
                  editor,
                  { ...value.model_price, output_price_per_1m: next },
                  onChange,
                )
              }
            />
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <Check
              label="Subscription-backed model"
              checked={value.model_price.subscription ?? false}
              onChange={(next) =>
                setValue("model_price", {
                  ...value.model_price,
                  subscription: next,
                })
              }
            />
            <Field label="Equivalent model">
              <Input
                aria-label="Equivalent model"
                value={value.model_price.equivalent_model ?? ""}
                onChange={(event) =>
                  setValue("model_price", {
                    ...value.model_price,
                    equivalent_model: event.target.value,
                  })
                }
              />
            </Field>
          </div>
        </section>

        <section className="space-y-3 border-t pt-4">
          <h3 className="text-sm font-semibold">
            Accounts and quota guardrails
          </h3>
          {accounts.length === 0 ? (
            <p className="text-muted-foreground text-xs">
              No account telemetry available.
            </p>
          ) : (
            <div className="grid gap-2 sm:grid-cols-2">
              {accounts.map((account) => (
                <Check
                  key={account.id}
                  label={`${account.label || account.id} · ${account.status}`}
                  checked={value.budget.account_ids.includes(account.id)}
                  onChange={(checked) =>
                    setBudget(
                      "account_ids",
                      checked
                        ? [...value.budget.account_ids, account.id]
                        : value.budget.account_ids.filter(
                            (id) => id !== account.id,
                          ),
                    )
                  }
                />
              ))}
            </div>
          )}
          <div className="grid gap-4 sm:grid-cols-2">
            <NumberField
              label="Minimum remaining (%)"
              max={100}
              value={value.budget.min_remaining_percent}
              onChange={(next) => setBudget("min_remaining_percent", next)}
            />
            <NumberField
              label="Account check interval (seconds)"
              min={15}
              max={3_600}
              value={value.budget.check_interval_seconds}
              onChange={(next) => setBudget("check_interval_seconds", next)}
            />
          </div>
          <Field
            label="Window limits"
            hint="One window=remaining-percent entry per line."
          >
            <Textarea
              aria-label="Window limits"
              value={editor.windowLimits}
              onChange={(event) => {
                const windowLimits = event.target.value
                const hasWindowGuardrail = Object.values(
                  parseWindowLimits(windowLimits),
                ).some((remaining) => remaining > 0)
                onChange({
                  ...editor,
                  windowLimits,
                  value: hasWindowGuardrail
                    ? { ...value, max_parallel_children: 1 }
                    : value,
                })
              }}
              placeholder="daily=15\nweekly=10"
            />
          </Field>
          <div className="flex flex-wrap gap-5 text-sm">
            <Check
              label="Auto-resume after limits recover"
              checked={value.budget.auto_resume}
              onChange={(next) => setBudget("auto_resume", next)}
            />
            <Check
              label="Pause when quota is unknown"
              checked={value.budget.pause_on_unknown}
              onChange={(next) => setBudget("pause_on_unknown", next)}
            />
          </div>
        </section>
      </ReviewAdvancedSection>

      {!value.scope_policy.code_types.length && (
        <p role="alert" className="text-destructive text-sm">
          Select at least one code type.
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="button" disabled={busy || !valid} onClick={onSave}>
          Save profile
        </Button>
      </div>
    </div>
  )
}

function Field({
  label,
  hint,
  children,
}: {
  label: string
  hint?: string
  children: React.ReactNode
}) {
  return (
    <div className="space-y-2">
      <Label>{label}</Label>
      {hint && <p className="text-muted-foreground text-xs">{hint}</p>}
      {children}
    </div>
  )
}

function NumberField({
  label,
  value,
  step = "1",
  min = 0,
  max,
  disabled,
  onChange,
}: {
  label: string
  value: number
  step?: string
  min?: number
  max?: number
  disabled?: boolean
  onChange: (value: number) => void
}) {
  return (
    <Field label={label}>
      <Input
        aria-label={label}
        type="number"
        min={min}
        max={max}
        step={step}
        value={value}
        disabled={disabled}
        onChange={(event) => {
          const parsed = Number(event.target.value)
          if (!event.target.value || !Number.isFinite(parsed)) {
            onChange(0)
            return
          }
          onChange(Math.min(max ?? parsed, Math.max(min, parsed)))
        }}
      />
    </Field>
  )
}

function Check({
  label,
  checked,
  onChange,
}: {
  label: string
  checked: boolean
  onChange: (checked: boolean) => void
}) {
  return (
    <label className="flex items-center gap-2 text-sm">
      <input
        type="checkbox"
        checked={checked}
        onChange={(event) => onChange(event.target.checked)}
      />
      {label}
    </label>
  )
}

function copyProfile(
  value: RepositoryReviewProfileConfig,
): RepositoryReviewProfileConfig {
  return {
    ...value,
    model_price: { ...value.model_price },
    scope_policy: {
      ...value.scope_policy,
      code_types: [...value.scope_policy.code_types],
      include_folders: [...value.scope_policy.include_folders],
      exclude_folders: [...value.scope_policy.exclude_folders],
    },
    budget: {
      ...value.budget,
      account_ids: [...value.budget.account_ids],
      min_remaining_percent_by_window: {
        ...value.budget.min_remaining_percent_by_window,
      },
    },
  }
}

function modelPrice(model?: ReviewModelOption): ReviewModelPrice {
  if (!model?.price_known) {
    return { input_price_per_1m: 0, output_price_per_1m: 0 }
  }
  return {
    input_price_per_1m: model.input_price_per_1m,
    output_price_per_1m: model.output_price_per_1m,
    ...(model.subscription == null ? {} : { subscription: model.subscription }),
    ...(model.equivalent_model
      ? { equivalent_model: model.equivalent_model }
      : {}),
  }
}

function modelPriceBillable(price: ReviewModelPrice): boolean {
  return price.input_price_per_1m > 0 || price.output_price_per_1m > 0
}

function setModelPrice(
  editor: ProfileEditor,
  model_price: ReviewModelPrice,
  onChange: (editor: ProfileEditor) => void,
) {
  onChange({
    ...editor,
    value: {
      ...editor.value,
      model_price,
      budget: {
        ...editor.value.budget,
        ...(modelPriceBillable(model_price)
          ? {}
          : { max_estimated_cost_usd: 0 }),
      },
    },
  })
}

function profileGuardrailsActive(
  budget: RepositoryReviewAutomationBudget,
): boolean {
  return (
    budget.max_total_tokens > 0 ||
    budget.max_estimated_cost_usd > 0 ||
    budget.account_ids.length > 0 ||
    budget.min_remaining_percent > 0 ||
    budget.pause_on_unknown ||
    Object.values(budget.min_remaining_percent_by_window).some(
      (remaining) => remaining > 0,
    )
  )
}

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

function parseWindowLimits(value: string): Record<string, number> {
  return Object.fromEntries(
    lines(value).flatMap((line) => {
      const [name, raw] = line.split("=", 2).map((item) => item.trim())
      const percent = Number(raw)
      return name && Number.isFinite(percent)
        ? [[name, Math.min(100, Math.max(0, percent))]]
        : []
    }),
  )
}

function formatWindowLimits(value: Record<string, number>): string {
  return Object.entries(value)
    .map(([name, percent]) => `${name}=${percent}`)
    .join("\n")
}

function formatLimit(value: number): string {
  return value > 0 ? new Intl.NumberFormat().format(value) : "No"
}

function formatMoney(value: number): string {
  return value > 0 ? `$${value.toFixed(2)}` : "No"
}

function Empty({ text }: { text: string }) {
  return (
    <Card size="sm" className="border-dashed">
      <CardContent className="text-muted-foreground py-10 text-center text-sm">
        {text}
      </CardContent>
    </Card>
  )
}

function errorMessage(error: unknown): string {
  return error instanceof Error
    ? error.message
    : "Review profile request failed."
}
