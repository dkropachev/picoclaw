import {
  IconAlertTriangle,
  IconEdit,
  IconPlus,
  IconTrash,
} from "@tabler/icons-react"
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"
import { useEffect, useMemo, useState } from "react"

import {
  RepositoryReviewAPIError,
  type RepositoryReviewCodeType,
  type RepositoryReviewProfile,
  type RepositoryReviewProfileConfig,
  type ReviewAccountOption,
  type ReviewModelOption,
  createRepositoryReviewProfile,
  deleteRepositoryReviewProfile,
  getRepositoryReviewAutomationOptions,
  getRepositoryReviewProfile,
  listRepositoryReviewProfilesPage,
  repositoryReviewDefaultIssuePrompt,
  updateRepositoryReviewProfile,
} from "@/api/repository-reviews"
import {
  type CollectionDefinition,
  CollectionDetailShell,
} from "@/components/collection"
import { StandardCollectionPage } from "@/components/collection/standard-collection-page"
import { RepositoryReviewGuardExpressionEditor } from "@/components/repository-reviews/repository-review-guard-expression-editor"
import {
  repositoryReviewProfileDefaultQuery,
  repositoryReviewProfileViews,
} from "@/components/repository-reviews/repository-review-profile-route-state"
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
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import {
  type CollectionRouteSearch,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

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
  issue_writer_model: "",
  review_focus: "Find correctness, security, and reliability bugs.",
  issue_prompt: repositoryReviewDefaultIssuePrompt,
  force: false,
  auto_continue: true,
  max_files_per_run: 24,
  max_content_bytes: 524_288,
  max_parallel_children: 8,
  assignment_timeout_seconds: 3_600,
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

export function RepositoryReviewProfilesPage({
  search,
  onSearchChange,
  onAdd,
  onOpen,
  onEdit,
}: {
  search: { q?: string; view?: "list" | "table" | "grid" }
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
  onAdd: () => void
  onOpen: (profile: RepositoryReviewProfile) => void
  onEdit: (profile: RepositoryReviewProfile) => void
}) {
  const activeQuery = normalizeCollectionRouteSearch(search, {
    defaultQuery: repositoryReviewProfileDefaultQuery,
    supportedViews: repositoryReviewProfileViews,
  }).q
  const query = useInfiniteQuery({
    queryKey: [...profilesKey, activeQuery],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      listRepositoryReviewProfilesPage(
        {
          query: activeQuery,
          cursor: pageParam || undefined,
          limit: 50,
        },
        signal,
      ),
    getNextPageParam: (page) => page.next_cursor || undefined,
    retry: false,
  })
  const profiles = useMemo(
    () => query.data?.pages.flatMap((page) => page.profiles) ?? [],
    [query.data?.pages],
  )
  const firstPage = query.data?.pages[0]
  const definition = useMemo<CollectionDefinition<RepositoryReviewProfile>>(
    () => ({
      key: "repository-review-profiles",
      title: "Review profiles",
      defaultQuery: repositoryReviewProfileDefaultQuery,
      supportedViews: repositoryReviewProfileViews,
      defaultView: "list",
      getItemID: (profile) => profile.id,
      getItemLabel: (profile) => profile.name,
      getItemIdentity: (profile) => ({
        title: profile.name,
        description: profile.reviewer_model,
        metadata: profile.review_focus,
      }),
      columns: [
        {
          id: "account",
          header: "Account",
          cell: (profile) => profile.account_ref || "Default",
        },
        {
          id: "writer",
          header: "Issue writer",
          cell: profileWriterLabel,
        },
        {
          id: "parallel",
          header: "Workers",
          cell: (profile) => profile.max_parallel_children,
          className: "w-24 tabular-nums",
        },
        {
          id: "updated",
          header: "Updated",
          cell: (profile) => formatTimestamp(profile.updated_at),
          className: "w-44",
        },
      ],
      gridFacts: [
        {
          id: "account",
          label: "Account",
          value: (profile) => profile.account_ref || "Default",
        },
        {
          id: "writer",
          label: "Issue writer",
          value: profileWriterLabel,
        },
        {
          id: "files",
          label: "Files per batch",
          value: (profile) => profile.max_files_per_run,
        },
        {
          id: "parallel",
          label: "Workers",
          value: (profile) => profile.max_parallel_children,
        },
      ],
      badges: [
        {
          id: "version",
          label: (profile) => `v${profile.version}`,
          variant: "secondary",
        },
        {
          id: "force",
          label: (profile) => (profile.force ? "force" : null),
          variant: "outline",
        },
      ],
      actions: [
        {
          id: "edit",
          label: "Edit profile",
          icon: <IconEdit />,
          onSelect: onEdit,
        },
      ],
    }),
    [onEdit],
  )
  return (
    <StandardCollectionPage
      definition={definition}
      search={search}
      onSearchChange={onSearchChange}
      items={profiles}
      total={firstPage?.total}
      schema={firstPage?.query_schema}
      canonicalQuery={firstPage?.canonical_query}
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
          <IconPlus /> New profile
        </Button>
      }
      emptyTitle="No review profiles"
      emptyDescription="Create a reusable review policy before assigning a repository."
    />
  )
}

