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
  type RepositoryReviewCodeType,
  type RepositoryReviewProfile,
  type RepositoryReviewProfileConfig,
  createRepositoryReviewProfile,
  deleteRepositoryReviewProfile,
  getRepositoryReviewAutomationOptions,
  listRepositoryReviewProfiles,
  updateRepositoryReviewProfile,
} from "@/api/repository-reviews"
import { PageHeader } from "@/components/page-header"
import { RepositoryReviewGuardExpressionEditor } from "@/components/repository-reviews/repository-review-guard-expression-editor"
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
}

const emptyProfile: RepositoryReviewProfileConfig = {
  name: "",
  account_ref: "",
  reviewer_model: "",
  review_focus: "Find correctness, security, and reliability bugs.",
  force: false,
  auto_continue: true,
  max_files_per_run: 24,
  max_content_bytes: 524_288,
  max_parallel_children: 8,
  scope_policy: {
    code_types: ["hotpath-code", "code"],
    include_folders: [],
    exclude_folders: [],
    free_text: "",
  },
  budget: {
    guard_expression: "",
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
          guard_expression: value.budget.guard_expression.trim(),
        },
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
    setActionError("")
    const defaultAccount = options.accounts.find((account) => account.default)
    const model = options.models.find(
      (item) =>
        item.available &&
        (defaultAccount?.models === undefined ||
          defaultAccount.models.includes(item.alias)),
    )
    const value = copyProfile(emptyProfile)
    if (model) {
      value.reviewer_model = model.alias
    }
    setEditor({
      profile: null,
      value,
      includeFolders: "",
      excludeFolders: "",
    })
  }
  const openEdit = (profile: RepositoryReviewProfile) => {
    setActionError("")
    const value = copyProfile(profile)
    setEditor({
      profile,
      value,
      includeFolders: profile.scope_policy.include_folders.join("\n"),
      excludeFolders: profile.scope_policy.exclude_folders.join("\n"),
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
          {actionError && !editor && (
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
                        Account:{" "}
                        {profileAccountLabel(
                          profile.account_ref,
                          options.accounts,
                        )}
                      </span>
                      <span>
                        {profile.budget.guard_expression.trim()
                          ? "Task guard configured"
                          : "No task guard"}
                      </span>
                      <span>{profile.max_files_per_run} files per batch</span>
                      <span>
                        {profile.max_parallel_children} parallel workers
                      </span>
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
        onOpenChange={(open) => {
          if (!open) {
            setEditor(null)
            setActionError("")
          }
        }}
      >
        <DialogContent className="max-h-[92vh] overflow-y-auto sm:max-w-3xl">
          <DialogHeader>
            <DialogTitle>
              {editor?.profile ? "Edit profile" : "New review profile"}
            </DialogTitle>
            <DialogDescription>
              Name, model, review focus, and scope stay visible. Execution
              sizing and guardrails remain under Advanced until needed.
            </DialogDescription>
          </DialogHeader>
          {actionError && (
            <div
              role="alert"
              className="text-destructive flex items-center gap-2 text-sm"
            >
              <IconAlertTriangle className="size-4" /> {actionError}
            </div>
          )}
          {editor && (
            <ProfileForm
              editor={editor}
              models={options.models}
              accounts={options.accounts}
              busy={saveMutation.isPending}
              onChange={(next) => {
                setActionError("")
                setEditor(next)
              }}
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
  const setGuardExpression = (guardExpression: string) =>
    setValue("budget", {
      ...value.budget,
      guard_expression: guardExpression,
    })
  const setScope = <
    K extends keyof RepositoryReviewProfileConfig["scope_policy"],
  >(
    key: K,
    next: RepositoryReviewProfileConfig["scope_policy"][K],
  ) => setValue("scope_policy", { ...value.scope_policy, [key]: next })
  const selectedAccount = value.account_ref
    ? accounts.find((account) => account.id === value.account_ref)
    : accounts.find((account) => account.default)
  const modelAvailableOnSelectedAccount = (alias: string) =>
    selectedAccount?.models === undefined ||
    selectedAccount.models.includes(alias)
  const valid =
    value.name.trim() !== "" &&
    value.reviewer_model !== "" &&
    modelAvailableOnSelectedAccount(value.reviewer_model) &&
    value.scope_policy.code_types.length > 0 &&
    value.max_files_per_run >= 1 &&
    value.max_content_bytes >= 1 &&
    value.max_parallel_children >= 1

  return (
    <div className="space-y-5">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Profile name" controlId="review-profile-name">
          <Input
            id="review-profile-name"
            aria-label="Profile name"
            value={value.name}
            onChange={(event) => setValue("name", event.target.value)}
          />
        </Field>
        <Field label="Reviewer model" controlId="review-profile-model">
          <select
            id="review-profile-model"
            aria-label="Reviewer model"
            className="border-input bg-background h-9 w-full rounded-md border px-3 text-sm"
            value={value.reviewer_model}
            onChange={(event) => {
              setValue("reviewer_model", event.target.value)
            }}
          >
            <option value="">Select model</option>
            {models.map((model) => (
              <option
                key={model.alias}
                value={model.alias}
                disabled={
                  !model.available ||
                  !modelAvailableOnSelectedAccount(model.alias)
                }
              >
                {model.alias}
                {!model.available
                  ? " (unavailable)"
                  : modelAvailableOnSelectedAccount(model.alias)
                    ? ""
                    : " (unavailable on account)"}
              </option>
            ))}
          </select>
        </Field>
      </div>
      <Field
        label="Review focus"
        hint="Narrows defect classes only. Findings diagnose validated defects and never include fixes or remediation."
        hintId="review-focus-help"
        controlId="review-profile-focus"
      >
        <Textarea
          id="review-profile-focus"
          aria-label="Review focus"
          aria-describedby="review-focus-help"
          value={value.review_focus}
          onChange={(event) => setValue("review_focus", event.target.value)}
        />
      </Field>
      <section className="space-y-3 rounded-lg border p-4">
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
          <Field label="Include folders" controlId="review-include-folders">
            <Textarea
              id="review-include-folders"
              aria-label="Include folders"
              value={editor.includeFolders}
              onChange={(event) =>
                onChange({ ...editor, includeFolders: event.target.value })
              }
              placeholder={"cmd\ninternal/review"}
            />
          </Field>
          <Field label="Exclude folders" controlId="review-exclude-folders">
            <Textarea
              id="review-exclude-folders"
              aria-label="Exclude folders"
              value={editor.excludeFolders}
              onChange={(event) =>
                onChange({ ...editor, excludeFolders: event.target.value })
              }
              placeholder={"generated\ntestdata"}
            />
          </Field>
        </div>
        <Field
          label="Additional scope guidance"
          controlId="review-scope-guidance"
        >
          <Textarea
            id="review-scope-guidance"
            aria-label="Additional scope guidance"
            value={value.scope_policy.free_text}
            onChange={(event) => setScope("free_text", event.target.value)}
          />
        </Field>
      </section>

      <ReviewAdvancedSection description="execution, sizing, and task admission">
        <section className="space-y-3">
          <h3 className="text-sm font-semibold">Execution</h3>
          <Field
            label="Execution account"
            hint="Default account follows the runtime's configured default account."
            hintId="review-account-help"
            controlId="review-execution-account"
          >
            <select
              id="review-execution-account"
              aria-label="Execution account"
              aria-describedby="review-account-help"
              className="border-input bg-background h-9 w-full rounded-md border px-3 text-sm"
              value={value.account_ref}
              onChange={(event) => {
                const accountRef = event.target.value
                const account = accountRef
                  ? accounts.find((candidate) => candidate.id === accountRef)
                  : accounts.find((candidate) => candidate.default)
                const reviewerModel =
                  account?.models === undefined ||
                  account.models.includes(value.reviewer_model)
                    ? value.reviewer_model
                    : (models.find(
                        (model) =>
                          model.available &&
                          account.models?.includes(model.alias),
                      )?.alias ?? "")
                onChange({
                  ...editor,
                  value: {
                    ...value,
                    account_ref: accountRef,
                    reviewer_model: reviewerModel,
                  },
                })
              }}
            >
              <option value="">
                {accounts.find((account) => account.default)?.label
                  ? `Default account (currently ${accounts.find((account) => account.default)?.label})`
                  : "Default account"}
              </option>
              {value.account_ref &&
                !accounts.some(
                  (account) => account.id === value.account_ref,
                ) && (
                  <option value={value.account_ref}>
                    {value.account_ref} (unavailable)
                  </option>
                )}
              {accounts.map((account) => (
                <option
                  key={account.id}
                  value={account.id}
                  disabled={account.models?.length === 0}
                >
                  {account.label || account.id}
                  {account.provider ? ` · ${account.provider}` : ""}
                  {account.models?.length === 0
                    ? " (no compatible review models)"
                    : ""}
                </option>
              ))}
            </select>
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
              hint="Runs independent review tasks concurrently. The task guard is checked separately before each worker takes another task."
              describedBy="parallel-workers-help"
              min={1}
              max={64}
              value={value.max_parallel_children}
              onChange={(next) => setValue("max_parallel_children", next)}
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
          <h3 className="text-sm font-semibold">Task admission guard</h3>
          <RepositoryReviewGuardExpressionEditor
            value={value.budget.guard_expression}
            limitWindows={(selectedAccount?.entries ?? []).flatMap((entry) =>
              entry.window ? [entry.window] : [],
            )}
            onChange={setGuardExpression}
          />
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
  hintId,
  controlId,
  children,
}: {
  label: string
  hint?: string
  hintId?: string
  controlId?: string
  children: React.ReactNode
}) {
  return (
    <div className="space-y-2">
      <Label htmlFor={controlId}>{label}</Label>
      {hint && (
        <p id={hintId} className="text-muted-foreground text-xs">
          {hint}
        </p>
      )}
      {children}
    </div>
  )
}

function NumberField({
  label,
  hint,
  value,
  min = 0,
  max,
  describedBy,
  onChange,
}: {
  label: string
  hint?: string
  value: number
  min?: number
  max?: number
  describedBy?: string
  onChange: (value: number) => void
}) {
  const controlId = `review-profile-${label.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`
  return (
    <Field
      label={label}
      hint={hint}
      hintId={hint ? describedBy : undefined}
      controlId={controlId}
    >
      <Input
        id={controlId}
        aria-label={label}
        type="number"
        min={min}
        max={max}
        step="1"
        value={value}
        aria-describedby={describedBy}
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
    scope_policy: {
      ...value.scope_policy,
      code_types: [...value.scope_policy.code_types],
      include_folders: [...value.scope_policy.include_folders],
      exclude_folders: [...value.scope_policy.exclude_folders],
    },
    budget: {
      ...value.budget,
    },
  }
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

function profileAccountLabel(
  accountRef: string,
  accounts: Awaited<
    ReturnType<typeof getRepositoryReviewAutomationOptions>
  >["accounts"],
): string {
  if (!accountRef) return "Default account"
  const account = accounts.find((candidate) => candidate.id === accountRef)
  return account?.label || accountRef
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