export function RepositoryReviewProfileDetailPage({
  profileID,
  onBack,
  onEdit,
  onDeleted,
}: {
  profileID: string
  onBack: () => void
  onEdit: () => void
  onDeleted: () => void
}) {
  const queryClient = useQueryClient()
  const [actionError, setActionError] = useState("")
  const query = useQuery({
    queryKey: [...profilesKey, "detail", profileID],
    queryFn: ({ signal }) => getRepositoryReviewProfile(profileID, signal),
    retry: false,
  })
  const optionsQuery = useQuery({
    queryKey: optionsKey,
    queryFn: ({ signal }) => getRepositoryReviewAutomationOptions(signal),
    retry: false,
  })
  const profile = query.data
  const notFound =
    query.error instanceof RepositoryReviewAPIError &&
    query.error.status === 404
  const deleteMutation = useMutation({
    mutationFn: async () => {
      if (!profile) throw new Error("Review profile is unavailable.")
      return deleteRepositoryReviewProfile(profile.id, {
        expected_version: profile.version,
      })
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: profilesKey })
      onDeleted()
    },
    onError: (error) => setActionError(errorMessage(error)),
  })
  const accounts = optionsQuery.data?.accounts ?? []
  return (
    <CollectionDetailShell
      title={profile?.name || "Review profile"}
      identity={
        profile ? (
          <span className="font-mono text-xs">{profile.id}</span>
        ) : undefined
      }
      status={
        profile ? (
          <Badge variant="secondary">v{profile.version}</Badge>
        ) : undefined
      }
      actions={
        profile ? (
          <>
            <Button type="button" size="sm" variant="outline" onClick={onEdit}>
              <IconEdit /> Edit
            </Button>
            <AlertDialog>
              <AlertDialogTrigger asChild>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  disabled={deleteMutation.isPending}
                >
                  <IconTrash /> Delete
                </Button>
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>Delete {profile.name}?</AlertDialogTitle>
                  <AlertDialogDescription>
                    Assigned profiles cannot be deleted. Reassign any
                    repositories first.
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>Cancel</AlertDialogCancel>
                  <AlertDialogAction onClick={() => deleteMutation.mutate()}>
                    Delete profile
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          </>
        ) : undefined
      }
      loading={query.isLoading}
      error={!notFound ? query.error?.message : undefined}
      notFound={notFound}
      onBack={onBack}
      onRetry={() => void query.refetch()}
      backLabel="All review profiles"
    >
      {profile && (
        <div className="space-y-6">
          {actionError && <InlineError message={actionError} />}
          <ProfileDetailRows
            rows={[
              [
                "Execution account",
                profileAccountLabel(profile.account_ref, accounts),
              ],
              ["Reviewer", profile.reviewer_model],
              ["Issue writer", profileWriterLabel(profile)],
              ["Files per batch", String(profile.max_files_per_run)],
              ["Content bytes", String(profile.max_content_bytes)],
              ["Parallel workers", String(profile.max_parallel_children)],
              [
                "Assignment deadline",
                assignmentDeadlineLabel(profile.assignment_timeout_seconds),
              ],
              ["Auto continue", profile.auto_continue ? "Yes" : "No"],
              ["Force unchanged files", profile.force ? "Yes" : "No"],
              ["Updated", formatTimestamp(profile.updated_at)],
            ]}
          />
          <ProfileSection title="Review focus">
            <p className="text-sm whitespace-pre-wrap">
              {profile.review_focus}
            </p>
          </ProfileSection>
          <ProfileSection title="Issue prompt">
            <p className="text-sm whitespace-pre-wrap">
              {profile.issue_prompt}
            </p>
          </ProfileSection>
          <ProfileSection title="Scope">
            <ProfileDetailRows
              rows={[
                ["Code types", profile.scope_policy.code_types.join(", ")],
                [
                  "Included folders",
                  profile.scope_policy.include_folders.join(", ") ||
                    "All matching folders",
                ],
                [
                  "Excluded folders",
                  profile.scope_policy.exclude_folders.join(", ") || "None",
                ],
                ["Guidance", profile.scope_policy.free_text || "None"],
              ]}
            />
          </ProfileSection>
          <ProfileSection title="Task admission">
            <p className="font-mono text-sm whitespace-pre-wrap">
              {profile.budget.guard_expression || "No guard expression"}
            </p>
          </ProfileSection>
        </div>
      )}
    </CollectionDetailShell>
  )
}

export function RepositoryReviewProfileEditorPage({
  profileID,
  onBack,
  onSaved,
}: {
  profileID?: string
  onBack: () => void
  onSaved: (profile: RepositoryReviewProfile) => void
}) {
  const queryClient = useQueryClient()
  const [editor, setEditor] = useState<ProfileEditor | null>(null)
  const [actionError, setActionError] = useState("")
  const profileQuery = useQuery({
    queryKey: [...profilesKey, "detail", profileID],
    queryFn: ({ signal }) =>
      getRepositoryReviewProfile(profileID || "", signal),
    enabled: Boolean(profileID),
    retry: false,
  })
  const optionsQuery = useQuery({
    queryKey: optionsKey,
    queryFn: ({ signal }) => getRepositoryReviewAutomationOptions(signal),
    retry: false,
  })
  useEffect(() => {
    if (editor || !optionsQuery.data) return
    if (profileID) {
      if (!profileQuery.data) return
      setEditor(profileEditor(profileQuery.data))
      return
    }
    setEditor(
      newProfileEditor(optionsQuery.data.models, optionsQuery.data.accounts),
    )
  }, [editor, optionsQuery.data, profileID, profileQuery.data])
  const saveMutation = useMutation({
    mutationFn: (current: ProfileEditor) => {
      const config = profileConfig(current)
      return current.profile
        ? updateRepositoryReviewProfile(current.profile.id, {
            ...config,
            expected_version: current.profile.version,
          })
        : createRepositoryReviewProfile(config)
    },
    onSuccess: async (saved) => {
      queryClient.setQueryData([...profilesKey, "detail", saved.id], saved)
      await queryClient.invalidateQueries({ queryKey: profilesKey })
      onSaved(saved)
    },
    onError: (error) => setActionError(errorMessage(error)),
  })
  const notFound =
    profileQuery.error instanceof RepositoryReviewAPIError &&
    profileQuery.error.status === 404
  const loading =
    optionsQuery.isLoading || (Boolean(profileID) && profileQuery.isLoading)
  const loadError =
    (!optionsQuery.data ? optionsQuery.error : undefined) ??
    (!notFound ? profileQuery.error : undefined)
  const options = optionsQuery.data ?? { models: [], accounts: [] }
  const optionsUsable = optionsQuery.isSuccess && !optionsQuery.isFetching
  return (
    <CollectionDetailShell
      title={profileID ? "Edit review profile" : "New review profile"}
      identity={
        profileID ? (
          <span className="font-mono text-xs">{profileID}</span>
        ) : undefined
      }
      loading={loading}
      error={loadError?.message}
      notFound={notFound}
      onBack={onBack}
      onRetry={() => {
        void optionsQuery.refetch()
        if (profileID) void profileQuery.refetch()
      }}
      backLabel={profileID ? "Profile details" : "All review profiles"}
      contentClassName="max-w-3xl"
    >
      {editor && (
        <div className="space-y-5">
          <p className="text-muted-foreground text-sm">
            Account, reviewer, issue writer, focus, and scope are always
            visible. Sizing and task admission remain under Advanced.
          </p>
          {actionError && <InlineError message={actionError} />}
          {optionsQuery.isError && (
            <InlineError message="Reviewer models and execution accounts could not be refreshed. Refresh before saving." />
          )}
          <ProfileForm
            editor={editor}
            models={options.models}
            accounts={options.accounts}
            busy={saveMutation.isPending || !optionsUsable}
            onChange={(next) => {
              setActionError("")
              setEditor(next)
            }}
            onCancel={onBack}
            onSave={() => saveMutation.mutate(editor)}
          />
        </div>
      )}
    </CollectionDetailShell>
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
  const selectedModel = models.find(
    (model) => model.alias === value.reviewer_model,
  )
  const selectedModelAvailability = profileModelAvailability(
    selectedModel,
    selectedAccount,
  )
  const availableModel = firstAvailableProfileModel(models, selectedAccount)
  const selectedWriterModel = value.issue_writer_model
    ? models.find((model) => model.alias === value.issue_writer_model)
    : selectedModel
  const selectedWriterAvailability = profileModelAvailability(
    selectedWriterModel,
    selectedAccount,
  )
  const accountIssue = selectedAccount
    ? selectedAccount.available
      ? ""
      : `Execution account ${selectedAccount.label || selectedAccount.id} is unavailable (${selectedAccount.status || "credential unavailable"}).`
    : value.account_ref
      ? `Execution account ${value.account_ref} is no longer available.`
      : "No default execution account is available. Choose an account."
  const modelIssue = value.reviewer_model
    ? selectedModelAvailability.reason
    : !selectedAccount
      ? "Choose an available execution account before selecting a reviewer model."
      : models.length === 0
        ? "No reviewer model aliases are configured."
        : !availableModel
          ? `No reviewer models are available on ${selectedAccount.label || selectedAccount.id}.`
          : ""
  const explicitWriterAvailability = profileWriterModelAvailability(
    selectedWriterModel,
    selectedAccount,
  )
  const writerIssue = value.issue_writer_model
    ? explicitWriterAvailability.reason
    : selectedModelAvailability.reason
  const valid =
    value.name.trim() !== "" &&
    value.reviewer_model !== "" &&
    value.issue_prompt.trim() !== "" &&
    selectedAccount !== undefined &&
    selectedModelAvailability.available &&
    (value.issue_writer_model
      ? explicitWriterAvailability.available
      : selectedWriterAvailability.available) &&
    value.scope_policy.code_types.length > 0 &&
    value.max_files_per_run >= 1 &&
    value.max_content_bytes >= 1 &&
    value.max_parallel_children >= 1 &&
    validAssignmentTimeout(value.assignment_timeout_seconds)

  return (
    <div className="space-y-5">
      <Field label="Profile name" controlId="review-profile-name">
        <Input
          id="review-profile-name"
          aria-label="Profile name"
          value={value.name}
          onChange={(event) => setValue("name", event.target.value)}
        />
      </Field>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field
          label="Execution account"
          hint="Default account follows the runtime's configured default account."
          hintId="review-account-help"
          controlId="review-execution-account"
        >
          <select
            id="review-execution-account"
            aria-label="Execution account"
            aria-describedby={
              accountIssue
                ? "review-account-help review-account-availability"
                : "review-account-help"
            }
            aria-invalid={accountIssue ? true : undefined}
            className="border-input bg-background h-9 w-full rounded-md border px-3 text-sm"
            value={value.account_ref}
            onChange={(event) => {
              const accountRef = event.target.value
              const account = accountRef
                ? accounts.find((candidate) => candidate.id === accountRef)
                : accounts.find((candidate) => candidate.default)
              const current = models.find(
                (model) => model.alias === value.reviewer_model,
              )
              const reviewerModel = profileModelAvailability(current, account)
                .available
                ? value.reviewer_model
                : (firstAvailableProfileModel(models, account)?.alias ?? "")
              const writer = value.issue_writer_model
                ? models.find(
                    (model) => model.alias === value.issue_writer_model,
                  )
                : undefined
              const issueWriterModel = value.issue_writer_model
                ? profileWriterModelAvailability(writer, account).available
                  ? value.issue_writer_model
                  : ""
                : ""
              onChange({
                ...editor,
                value: {
                  ...value,
                  account_ref: accountRef,
                  reviewer_model: reviewerModel,
                  issue_writer_model: issueWriterModel,
                },
              })
            }}
          >
            <option
              value=""
              disabled={
                !firstAvailableProfileModel(
                  models,
                  accounts.find((account) => account.default),
                )
              }
            >
              {defaultAccountOptionLabel(
                accounts,
                Boolean(
                  firstAvailableProfileModel(
                    models,
                    accounts.find((account) => account.default),
                  ),
                ),
              )}
            </option>
            {value.account_ref &&
              !accounts.some((account) => account.id === value.account_ref) && (
                <option value={value.account_ref} disabled>
                  {value.account_ref} (unavailable)
                </option>
              )}
            {accounts.map((account) => {
              const selectable = Boolean(
                firstAvailableProfileModel(models, account),
              )
              return (
                <option
                  key={account.id}
                  value={account.id}
                  disabled={!selectable}
                >
                  {accountOptionLabel(account, selectable)}
                </option>
              )
            })}
          </select>
          {accountIssue && (
            <p
              id="review-account-availability"
              role="alert"
              className="text-destructive text-xs"
            >
              {accountIssue}
            </p>
          )}
        </Field>
        <Field label="Reviewer model" controlId="review-profile-model">
          <select
            id="review-profile-model"
            aria-label="Reviewer model"
            aria-describedby={
              modelIssue ? "review-model-availability" : undefined
            }
            aria-invalid={modelIssue ? true : undefined}
            className="border-input bg-background h-9 w-full rounded-md border px-3 text-sm"
            value={value.reviewer_model}
            disabled={!selectedAccount?.available}
            onChange={(event) => {
              setValue("reviewer_model", event.target.value)
            }}
          >
            <option value="">Select model</option>
            {value.reviewer_model && !selectedModel && (
              <option value={value.reviewer_model} disabled>
                {value.reviewer_model} (unavailable)
              </option>
            )}
            {models.map((model) => {
              const availability = profileModelAvailability(
                model,
                selectedAccount,
              )
              return (
                <option
                  key={model.alias}
                  value={model.alias}
                  disabled={!availability.available}
                >
                  {modelOptionLabel(model, availability)}
                </option>
              )
            })}
          </select>
          {modelIssue && (
            <p
              id="review-model-availability"
              role="alert"
              className="text-destructive text-xs"
            >
              {modelIssue}
            </p>
          )}
        </Field>
      </div>
      <Field
        label="Issue writer model"
        hint="Blank uses the reviewer model. An explicit alias writes previews and ranks existing-issue candidates on the same execution account."
        hintId="review-issue-writer-help"
        controlId="review-profile-issue-writer"
      >
        <select
          id="review-profile-issue-writer"
          aria-label="Issue writer model"
          aria-describedby={
            writerIssue
              ? "review-issue-writer-help review-issue-writer-availability"
              : "review-issue-writer-help"
          }
          aria-invalid={writerIssue ? true : undefined}
          className="border-input bg-background h-9 w-full rounded-md border px-3 text-sm"
          value={value.issue_writer_model ?? ""}
          disabled={
            !selectedAccount?.available || !selectedModelAvailability.available
          }
          onChange={(event) =>
            setValue("issue_writer_model", event.target.value)
          }
        >
          <option value="">
            Same as reviewer
            {value.reviewer_model ? ` (${value.reviewer_model})` : ""}
          </option>
          {value.issue_writer_model && !selectedWriterModel && (
            <option value={value.issue_writer_model} disabled>
              {value.issue_writer_model} (unavailable)
            </option>
          )}
          {models.map((model) => {
            const availability = profileWriterModelAvailability(
              model,
              selectedAccount,
            )
            return (
              <option
                key={model.alias}
                value={model.alias}
                disabled={!availability.available}
              >
                {modelOptionLabel(model, availability)}
              </option>
            )
          })}
        </select>
        {writerIssue && (
          <p
            id="review-issue-writer-availability"
            role="alert"
            className="text-destructive text-xs"
          >
            {writerIssue}
          </p>
        )}
      </Field>
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
      <Field
        label="Issue prompt"
        hint="Controls issue presentation. The private diagnosis-only policy remains server-owned."
        hintId="review-issue-prompt-help"
        controlId="review-profile-issue-prompt"
      >
        <Textarea
          id="review-profile-issue-prompt"
          aria-label="Issue prompt"
          aria-describedby="review-issue-prompt-help"
          className="min-h-28"
          value={value.issue_prompt}
          onChange={(event) => setValue("issue_prompt", event.target.value)}
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

      <ReviewAdvancedSection description="sizing and task admission">
        <section className="space-y-3">
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
            <NumberField
              label="Assignment deadline (minutes)"
              hint="Required whole-minute wall-clock limit from 1 to 1,440 minutes."
              describedBy="assignment-deadline-help"
              min={1}
              max={1_440}
              value={value.assignment_timeout_seconds / 60}
              invalid={
                !validAssignmentTimeout(value.assignment_timeout_seconds)
              }
              onChange={(next) =>
                setValue("assignment_timeout_seconds", Math.round(next) * 60)
              }
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

interface ProfileModelAvailability {
  available: boolean
  reason: string
}

function firstAvailableProfileModel(
  models: ReviewModelOption[],
  account: ReviewAccountOption | undefined,
): ReviewModelOption | undefined {
  if (!account?.available) return undefined
  return models.find(
    (model) =>
      model.available && account.models?.includes(model.alias) === true,
  )
}

function profileModelAvailability(
  model: ReviewModelOption | undefined,
  account: ReviewAccountOption | undefined,
): ProfileModelAvailability {
  if (!model) {
    return {
      available: false,
      reason: "Reviewer model alias is no longer configured.",
    }
  }
  if (!model.available) {
    return {
      available: false,
      reason:
        model.blocked_reason ||
        "Reviewer model is unavailable for every execution account.",
    }
  }
  if (!account) {
    return {
      available: false,
      reason: "Choose an available execution account first.",
    }
  }
  if (!account.available) {
    return {
      available: false,
      reason: `Execution account ${account.label || account.id} is unavailable.`,
    }
  }
  if (account.models?.includes(model.alias) !== true) {
    return {
      available: false,
      reason: `Reviewer model is unavailable on ${account.label || account.id}.`,
    }
  }
  return { available: true, reason: "" }
}

function profileWriterModelAvailability(
  model: ReviewModelOption | undefined,
  account: ReviewAccountOption | undefined,
): ProfileModelAvailability {
  if (!model) {
    return {
      available: false,
      reason: "Issue writer model alias is no longer configured.",
    }
  }
  if (!account) {
    return {
      available: false,
      reason: "Choose an available execution account first.",
    }
  }
  if (!account.available) {
    return {
      available: false,
      reason: `Execution account ${account.label || account.id} is unavailable.`,
    }
  }
  if (account.writer_models?.includes(model.alias) !== true) {
    return {
      available: false,
      reason: `Issue writer model is unavailable on ${account.label || account.id}.`,
    }
  }
  return { available: true, reason: "" }
}

function modelOptionLabel(
  model: ReviewModelOption,
  availability: ProfileModelAvailability,
): string {
  return availability.available
    ? model.alias
    : `${model.alias} (${availability.reason})`
}

function defaultAccountOptionLabel(
  accounts: ReviewAccountOption[],
  selectable: boolean,
): string {
  const account = accounts.find((candidate) => candidate.default)
  if (!account) return "Default account (unavailable)"
  const label = `Default account (currently ${account.label || account.id})`
  if (!account.available) {
    return `${label} (${account.status || "credential unavailable"})`
  }
  return selectable ? label : `${label} (no available reviewer models)`
}

function accountOptionLabel(
  account: ReviewAccountOption,
  selectable: boolean,
): string {
  const label = `${account.label || account.id}${
    account.provider ? ` · ${account.provider}` : ""
  }`
  if (!account.available) {
    return `${label} (${account.status || "credential unavailable"})`
  }
  return selectable ? label : `${label} (no available reviewer models)`
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
  invalid = false,
  onChange,
}: {
  label: string
  hint?: string
  value: number
  min?: number
  max?: number
  describedBy?: string
  invalid?: boolean
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
        aria-invalid={invalid || undefined}
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

function newProfileEditor(
  models: ReviewModelOption[],
  accounts: ReviewAccountOption[],
): ProfileEditor {
  const defaultAccount = accounts.find(
    (account) => account.default && firstAvailableProfileModel(models, account),
  )
  const account =
    defaultAccount ??
    accounts.find((candidate) => firstAvailableProfileModel(models, candidate))
  const model = firstAvailableProfileModel(models, account)
  const value = copyProfile(emptyProfile)
  value.account_ref = account && !account.default ? account.id : ""
  value.reviewer_model = model?.alias ?? ""
  return { profile: null, value, includeFolders: "", excludeFolders: "" }
}

function profileEditor(profile: RepositoryReviewProfile): ProfileEditor {
  return {
    profile,
    value: copyProfile(profile),
    includeFolders: profile.scope_policy.include_folders.join("\n"),
    excludeFolders: profile.scope_policy.exclude_folders.join("\n"),
  }
}

function profileConfig(editor: ProfileEditor): RepositoryReviewProfileConfig {
  return {
    ...editor.value,
    name: editor.value.name.trim(),
    review_focus: editor.value.review_focus.trim(),
    issue_prompt: editor.value.issue_prompt.trim(),
    scope_policy: {
      ...editor.value.scope_policy,
      include_folders: lines(editor.includeFolders),
      exclude_folders: lines(editor.excludeFolders),
      free_text: editor.value.scope_policy.free_text.trim(),
    },
    budget: {
      guard_expression: editor.value.budget.guard_expression.trim(),
    },
  }
}

function profileWriterLabel(profile: RepositoryReviewProfile): string {
  return profile.issue_writer_model || `${profile.reviewer_model} (reviewer)`
}

function assignmentDeadlineLabel(seconds: number): string {
  const minutes = seconds / 60
  return `${minutes.toLocaleString()} minute${minutes === 1 ? "" : "s"}`
}

function validAssignmentTimeout(seconds: number): boolean {
  return (
    Number.isInteger(seconds) &&
    seconds >= 60 &&
    seconds <= 86_400 &&
    seconds % 60 === 0
  )
}

function formatTimestamp(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.valueOf())
    ? value || "Not reported"
    : date.toLocaleString()
}

function ProfileDetailRows({ rows }: { rows: Array<[string, string]> }) {
  return (
    <dl className="border-border divide-border divide-y rounded-lg border">
      {rows.map(([label, value]) => (
        <div
          key={label}
          className="grid gap-1 px-3 py-3 text-sm sm:grid-cols-[12rem_minmax(0,1fr)]"
        >
          <dt className="text-muted-foreground">{label}</dt>
          <dd className="min-w-0 break-words">{value}</dd>
        </div>
      ))}
    </dl>
  )
}

function ProfileSection({
  title,
  children,
}: {
  title: string
  children: React.ReactNode
}) {
  return (
    <section className="space-y-3">
      <h2 className="text-base font-semibold">{title}</h2>
      {children}
    </section>
  )
}

function InlineError({ message }: { message: string }) {
  return (
    <div
      role="alert"
      className="text-destructive flex items-center gap-2 text-sm"
    >
      <IconAlertTriangle className="size-4 shrink-0" /> {message}
    </div>
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

function errorMessage(error: unknown): string {
  if (!error) return ""
  return error instanceof Error
    ? error.message
    : "Review profile request failed."
}
