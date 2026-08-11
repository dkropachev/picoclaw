import {
  IconArrowDown,
  IconArrowUp,
  IconDeviceFloppy,
  IconLoader2,
  IconPlus,
  IconRefresh,
  IconTrash,
} from "@tabler/icons-react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useBlocker } from "@tanstack/react-router"
import {
  type FormEvent,
  type ReactNode,
  useCallback,
  useDeferredValue,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"
import { useTranslation } from "react-i18next"

import {
  type ReviewAttentionAgent,
  type ReviewAttentionAgentCatalog,
  ReviewAttentionAgentsAPIError,
  getReviewAttentionAgents,
} from "@/api/review-attention-agents"
import {
  type ReviewAttentionGateKind,
  ReviewAttentionPoliciesAPIError,
  type ReviewAttentionPolicyMode,
  type ReviewAttentionPolicySnapshot,
  getReviewAttentionPolicies,
  putReviewAttentionPolicies,
} from "@/api/review-attention-policies"
import { ConfigChangeNotice } from "@/components/config-change-notice"
import { PageHeader } from "@/components/page-header"
import {
  type ReviewAttentionGateDraft,
  type ReviewAttentionGlobalPolicyDraft,
  type ReviewAttentionPolicyDraft,
  type ReviewAttentionPolicyIssue,
  type ReviewAttentionRepositoryPolicyDraft,
  convertReviewAttentionGateKind,
  createReviewAttentionEditorKeyFactory,
  createReviewAttentionGateDraft,
  createReviewAttentionGlobalPolicyDraft,
  createReviewAttentionRepositoryDraft,
  createReviewAttentionRepositoryPolicyDraft,
  reorderReviewAttentionGates,
  resolveReviewAttentionPolicy,
  reviewAttentionPolicyDraftFromCatalog,
  validateReviewAttentionPolicyDraft,
} from "@/components/reviews/review-attention-policy-model"
import { ReviewWorkbenchTabs } from "@/components/reviews/review-workbench-tabs"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { cn } from "@/lib/utils"

const policyQueryKey = ["reviews", "attention-policies"] as const
const agentQueryKey = ["reviews", "attention-policy-agents"] as const
const policyEditorPageSize = 8
const knownAttentionDecisionPoints = [
  {
    value: "review.submitted",
    labelKey: "pages.reviews.policies.decision_review_submitted",
    label: "Outgoing review submitted",
  },
  {
    value: "pr_development.review_attention_required",
    labelKey: "pages.reviews.policies.decision_pr_development_attention",
    label: "My PR development review needs attention",
  },
  {
    value: "pr_development.before_push",
    labelKey: "pages.reviews.policies.decision_pr_development_before_push",
    label: "Before pushing my PR changes",
  },
] as const

function agentPageQueryKey(configRevision: string, cursor: string | undefined) {
  return [...agentQueryKey, configRevision, cursor ?? "first"] as const
}

type EditablePolicy =
  | ReviewAttentionGlobalPolicyDraft
  | ReviewAttentionRepositoryPolicyDraft

interface PendingModeChange {
  repositoryKey: string
  policyKey: string
  mode: ReviewAttentionPolicyMode
}

export function ReviewAttentionPoliciesPage({
  onShowInbox,
  onShowDevelopment,
}: {
  onShowInbox: () => void
  onShowDevelopment: () => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [nextEditorKey] = useState(createReviewAttentionEditorKeyFactory)
  const proceedingNavigationRef = useRef(false)
  const [draft, setDraft] = useState<ReviewAttentionPolicyDraft | null>(null)
  const [baseline, setBaseline] = useState<ReviewAttentionPolicyDraft | null>(
    null,
  )
  const [snapshot, setSnapshot] =
    useState<ReviewAttentionPolicySnapshot | null>(null)
  const [reloadTarget, setReloadTarget] =
    useState<ReviewAttentionPolicySnapshot | null>(null)
  const [selectedScope, setSelectedScope] = useState("global")
  const [repositorySearch, setRepositorySearch] = useState("")
  const [saving, setSaving] = useState(false)
  const [reloading, setReloading] = useState(false)
  const [serverError, setServerError] = useState("")
  const [conflicted, setConflicted] = useState(false)
  const [reloadConfirmOpen, setReloadConfirmOpen] = useState(false)
  const [discardNavigationOpen, setDiscardNavigationOpen] = useState(false)
  const [pendingMode, setPendingMode] = useState<PendingModeChange | null>(null)
  const [agentCursor, setAgentCursor] = useState<string | undefined>()
  const [policyPage, setPolicyPage] = useState(0)
  const [trustedSelectedAgentIDs, setTrustedSelectedAgentIDs] = useState<
    ReadonlySet<string>
  >(() => new Set())

  const policyQuery = useQuery({
    queryKey: policyQueryKey,
    queryFn: ({ signal }) => getReviewAttentionPolicies(signal),
    // Exact number nodes and null-prototype question objects must not pass
    // through React Query's ordinary JSON structural-sharing copier.
    structuralSharing: false,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    retry: false,
  })
  const agentConfigRevision =
    snapshot?.config_revision ?? policyQuery.data?.config_revision
  const agentsQuery = useQuery({
    queryKey: agentPageQueryKey(agentConfigRevision ?? "pending", agentCursor),
    queryFn: ({ signal }) =>
      getReviewAttentionAgents({
        expectedConfigRevision: agentConfigRevision!,
        ...(agentCursor === undefined ? {} : { cursor: agentCursor }),
        signal,
      }),
    enabled: agentConfigRevision != null,
    // Moving between pages must release the prior page instead of growing a
    // browser-side copy of the complete configured-agent catalog.
    gcTime: 0,
    // A page is immutable inside its required configuration revision. Refresh
    // is explicit so a freshly seeded reload/save page is not fetched twice.
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    retry: false,
  })

  const initialize = useCallback(
    (next: ReviewAttentionPolicySnapshot) => {
      const nextDraft = reviewAttentionPolicyDraftFromCatalog(
        next,
        nextEditorKey,
      )
      setDraft(nextDraft)
      setBaseline(structuredClone(nextDraft))
      setSnapshot(next)
      setTrustedSelectedAgentIDs(new Set())
      setReloadTarget(null)
      setSelectedScope("global")
      setPolicyPage(0)
      setServerError("")
      setConflicted(false)
    },
    [nextEditorKey],
  )

  const dirty = useMemo(
    () =>
      draft != null &&
      baseline != null &&
      JSON.stringify(draft) !== JSON.stringify(baseline),
    [baseline, draft],
  )

  useEffect(() => {
    const incoming = policyQuery.data
    if (incoming == null) return
    if (draft == null || baseline == null || snapshot == null) {
      if (
        !agentsQuery.isError &&
        agentsQuery.data?.config_revision === incoming.config_revision
      ) {
        initialize(incoming)
      }
      return
    }
    if (incoming.config_revision === snapshot.config_revision) return
    setReloadTarget(incoming)
    setConflicted(true)
  }, [
    agentsQuery.data,
    agentsQuery.isError,
    baseline,
    draft,
    initialize,
    policyQuery.data,
    snapshot,
  ])

  const agentGenerationMatches =
    snapshot != null &&
    agentsQuery.data != null &&
    agentsQuery.data.config_revision === snapshot.config_revision
  const agentsUsable = agentGenerationMatches && !agentsQuery.isError
  const agentRevisionConflict =
    agentsQuery.error instanceof ReviewAttentionAgentsAPIError &&
    (agentsQuery.error.status === 409 ||
      agentsQuery.error.code === "config_revision_mismatch")
  const agentGenerationMismatch =
    agentRevisionConflict ||
    (snapshot != null &&
      agentsQuery.data != null &&
      agentsQuery.data.config_revision !== snapshot.config_revision)
  const referencedAgentIDs = useMemo(
    () => collectReviewAttentionAgentIDs(draft),
    [draft],
  )
  const baselineAgentIDs = useMemo(
    () => collectReviewAttentionAgentIDs(baseline),
    [baseline],
  )
  useEffect(() => {
    setTrustedSelectedAgentIDs((current) => {
      const retained = new Set(
        [...current].filter((id) => referencedAgentIDs.has(id)),
      )
      return retained.size === current.size ? current : retained
    })
  }, [referencedAgentIDs])
  const agentIDs = useMemo(() => {
    if (!agentsUsable) return new Set<string>()
    // Policy GET validates every referenced agent against this same complete
    // configuration generation. Page-selected IDs are admitted only through
    // a matching identity page and pruned when the draft stops using them.
    const ids = new Set(baselineAgentIDs)
    for (const id of trustedSelectedAgentIDs) ids.add(id)
    for (const agent of agentsQuery.data?.agents ?? []) ids.add(agent.id)
    if (agentsQuery.data?.default_agent_id) {
      ids.add(agentsQuery.data.default_agent_id)
    }
    return ids
  }, [
    agentsQuery.data,
    agentsUsable,
    baselineAgentIDs,
    trustedSelectedAgentIDs,
  ])
  const defaultAgentID = agentsUsable
    ? (agentsQuery.data?.default_agent_id ?? "")
    : ""
  const trustSelectedAgent = useCallback(
    (id: string) => {
      if (!agentIDs.has(id)) return
      setTrustedSelectedAgentIDs((current) => {
        if (current.has(id)) return current
        return new Set([...current, id])
      })
    },
    [agentIDs],
  )
  const previousAgentCursor = previousReviewAttentionAgentCursor(agentCursor)
  const agentPageNumber =
    agentCursor === undefined ? 1 : Number(agentCursor) / 256 + 1
  const deferredDraft = useDeferredValue(draft)
  const validationPending = deferredDraft !== draft
  const validation = useMemo(
    () =>
      deferredDraft == null || !agentsUsable
        ? null
        : validateReviewAttentionPolicyDraft(deferredDraft, agentIDs),
    [agentIDs, agentsUsable, deferredDraft],
  )
  const displayedValidation = validationPending ? null : validation

  const navigationBusy = saving || reloading
  const shouldBlockNavigation = useCallback(
    () => dirty || navigationBusy,
    [dirty, navigationBusy],
  )
  const navigationBlocker = useBlocker({
    shouldBlockFn: shouldBlockNavigation,
    enableBeforeUnload: shouldBlockNavigation,
    disabled: !dirty && !navigationBusy,
    withResolver: true,
  })

  useEffect(() => {
    if (navigationBlocker.status !== "blocked") return
    if (!dirty && !navigationBusy) {
      proceedingNavigationRef.current = true
      navigationBlocker.proceed()
      setDiscardNavigationOpen(false)
      return
    }
    setDiscardNavigationOpen(true)
  }, [dirty, navigationBlocker, navigationBusy])

  const changeDiscardNavigationOpen = (open: boolean) => {
    if (!open && navigationBlocker.status === "blocked") {
      if (!proceedingNavigationRef.current) navigationBlocker.reset()
      proceedingNavigationRef.current = false
    }
    setDiscardNavigationOpen(open)
  }

  const discardAndNavigate = () => {
    if (navigationBusy) return
    if (navigationBlocker.status === "blocked") {
      proceedingNavigationRef.current = true
      navigationBlocker.proceed()
    }
    setDiscardNavigationOpen(false)
  }

  const reloadLatest = async () => {
    if (saving || reloading) return
    setReloadConfirmOpen(false)
    setServerError("")
    setReloading(true)
    try {
      // A previously observed reload target is only a notification. Explicit
      // reload always reacquires both catalogs so B cannot remain pinned after
      // the complete configuration advances to C.
      const latestPolicies = await policyQuery.refetch({ cancelRefetch: true })
      if (latestPolicies.isError || latestPolicies.data == null) {
        setServerError(
          t(
            "pages.reviews.policies.reload_error",
            "The latest policies could not be loaded. Your draft is still preserved.",
          ),
        )
        return
      }
      let latestAgents: ReviewAttentionAgentCatalog
      try {
        latestAgents = await getReviewAttentionAgents({
          expectedConfigRevision: latestPolicies.data.config_revision,
        })
      } catch {
        setServerError(
          t(
            "pages.reviews.policies.reload_generation_error",
            "A matching policy and agent configuration generation could not be loaded. Your draft is still preserved; retry the reload.",
          ),
        )
        return
      }
      if (
        latestAgents.config_revision !== latestPolicies.data.config_revision
      ) {
        setServerError(
          t(
            "pages.reviews.policies.reload_generation_error",
            "A matching policy and agent configuration generation could not be loaded. Your draft is still preserved; retry the reload.",
          ),
        )
        return
      }
      await queryClient.cancelQueries({ queryKey: agentQueryKey })
      queryClient.removeQueries({ queryKey: agentQueryKey })
      queryClient.setQueryData(
        agentPageQueryKey(latestPolicies.data.config_revision, undefined),
        latestAgents,
      )
      setAgentCursor(undefined)
      initialize(latestPolicies.data)
    } finally {
      setReloading(false)
    }
  }

  const requestReload = () => {
    if (saving || reloading || policyQuery.isFetching) return
    if (dirty) {
      setReloadConfirmOpen(true)
      return
    }
    void reloadLatest()
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (
      saving ||
      reloading ||
      policyQuery.isFetching ||
      conflicted ||
      !dirty ||
      validationPending ||
      snapshot == null ||
      validation?.catalog == null ||
      agentsQuery.data == null ||
      agentsQuery.isError ||
      agentsQuery.isFetching ||
      !agentGenerationMatches
    ) {
      return
    }
    setSaving(true)
    setServerError("")
    try {
      // A read dispatched before this replacement must never land after its
      // authoritative response. The query function consumes AbortSignal, and
      // React Query also ignores a canceled result if a test double or browser
      // transport completes after cancellation.
      await queryClient.cancelQueries({
        queryKey: policyQueryKey,
        exact: true,
      })
      const response = await putReviewAttentionPolicies(
        validation.catalog,
        snapshot.config_revision,
      )
      await queryClient.cancelQueries({
        queryKey: policyQueryKey,
        exact: true,
      })
      const observedPolicy =
        queryClient.getQueryData<ReviewAttentionPolicySnapshot>(policyQueryKey)
      const supersedingPolicy =
        observedPolicy != null &&
        observedPolicy.config_revision !== snapshot.config_revision &&
        observedPolicy.config_revision !== response.config_revision
          ? observedPolicy
          : null
      if (supersedingPolicy == null) {
        queryClient.setQueryData(policyQueryKey, response)
        await queryClient.cancelQueries({ queryKey: agentQueryKey })
        queryClient.removeQueries({ queryKey: agentQueryKey })
        queryClient.setQueryData<ReviewAttentionAgentCatalog>(
          agentPageQueryKey(response.config_revision, agentCursor),
          {
            ...agentsQuery.data,
            config_revision: response.config_revision,
          },
        )
        initialize(response)
      } else {
        // The PUT succeeded, but a read has already observed a generation
        // newer than both the captured and returned revisions. Never erase it
        // or claim that the local draft represents that newer generation.
        setReloadTarget(supersedingPolicy)
        setConflicted(true)
        setServerError(
          t(
            "pages.reviews.policies.saved_newer_available",
            "The policies were saved, but a newer configuration generation was observed before the response completed. Your draft is preserved; reload the latest policies.",
          ),
        )
      }
      showSaveSuccessOrRestartToast(
        t,
        t("pages.reviews.policies.saved", "Review attention policies saved."),
        t("pages.reviews.policies.name", "review attention policies"),
        response.effects.gateway_effect === "restart_required",
      )
    } catch (error) {
      if (
        error instanceof ReviewAttentionPoliciesAPIError &&
        (error.status === 409 || error.code === "config_revision_mismatch")
      ) {
        setConflicted(true)
        setServerError(
          t(
            "pages.reviews.policies.conflict",
            "These policies changed elsewhere. Your draft is preserved; reload the latest version before saving.",
          ),
        )
        const latest = await policyQuery.refetch({ cancelRefetch: true })
        if (
          !latest.isError &&
          latest.data != null &&
          latest.data.config_revision !== snapshot.config_revision
        ) {
          setReloadTarget(latest.data)
        } else {
          setServerError(
            t(
              "pages.reviews.policies.conflict_reload_error",
              "These policies changed elsewhere, but the latest generation could not be loaded. Your draft is preserved; retry the reload before saving.",
            ),
          )
        }
      } else {
        setServerError(
          t(
            "pages.reviews.policies.save_error",
            "The policies could not be saved. Your draft is still preserved.",
          ),
        )
      }
    } finally {
      setSaving(false)
    }
  }

  const selectGlobal = () => {
    setSelectedScope("global")
    setPolicyPage(0)
  }
  const selectedRepository =
    draft?.repositories.find(
      (repository) => repository.editorKey === selectedScope,
    ) ?? null
  const selectedRepositoryIndex =
    draft?.repositories.findIndex(
      (repository) => repository.editorKey === selectedScope,
    ) ?? -1
  const validationIssues = displayedValidation?.issues ?? []
  const selectedRepositoryIssue = findReviewAttentionPolicyIssue(
    validationIssues,
    `repositories[${selectedRepositoryIndex}].repository`,
  )
  const visibleRepositories = useMemo(() => {
    const search = repositorySearch.trim().toLowerCase()
    if (draft == null || search === "") return draft?.repositories ?? []
    return draft.repositories.filter((repository) =>
      repository.repository.toLowerCase().includes(search),
    )
  }, [draft, repositorySearch])

  const addRepository = () => {
    const repository = createReviewAttentionRepositoryDraft(nextEditorKey)
    setDraft((current) =>
      current == null
        ? current
        : { ...current, repositories: [...current.repositories, repository] },
    )
    setSelectedScope(repository.editorKey)
    setPolicyPage(0)
  }

  const removeRepository = (repositoryKey: string) => {
    setDraft((current) =>
      current == null
        ? current
        : {
            ...current,
            repositories: current.repositories.filter(
              (repository) => repository.editorKey !== repositoryKey,
            ),
          },
    )
    setSelectedScope("global")
    setPolicyPage(0)
  }

  const updateSelectedRepository = (
    update: (
      repository: NonNullable<typeof selectedRepository>,
    ) => NonNullable<typeof selectedRepository>,
  ) => {
    if (selectedRepository == null) return
    setDraft((current) =>
      current == null
        ? current
        : {
            ...current,
            repositories: current.repositories.map((repository) =>
              repository.editorKey === selectedRepository.editorKey
                ? update(repository)
                : repository,
            ),
          },
    )
  }

  const addPolicy = () => {
    if (draft == null) return
    if (selectedRepository == null) {
      const policy = createReviewAttentionGlobalPolicyDraft(nextEditorKey)
      setPolicyPage(Math.floor(draft.global.length / policyEditorPageSize))
      setDraft({ ...draft, global: [...draft.global, policy] })
      return
    }
    const policy = createReviewAttentionRepositoryPolicyDraft(nextEditorKey)
    setPolicyPage(
      Math.floor(selectedRepository.policies.length / policyEditorPageSize),
    )
    updateSelectedRepository((repository) => ({
      ...repository,
      policies: [...repository.policies, policy],
    }))
  }

  const updatePolicy = (policyKey: string, next: EditablePolicy) => {
    if (selectedRepository == null) {
      setDraft((current) =>
        current == null
          ? current
          : {
              ...current,
              global: current.global.map((policy) =>
                policy.editorKey === policyKey
                  ? (next as ReviewAttentionGlobalPolicyDraft)
                  : policy,
              ),
            },
      )
      return
    }
    updateSelectedRepository((repository) => ({
      ...repository,
      policies: repository.policies.map((policy) =>
        policy.editorKey === policyKey
          ? (next as ReviewAttentionRepositoryPolicyDraft)
          : policy,
      ),
    }))
  }

  const removePolicy = (policyKey: string) => {
    if (selectedRepository == null) {
      setDraft((current) =>
        current == null
          ? current
          : {
              ...current,
              global: current.global.filter(
                (policy) => policy.editorKey !== policyKey,
              ),
            },
      )
      return
    }
    updateSelectedRepository((repository) => ({
      ...repository,
      policies: repository.policies.filter(
        (policy) => policy.editorKey !== policyKey,
      ),
    }))
  }

  const requestModeChange = (
    policy: ReviewAttentionRepositoryPolicyDraft,
    mode: ReviewAttentionPolicyMode,
  ) => {
    if (
      selectedRepository == null ||
      mode === policy.mode ||
      (mode !== "inherit" && mode !== "disable") ||
      policy.gates.length === 0
    ) {
      applyModeChange(policy, mode)
      return
    }
    setPendingMode({
      repositoryKey: selectedRepository.editorKey,
      policyKey: policy.editorKey,
      mode,
    })
  }

  const applyModeChange = (
    policy: ReviewAttentionRepositoryPolicyDraft,
    mode: ReviewAttentionPolicyMode,
  ) => {
    let gates = policy.gates
    if (mode === "inherit" || mode === "disable") gates = []
    if ((mode === "overlay" || mode === "replace") && gates.length === 0) {
      gates = [
        createReviewAttentionGateDraft("zero", defaultAgentID, nextEditorKey),
      ]
    }
    updatePolicy(policy.editorKey, { ...policy, mode, gates })
  }

  const confirmModeChange = () => {
    if (pendingMode == null || draft == null) return
    const repository = draft.repositories.find(
      (candidate) => candidate.editorKey === pendingMode.repositoryKey,
    )
    const policy = repository?.policies.find(
      (candidate) => candidate.editorKey === pendingMode.policyKey,
    )
    if (policy != null) applyModeChange(policy, pendingMode.mode)
    setPendingMode(null)
  }

  const initialAgentGenerationMismatch =
    snapshot == null &&
    policyQuery.data != null &&
    agentsQuery.data != null &&
    agentsQuery.data.config_revision !== policyQuery.data.config_revision
  const initialAgentHydrationFailed =
    snapshot == null &&
    policyQuery.data != null &&
    (agentsQuery.isError || initialAgentGenerationMismatch)

  if (policyQuery.isPending || draft == null || snapshot == null) {
    if (policyQuery.isError || initialAgentHydrationFailed) {
      return (
        <PolicyPageState
          onShowInbox={onShowInbox}
          onShowDevelopment={onShowDevelopment}
          title={t(
            initialAgentHydrationFailed
              ? "pages.reviews.policies.hydration_error"
              : "pages.reviews.policies.load_error",
            initialAgentHydrationFailed
              ? "Review attention policies and configured agents could not be loaded as one trusted generation."
              : "Review attention policies are unavailable.",
          )}
          action={
            <Button
              type="button"
              disabled={
                policyQuery.isFetching || agentsQuery.isFetching || reloading
              }
              onClick={() => {
                if (initialAgentHydrationFailed && !agentRevisionConflict) {
                  void agentsQuery.refetch()
                  return
                }
                if (initialAgentHydrationFailed) {
                  void reloadLatest()
                  return
                }
                void policyQuery.refetch()
              }}
            >
              <IconRefresh className="size-4" />
              {initialAgentHydrationFailed && agentRevisionConflict
                ? t("pages.reviews.policies.reload", "Reload")
                : t("common.retry", "Retry")}
            </Button>
          }
        />
      )
    }
    return (
      <PolicyPageState
        onShowInbox={onShowInbox}
        onShowDevelopment={onShowDevelopment}
        title={t(
          "pages.reviews.policies.loading",
          "Loading review attention policies…",
        )}
        loading
      />
    )
  }

  const policies =
    selectedRepository == null ? draft.global : selectedRepository.policies
  const policyPageCount = Math.max(
    1,
    Math.ceil(policies.length / policyEditorPageSize),
  )
  const activePolicyPage = Math.min(policyPage, policyPageCount - 1)
  const policyPageStart = activePolicyPage * policyEditorPageSize
  const visiblePolicies = policies.slice(
    policyPageStart,
    policyPageStart + policyEditorPageSize,
  )
  const editorBusy = saving || reloading || policyQuery.isFetching

  return (
    <div className="bg-background flex h-full min-h-0 flex-col">
      <PageHeader
        title={t("pages.reviews.title", "Pull request reviews")}
        titleExtra={
          <Badge variant="secondary">
            {t("pages.reviews.policies.badge", "Policy configuration")}
          </Badge>
        }
      >
        <Button
          type="button"
          variant="outline"
          onClick={requestReload}
          disabled={editorBusy}
        >
          <IconRefresh
            className={cn("size-4", policyQuery.isFetching && "animate-spin")}
          />
          {t("pages.reviews.policies.reload", "Reload")}
        </Button>
        <Button
          type="submit"
          form="review-attention-policy-form"
          disabled={
            editorBusy ||
            conflicted ||
            !dirty ||
            validationPending ||
            validation?.catalog == null ||
            agentsQuery.data == null ||
            agentsQuery.isError ||
            agentsQuery.isFetching ||
            !agentGenerationMatches
          }
        >
          {saving ? (
            <IconLoader2 className="size-4 animate-spin" />
          ) : (
            <IconDeviceFloppy className="size-4" />
          )}
          {saving
            ? t("pages.reviews.policies.saving", "Saving…")
            : t("pages.reviews.policies.save", "Save policies")}
        </Button>
      </PageHeader>

      <ReviewWorkbenchTabs
        active="policies"
        navigationDisabled={editorBusy}
        onChange={(view) => {
          if (view === "inbox") {
            onShowInbox()
          } else if (view === "development") {
            onShowDevelopment()
          }
        }}
      />

      <form
        id="review-attention-policy-form"
        onSubmit={submit}
        className="flex min-h-0 flex-1 flex-col"
      >
        <fieldset disabled={editorBusy} className="contents">
          <div className="min-h-0 flex-1 overflow-auto p-3 lg:p-4">
            <div className="mx-auto flex w-full max-w-[1500px] flex-col gap-3">
              <ConfigChangeNotice
                kind="save"
                title={t(
                  "pages.reviews.policies.automatic_title",
                  "Decision gates request attention",
                )}
                description={t(
                  "pages.reviews.policies.automatic_description",
                  "Use review.submitted for reviews you send, pr_development.review_attention_required for reviewer feedback on your PRs, and pr_development.before_push for the final local publication decision. Matching policies run when their runtime reaches that decision. Changes affect only future decisions that have not pinned a policy revision. Saving only updates configuration; it does not run a gate or model, edit code, run CI, invoke Git or push, acknowledge a review, resolve a review thread, or merge a pull request.",
                )}
              />
              {snapshot.effects.gateway_effect === "restart_required" && (
                <ConfigChangeNotice
                  kind="restart"
                  title={t(
                    "pages.reviews.policies.restart_required",
                    "The running gateway still needs a restart to use this policy generation.",
                  )}
                />
              )}
              {(serverError || reloadTarget != null) && (
                <div
                  role="alert"
                  className="border-destructive/40 bg-destructive/5 text-destructive flex flex-col gap-2 rounded-lg border p-3 text-sm sm:flex-row sm:items-center sm:justify-between"
                >
                  <span>
                    {serverError ||
                      t(
                        "pages.reviews.policies.newer_available",
                        "A newer policy generation is available. Your draft has not been replaced.",
                      )}
                  </span>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={requestReload}
                  >
                    {t("pages.reviews.policies.review_reload", "Reload latest")}
                  </Button>
                </div>
              )}
              {agentsQuery.isError && !agentRevisionConflict && (
                <div
                  role="alert"
                  className="border-destructive/40 bg-destructive/5 text-destructive flex flex-col gap-2 rounded-lg border p-3 text-sm sm:flex-row sm:items-center sm:justify-between"
                >
                  <span>
                    {t(
                      "pages.reviews.policies.agents_load_error",
                      "Configured agents could not be loaded. AI gate validation and saving are paused; your policy draft is preserved.",
                    )}
                  </span>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    disabled={agentsQuery.isFetching}
                    onClick={() => void agentsQuery.refetch()}
                  >
                    <IconRefresh
                      className={cn(
                        "size-4",
                        agentsQuery.isFetching && "animate-spin",
                      )}
                    />
                    {t("pages.reviews.policies.retry_agents", "Retry agents")}
                  </Button>
                </div>
              )}
              {agentGenerationMismatch && (
                <div
                  role="alert"
                  className="border-destructive/40 bg-destructive/5 text-destructive flex flex-col gap-2 rounded-lg border p-3 text-sm sm:flex-row sm:items-center sm:justify-between"
                >
                  <span>
                    {agentRevisionConflict
                      ? t(
                          "pages.reviews.policies.agent_revision_conflict",
                          "The configuration changed while loading agents. Your draft is preserved; reload the latest policies.",
                        )
                      : t(
                          "pages.reviews.policies.agent_generation_mismatch",
                          "Policy and agent catalogs came from different configuration generations. Saving is paused; retry agents or reload the latest policies.",
                        )}
                  </span>
                  <div className="flex flex-wrap gap-2">
                    {!agentRevisionConflict && (
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        disabled={agentsQuery.isFetching}
                        onClick={() => void agentsQuery.refetch()}
                      >
                        <IconRefresh
                          className={cn(
                            "size-4",
                            agentsQuery.isFetching && "animate-spin",
                          )}
                        />
                        {t(
                          "pages.reviews.policies.retry_agents",
                          "Retry agents",
                        )}
                      </Button>
                    )}
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={requestReload}
                    >
                      {t(
                        "pages.reviews.policies.reload_policies",
                        "Reload policies",
                      )}
                    </Button>
                  </div>
                </div>
              )}
              {(agentsUsable ||
                (agentCursor !== undefined && !agentRevisionConflict)) && (
                <div
                  role="group"
                  aria-label={t(
                    "pages.reviews.policies.agent_pages",
                    "AI agent catalog pages",
                  )}
                  className="border-border bg-card flex flex-wrap items-center justify-between gap-2 rounded-lg border p-3 text-sm"
                >
                  <span className="text-muted-foreground">
                    {t("pages.reviews.policies.agent_page", {
                      defaultValue:
                        "AI agent page {{page}} · {{count}} identities",
                      page: agentPageNumber,
                      count: agentsUsable
                        ? (agentsQuery.data?.agents.length ?? 0)
                        : 0,
                    })}
                  </span>
                  <div className="flex gap-2">
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      disabled={
                        agentCursor === undefined || agentsQuery.isFetching
                      }
                      onClick={() => setAgentCursor(previousAgentCursor)}
                    >
                      {t(
                        "pages.reviews.policies.previous_agents",
                        "Previous agents",
                      )}
                    </Button>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      disabled={
                        !agentsUsable ||
                        agentsQuery.isFetching ||
                        agentsQuery.data?.next_cursor === undefined
                      }
                      onClick={() =>
                        setAgentCursor(agentsQuery.data?.next_cursor)
                      }
                    >
                      {t("pages.reviews.policies.next_agents", "Next agents")}
                    </Button>
                  </div>
                </div>
              )}
              {displayedValidation != null && !displayedValidation.valid && (
                <div
                  role="alert"
                  className="border-destructive/40 bg-destructive/5 rounded-lg border p-3"
                >
                  <p className="text-destructive text-sm font-medium">
                    {t(
                      "pages.reviews.policies.validation_title",
                      "Fix policy errors before saving.",
                    )}
                  </p>
                  <ul className="text-muted-foreground mt-2 space-y-1 text-xs">
                    {displayedValidation.issues.slice(0, 24).map((issue) => (
                      <li
                        key={`${issue.path}:${issue.code}`}
                        className="min-w-0 [overflow-wrap:anywhere]"
                      >
                        <span className="text-foreground font-mono break-all">
                          {reviewAttentionIssueLocation(draft, issue.path)}
                        </span>{" "}
                        — {issue.message}
                      </li>
                    ))}
                  </ul>
                  {displayedValidation.issues.length > 24 && (
                    <p className="text-muted-foreground mt-2 text-xs">
                      {t("pages.reviews.policies.more_errors", {
                        defaultValue: "{{count}} additional errors are hidden.",
                        count: displayedValidation.issues.length - 24,
                      })}
                    </p>
                  )}
                </div>
              )}

              <div className="grid min-h-[480px] min-w-0 gap-3 lg:grid-cols-[minmax(250px,0.42fr)_minmax(0,1.58fr)]">
                <aside
                  aria-label={t(
                    "pages.reviews.policies.scopes",
                    "Policy scopes",
                  )}
                  className="border-border bg-card flex min-w-0 flex-col rounded-xl border"
                >
                  <div className="border-border space-y-2 border-b p-3">
                    <Label htmlFor="policy-repository-search">
                      {t(
                        "pages.reviews.policies.repository_search",
                        "Find repository",
                      )}
                    </Label>
                    <Input
                      id="policy-repository-search"
                      type="search"
                      value={repositorySearch}
                      onChange={(event) =>
                        setRepositorySearch(event.target.value)
                      }
                      placeholder="owner/repository"
                    />
                  </div>
                  <div className="min-h-0 flex-1 space-y-1 overflow-auto p-2">
                    <Button
                      type="button"
                      aria-pressed={selectedScope === "global"}
                      variant={
                        selectedScope === "global" ? "secondary" : "ghost"
                      }
                      className="w-full justify-start"
                      onClick={selectGlobal}
                    >
                      {t(
                        "pages.reviews.policies.global_defaults",
                        "Global defaults",
                      )}
                      <Badge variant="outline" className="ml-auto">
                        {draft.global.length}
                      </Badge>
                    </Button>
                    {visibleRepositories.map((repository) => (
                      <Button
                        key={repository.editorKey}
                        type="button"
                        aria-pressed={selectedScope === repository.editorKey}
                        variant={
                          selectedScope === repository.editorKey
                            ? "secondary"
                            : "ghost"
                        }
                        className="w-full min-w-0 justify-start"
                        onClick={() => {
                          setSelectedScope(repository.editorKey)
                          setPolicyPage(0)
                        }}
                      >
                        <span className="truncate font-mono text-xs">
                          {repository.repository ||
                            t(
                              "pages.reviews.policies.unnamed_repository",
                              "Unnamed repository",
                            )}
                        </span>
                        <Badge variant="outline" className="ml-auto">
                          {repository.policies.length}
                        </Badge>
                      </Button>
                    ))}
                  </div>
                  <div className="border-border border-t p-2">
                    <Button
                      type="button"
                      variant="outline"
                      className="w-full"
                      onClick={addRepository}
                    >
                      <IconPlus className="size-4" />
                      {t(
                        "pages.reviews.policies.add_repository",
                        "Add repository",
                      )}
                    </Button>
                  </div>
                </aside>

                <section
                  aria-label={
                    selectedRepository == null
                      ? t(
                          "pages.reviews.policies.global_defaults",
                          "Global defaults",
                        )
                      : selectedRepository.repository ||
                        t(
                          "pages.reviews.policies.unnamed_repository",
                          "Unnamed repository",
                        )
                  }
                  className="min-w-0 space-y-3"
                >
                  <div className="border-border bg-card flex flex-col gap-3 rounded-xl border p-3 sm:flex-row sm:items-end">
                    <div className="min-w-0 flex-1">
                      {selectedRepository == null ? (
                        <>
                          <h2 className="font-semibold">
                            {t(
                              "pages.reviews.policies.global_defaults",
                              "Global defaults",
                            )}
                          </h2>
                          <p className="text-muted-foreground mt-1 text-xs">
                            {t(
                              "pages.reviews.policies.global_help",
                              "These ordered policies apply to every repository unless a repository override changes them.",
                            )}
                          </p>
                        </>
                      ) : (
                        <>
                          <Label htmlFor="selected-policy-repository">
                            {t(
                              "pages.reviews.policies.repository",
                              "Repository",
                            )}
                          </Label>
                          <Input
                            id="selected-policy-repository"
                            aria-invalid={selectedRepositoryIssue != null}
                            aria-describedby={
                              selectedRepositoryIssue == null
                                ? undefined
                                : "selected-policy-repository-error"
                            }
                            value={selectedRepository.repository}
                            onChange={(event) =>
                              updateSelectedRepository((repository) => ({
                                ...repository,
                                repository: event.target.value,
                              }))
                            }
                            placeholder="owner/repository"
                            spellCheck={false}
                            autoComplete="off"
                          />
                          <ReviewAttentionFieldIssue
                            id="selected-policy-repository-error"
                            issue={selectedRepositoryIssue}
                          />
                        </>
                      )}
                    </div>
                    {selectedRepository != null && (
                      <Button
                        type="button"
                        variant="destructive"
                        onClick={() =>
                          removeRepository(selectedRepository.editorKey)
                        }
                      >
                        <IconTrash className="size-4" />
                        {t(
                          "pages.reviews.policies.remove_repository",
                          "Remove repository",
                        )}
                      </Button>
                    )}
                    <Button type="button" variant="outline" onClick={addPolicy}>
                      <IconPlus className="size-4" />
                      {t(
                        "pages.reviews.policies.add_decision",
                        "Add decision policy",
                      )}
                    </Button>
                  </div>

                  {policyPageCount > 1 && (
                    <div
                      role="group"
                      aria-label={t(
                        "pages.reviews.policies.policy_pages",
                        "Decision policy pages",
                      )}
                      className="border-border bg-card flex flex-wrap items-center justify-between gap-2 rounded-lg border p-3 text-sm"
                    >
                      <span className="text-muted-foreground">
                        {t("pages.reviews.policies.policy_page", {
                          defaultValue:
                            "Decision policies {{first}}–{{last}} of {{count}}",
                          first: policyPageStart + 1,
                          last: Math.min(
                            policies.length,
                            policyPageStart + policyEditorPageSize,
                          ),
                          count: policies.length,
                        })}
                      </span>
                      <div className="flex gap-2">
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          disabled={activePolicyPage === 0}
                          onClick={() => setPolicyPage(activePolicyPage - 1)}
                        >
                          {t(
                            "pages.reviews.policies.previous_policies",
                            "Previous policies",
                          )}
                        </Button>
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          disabled={activePolicyPage + 1 === policyPageCount}
                          onClick={() => setPolicyPage(activePolicyPage + 1)}
                        >
                          {t(
                            "pages.reviews.policies.next_policies",
                            "Next policies",
                          )}
                        </Button>
                      </div>
                    </div>
                  )}

                  {policies.length === 0 ? (
                    <div className="border-border bg-card rounded-xl border border-dashed p-8 text-center">
                      <p className="text-sm font-medium">
                        {t(
                          "pages.reviews.policies.empty",
                          "No decision policies are configured in this scope.",
                        )}
                      </p>
                      <Button
                        type="button"
                        variant="outline"
                        className="mt-3"
                        onClick={addPolicy}
                      >
                        <IconPlus className="size-4" />
                        {t(
                          "pages.reviews.policies.add_decision",
                          "Add decision policy",
                        )}
                      </Button>
                    </div>
                  ) : (
                    visiblePolicies.map((policy, visiblePolicyIndex) => (
                      <PolicyEditorCard
                        key={policy.editorKey}
                        policy={policy}
                        policyIndex={policyPageStart + visiblePolicyIndex}
                        policyPath={
                          selectedRepository == null
                            ? `global[${policyPageStart + visiblePolicyIndex}]`
                            : `repositories[${selectedRepositoryIndex}].policies[${policyPageStart + visiblePolicyIndex}]`
                        }
                        issues={validationIssues}
                        repository={selectedRepository?.repository}
                        agents={
                          agentsUsable ? (agentsQuery.data?.agents ?? []) : []
                        }
                        agentsLoading={
                          agentsQuery.isPending || agentsQuery.isFetching
                        }
                        agentsUnavailable={
                          agentsQuery.isError || agentGenerationMismatch
                        }
                        defaultAgentID={defaultAgentID}
                        catalog={displayedValidation?.catalog}
                        nextEditorKey={nextEditorKey}
                        onSelectAgent={trustSelectedAgent}
                        onChange={(next) =>
                          updatePolicy(policy.editorKey, next)
                        }
                        onRemove={() => removePolicy(policy.editorKey)}
                        onModeChange={(mode) =>
                          requestModeChange(
                            policy as ReviewAttentionRepositoryPolicyDraft,
                            mode,
                          )
                        }
                      />
                    ))
                  )}
                </section>
              </div>
            </div>
          </div>
        </fieldset>
      </form>

      <AlertDialog open={reloadConfirmOpen} onOpenChange={setReloadConfirmOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(
                "pages.reviews.policies.reload_title",
                "Discard this policy draft?",
              )}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(
                "pages.reviews.policies.reload_description",
                "Reloading replaces every unsaved policy edit with the latest trusted configuration.",
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>
              {t("common.cancel", "Cancel")}
            </AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={reloadLatest}>
              {t("pages.reviews.policies.discard_reload", "Discard and reload")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={discardNavigationOpen}
        onOpenChange={changeDiscardNavigationOpen}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(
                "pages.reviews.policies.leave_title",
                "Discard unsaved policy changes?",
              )}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(
                "pages.reviews.policies.leave_description",
                "Your memory-only draft will be lost if you leave this page.",
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>
              {t("pages.reviews.policies.keep_editing", "Keep editing")}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={discardAndNavigate}
              disabled={navigationBusy}
            >
              {t("pages.reviews.policies.discard", "Discard changes")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={pendingMode != null}
        onOpenChange={(open) => {
          if (!open) setPendingMode(null)
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(
                "pages.reviews.policies.mode_title",
                "Remove this override's gates?",
              )}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(
                "pages.reviews.policies.mode_description",
                "Inherit and disable modes cannot keep repository gates. This removes them from the draft.",
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>
              {t("common.cancel", "Cancel")}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={confirmModeChange}
            >
              {t("pages.reviews.policies.remove_gates", "Remove gates")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function PolicyEditorCard({
  policy,
  policyIndex,
  policyPath,
  issues,
  repository,
  agents,
  agentsLoading,
  agentsUnavailable,
  defaultAgentID,
  catalog,
  nextEditorKey,
  onSelectAgent,
  onChange,
  onRemove,
  onModeChange,
}: {
  policy: EditablePolicy
  policyIndex: number
  policyPath: string
  issues: readonly ReviewAttentionPolicyIssue[]
  repository?: string
  agents: ReviewAttentionAgent[]
  agentsLoading: boolean
  agentsUnavailable: boolean
  defaultAgentID: string
  catalog?: Parameters<typeof resolveReviewAttentionPolicy>[0]
  nextEditorKey: (prefix: string) => string
  onSelectAgent: (agentID: string) => void
  onChange: (policy: EditablePolicy) => void
  onRemove: () => void
  onModeChange: (mode: ReviewAttentionPolicyMode) => void
}) {
  const { t } = useTranslation()
  const repositoryPolicy = "mode" in policy ? policy : null
  const gatesEditable =
    repositoryPolicy == null ||
    repositoryPolicy.mode === "overlay" ||
    repositoryPolicy.mode === "replace"
  const preview =
    catalog != null && repository != null && policy.decisionPoint !== ""
      ? resolveReviewAttentionPolicy(catalog, repository, policy.decisionPoint)
      : null
  const decisionPointIssue = findReviewAttentionPolicyIssue(
    issues,
    `${policyPath}.decisionPoint`,
  )
  const modeIssue = findReviewAttentionPolicyIssue(issues, `${policyPath}.mode`)
  const gatesIssue = findReviewAttentionPolicyIssue(
    issues,
    `${policyPath}.gates`,
  )

  const updateGate = (gateKey: string, next: ReviewAttentionGateDraft) => {
    onChange({
      ...policy,
      gates: policy.gates.map((gate) =>
        gate.editorKey === gateKey ? next : gate,
      ),
    })
  }

  const addGate = () => {
    onChange({
      ...policy,
      gates: [
        ...policy.gates,
        createReviewAttentionGateDraft("zero", defaultAgentID, nextEditorKey),
      ],
    })
  }

  return (
    <article
      aria-label={t("pages.reviews.policies.decision_aria", {
        defaultValue: "Decision policy {{number}}",
        number: policyIndex + 1,
      })}
      className="border-border bg-card min-w-0 space-y-4 rounded-xl border p-3 sm:p-4"
    >
      <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
        <div className="min-w-0 flex-1">
          <Label htmlFor={`${policy.editorKey}-decision`}>
            {t("pages.reviews.policies.decision_point", "Decision point")}
          </Label>
          <Input
            id={`${policy.editorKey}-decision`}
            aria-invalid={decisionPointIssue != null}
            aria-describedby={
              decisionPointIssue == null
                ? undefined
                : `${policy.editorKey}-decision-error`
            }
            value={policy.decisionPoint}
            list={`${policy.editorKey}-decision-presets`}
            onChange={(event) =>
              onChange({ ...policy, decisionPoint: event.target.value })
            }
            placeholder="review.submitted"
            spellCheck={false}
            autoComplete="off"
          />
          <datalist id={`${policy.editorKey}-decision-presets`}>
            {knownAttentionDecisionPoints.map((decisionPoint) => (
              <option
                key={decisionPoint.value}
                value={decisionPoint.value}
                label={t(decisionPoint.labelKey, decisionPoint.label)}
              />
            ))}
          </datalist>
          <p className="text-muted-foreground mt-1 text-xs">
            {t(
              "pages.reviews.policies.decision_point_help",
              "Choose a known product decision or enter a custom workflow decision point.",
            )}
          </p>
          {policy.decisionPoint === "pr_development.before_push" && (
            <p className="text-muted-foreground mt-1 text-xs">
              {t(
                "pages.reviews.policies.before_push_owner_help",
                "For a working-context gate here, select the same agent that owns this PR's local development. An owner mismatch fails closed before publication.",
              )}
            </p>
          )}
          <ReviewAttentionFieldIssue
            id={`${policy.editorKey}-decision-error`}
            issue={decisionPointIssue}
          />
        </div>
        {repositoryPolicy != null && (
          <div className="min-w-[180px]">
            <Label htmlFor={`${policy.editorKey}-mode`}>
              {t("pages.reviews.policies.override_mode", "Override mode")}
            </Label>
            <select
              id={`${policy.editorKey}-mode`}
              aria-invalid={modeIssue != null}
              aria-describedby={
                modeIssue == null ? undefined : `${policy.editorKey}-mode-error`
              }
              value={repositoryPolicy.mode}
              onChange={(event) =>
                onModeChange(event.target.value as ReviewAttentionPolicyMode)
              }
              className="border-input bg-background focus-visible:border-ring focus-visible:ring-ring/25 h-9 w-full rounded-lg border px-3 text-sm outline-none focus-visible:ring-2"
            >
              <option value="inherit">Inherit</option>
              <option value="overlay">Overlay</option>
              <option value="replace">Replace</option>
              <option value="disable">Disable</option>
            </select>
            <ReviewAttentionFieldIssue
              id={`${policy.editorKey}-mode-error`}
              issue={modeIssue}
            />
          </div>
        )}
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label={t("pages.reviews.policies.remove_decision", {
            defaultValue: "Remove decision policy {{number}}",
            number: policyIndex + 1,
          })}
          title={t("pages.reviews.policies.remove_decision_short", "Remove")}
          onClick={onRemove}
        >
          <IconTrash className="size-4" />
        </Button>
      </div>

      {repositoryPolicy != null && (
        <p className="text-muted-foreground text-xs">
          {modeDescription(repositoryPolicy.mode, t)}
        </p>
      )}

      {gatesEditable && (
        <div
          className="space-y-3"
          aria-describedby={
            gatesIssue == null ? undefined : `${policy.editorKey}-gates-error`
          }
        >
          <ReviewAttentionFieldIssue
            id={`${policy.editorKey}-gates-error`}
            issue={gatesIssue}
          />
          <div className="flex items-center justify-between gap-2">
            <div>
              <h3 className="text-sm font-semibold">
                {t("pages.reviews.policies.gates", "Ordered gates")}
              </h3>
              <p className="text-muted-foreground text-xs">
                {t(
                  "pages.reviews.policies.gates_help",
                  "Add two or more gates to evaluate the same decision in configured order.",
                )}
              </p>
            </div>
            <Button type="button" variant="outline" size="sm" onClick={addGate}>
              <IconPlus className="size-4" />
              {t("pages.reviews.policies.add_gate", "Add gate")}
            </Button>
          </div>
          {policy.gates.length === 0 ? (
            <p className="border-border text-muted-foreground rounded-lg border border-dashed p-4 text-center text-xs">
              {t(
                "pages.reviews.policies.no_gates",
                "This decision currently has no gates and is a no-op.",
              )}
            </p>
          ) : (
            policy.gates.map((gate, gateIndex) => (
              <GateEditor
                key={gate.editorKey}
                gate={gate}
                gateIndex={gateIndex}
                gateCount={policy.gates.length}
                gatePath={`${policyPath}.gates[${gateIndex}]`}
                issues={issues}
                agents={agents}
                agentsLoading={agentsLoading}
                agentsUnavailable={agentsUnavailable}
                defaultAgentID={defaultAgentID}
                onSelectAgent={onSelectAgent}
                onChange={(next) => updateGate(gate.editorKey, next)}
                onMove={(nextIndex) =>
                  onChange({
                    ...policy,
                    gates: reorderReviewAttentionGates(
                      policy.gates,
                      gateIndex,
                      nextIndex,
                    ),
                  })
                }
                onRemove={() =>
                  onChange({
                    ...policy,
                    gates: policy.gates.filter(
                      (candidate) => candidate.editorKey !== gate.editorKey,
                    ),
                  })
                }
              />
            ))
          )}
        </div>
      )}

      {preview != null && (
        <div className="border-border bg-muted/35 rounded-lg border p-3">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="text-sm font-semibold">
              {t(
                "pages.reviews.policies.effective_preview",
                "Effective repository policy",
              )}
            </h3>
            {preview.noop && <Badge variant="outline">No-op</Badge>}
          </div>
          {preview.entries.length === 0 ? (
            <p className="text-muted-foreground mt-1 text-xs">
              {t(
                "pages.reviews.policies.effective_empty",
                "No gate will request attention for this decision.",
              )}
            </p>
          ) : (
            <ol
              aria-label={t(
                "pages.reviews.policies.effective_gate_order",
                "Resolved gate order",
              )}
              className="mt-2 flex min-w-0 flex-wrap gap-2"
            >
              {preview.entries.map((entry) => (
                <li
                  key={`${entry.effectivePosition}:${entry.gate.id}`}
                  aria-label={`${entry.effectivePosition}. ${entry.gate.id} ${entry.gate.kind} ${entry.action}`}
                  className="border-border bg-background flex max-w-full min-w-0 flex-wrap items-center gap-2 rounded-md border px-2 py-1 text-xs"
                >
                  <span className="text-muted-foreground shrink-0">
                    {entry.effectivePosition}.
                  </span>
                  <span className="min-w-0 font-mono break-all">
                    {entry.gate.id}
                  </span>
                  <Badge variant="secondary" className="shrink-0">
                    {entry.gate.kind}
                  </Badge>
                  <Badge variant="outline" className="shrink-0">
                    {entry.action}
                  </Badge>
                </li>
              ))}
            </ol>
          )}
        </div>
      )}
    </article>
  )
}

function GateEditor({
  gate,
  gateIndex,
  gateCount,
  gatePath,
  issues,
  agents,
  agentsLoading,
  agentsUnavailable,
  defaultAgentID,
  onSelectAgent,
  onChange,
  onMove,
  onRemove,
}: {
  gate: ReviewAttentionGateDraft
  gateIndex: number
  gateCount: number
  gatePath: string
  issues: readonly ReviewAttentionPolicyIssue[]
  agents: ReviewAttentionAgent[]
  agentsLoading: boolean
  agentsUnavailable: boolean
  defaultAgentID: string
  onSelectAgent: (agentID: string) => void
  onChange: (gate: ReviewAttentionGateDraft) => void
  onMove: (index: number) => void
  onRemove: () => void
}) {
  const { t } = useTranslation()
  const fieldID = (field: string) => `${gate.editorKey}-${field}`
  const fieldIssue = (field: string) =>
    findReviewAttentionPolicyIssue(issues, `${gatePath}.${field}`)
  const idIssue = fieldIssue("id")
  const kindIssue = fieldIssue("kind")
  const agentIssue = fieldIssue("agentID")
  const criteriaIssue = fieldIssue("criteria")
  const titleIssue = fieldIssue("title")
  const conditionIssue = fieldIssue("when")
  const questionsIssue = fieldIssue("questionsSource")
  const selectedAgentID =
    gate.kind === "ai_working_context" || gate.kind === "ai_isolated_context"
      ? gate.agentID
      : ""
  const agentOptions =
    selectedAgentID !== "" &&
    !agents.some((agent) => agent.id === selectedAgentID)
      ? [...agents, { id: selectedAgentID, name: "" }]
      : agents
  const gateKindHelp = {
    ai_working_context: t(
      "pages.reviews.policies.ai_working_context_help",
      "Uses the AI already working on the problem and its current context. It asks you only when that AI decides your input is needed.",
    ),
    ai_isolated_context: t(
      "pages.reviews.policies.ai_isolated_context_help",
      "Uses a separate private AI context over the code, findings, and other inputs supplied to this decision.",
    ),
    deterministic: t(
      "pages.reviews.policies.deterministic_help",
      "Evaluates the configured condition without AI and asks the configured questions only when it matches. Decision data is available under inputs.gate_subject.",
    ),
    zero: t(
      "pages.reviews.policies.zero_help",
      "A zero gate is an explicit no-op. In an overlay it can tombstone a global gate with the same ID.",
    ),
  }[gate.kind]
  return (
    <fieldset className="border-border min-w-0 space-y-3 rounded-lg border p-3">
      <legend className="px-1 text-xs font-medium">
        {t("pages.reviews.policies.gate_number", {
          defaultValue: "Gate {{number}}",
          number: gateIndex + 1,
        })}
      </legend>
      <div className="grid min-w-0 gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(190px,0.7fr)_auto] sm:items-end">
        <div>
          <Label htmlFor={fieldID("id")}>
            {t("pages.reviews.policies.gate_id", "Gate ID")}
          </Label>
          <Input
            id={fieldID("id")}
            aria-invalid={idIssue != null}
            aria-describedby={idIssue == null ? undefined : fieldID("id-error")}
            value={gate.id}
            onChange={(event) => onChange({ ...gate, id: event.target.value })}
            placeholder="ask_owner"
            spellCheck={false}
            autoComplete="off"
          />
          <ReviewAttentionFieldIssue id={fieldID("id-error")} issue={idIssue} />
        </div>
        <div>
          <Label htmlFor={fieldID("kind")}>
            {t("pages.reviews.policies.gate_type", "Gate type")}
          </Label>
          <select
            id={fieldID("kind")}
            aria-invalid={kindIssue != null}
            aria-describedby={
              kindIssue == null ? undefined : fieldID("kind-error")
            }
            value={gate.kind}
            onChange={(event) =>
              onChange(
                convertReviewAttentionGateKind(
                  gate,
                  event.target.value as ReviewAttentionGateKind,
                  defaultAgentID,
                ),
              )
            }
            className="border-input bg-background focus-visible:border-ring focus-visible:ring-ring/25 h-9 w-full rounded-lg border px-3 text-sm outline-none focus-visible:ring-2"
          >
            <option value="ai_working_context">AI · working context</option>
            <option value="ai_isolated_context">AI · isolated context</option>
            <option value="deterministic">Deterministic</option>
            <option value="zero">Zero / no-op</option>
          </select>
          <ReviewAttentionFieldIssue
            id={fieldID("kind-error")}
            issue={kindIssue}
          />
        </div>
        <div className="flex gap-1">
          <Button
            type="button"
            variant="ghost"
            size="icon"
            aria-label={t(
              "pages.reviews.policies.move_gate_up",
              "Move gate up",
            )}
            disabled={gateIndex === 0}
            onClick={() => onMove(gateIndex - 1)}
          >
            <IconArrowUp className="size-4" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon"
            aria-label={t(
              "pages.reviews.policies.move_gate_down",
              "Move gate down",
            )}
            disabled={gateIndex + 1 === gateCount}
            onClick={() => onMove(gateIndex + 1)}
          >
            <IconArrowDown className="size-4" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon"
            aria-label={t("pages.reviews.policies.remove_gate", "Remove gate")}
            onClick={onRemove}
          >
            <IconTrash className="size-4" />
          </Button>
        </div>
      </div>

      <p className="text-muted-foreground text-xs">{gateKindHelp}</p>

      {(gate.kind === "ai_working_context" ||
        gate.kind === "ai_isolated_context") && (
        <>
          <div className="grid gap-3 sm:grid-cols-2">
            <div>
              <Label htmlFor={fieldID("agent")}>
                {t("pages.reviews.policies.agent", "AI agent")}
              </Label>
              <select
                id={fieldID("agent")}
                aria-invalid={agentIssue != null}
                aria-describedby={
                  agentIssue == null ? undefined : fieldID("agent-error")
                }
                value={gate.agentID}
                onChange={(event) => {
                  onSelectAgent(event.target.value)
                  onChange({ ...gate, agentID: event.target.value })
                }}
                disabled={agentsLoading || agentsUnavailable}
                className="border-input bg-background focus-visible:border-ring focus-visible:ring-ring/25 h-9 w-full rounded-lg border px-3 text-sm outline-none focus-visible:ring-2 disabled:opacity-50"
              >
                <option value="">
                  {agentsLoading
                    ? t(
                        "pages.reviews.policies.loading_agents",
                        "Loading agents…",
                      )
                    : agentsUnavailable
                      ? t(
                          "pages.reviews.policies.agents_unavailable",
                          "Agents unavailable",
                        )
                      : t(
                          "pages.reviews.policies.select_agent",
                          "Select an agent",
                        )}
                </option>
                {agentOptions.map((agent) => (
                  <option key={agent.id} value={agent.id}>
                    {agent.name || agent.id} ({agent.id})
                  </option>
                ))}
              </select>
              <ReviewAttentionFieldIssue
                id={fieldID("agent-error")}
                issue={agentIssue}
              />
            </div>
            <div>
              <Label htmlFor={fieldID("title")}>
                {t("pages.reviews.policies.attention_title", "Attention title")}
              </Label>
              <Input
                id={fieldID("title")}
                aria-invalid={titleIssue != null}
                aria-describedby={
                  titleIssue == null ? undefined : fieldID("title-error")
                }
                value={gate.title}
                onChange={(event) =>
                  onChange({ ...gate, title: event.target.value })
                }
                placeholder="Owner input may be needed"
              />
              <ReviewAttentionFieldIssue
                id={fieldID("title-error")}
                issue={titleIssue}
              />
            </div>
          </div>
          <div>
            <Label htmlFor={fieldID("criteria")}>
              {t("pages.reviews.policies.criteria", "What AI should look for")}
            </Label>
            <Textarea
              id={fieldID("criteria")}
              aria-invalid={criteriaIssue != null}
              aria-describedby={
                criteriaIssue == null ? undefined : fieldID("criteria-error")
              }
              value={gate.criteria}
              onChange={(event) =>
                onChange({ ...gate, criteria: event.target.value })
              }
              placeholder="Explain when this decision is better made with the repository owner's attention."
              rows={3}
            />
            <ReviewAttentionFieldIssue
              id={fieldID("criteria-error")}
              issue={criteriaIssue}
            />
          </div>
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={gate.questionsSource != null}
              onChange={(event) =>
                onChange({
                  ...gate,
                  questionsSource: event.target.checked ? "{}" : null,
                })
              }
            />
            {t(
              "pages.reviews.policies.include_questions",
              "Include structured question guidance",
            )}
          </label>
          {gate.questionsSource != null && (
            <QuestionEditor
              id={fieldID("questions")}
              value={gate.questionsSource}
              required={false}
              issue={questionsIssue}
              onChange={(questionsSource) =>
                onChange({ ...gate, questionsSource })
              }
            />
          )}
        </>
      )}

      {gate.kind === "deterministic" && (
        <>
          <div className="grid gap-3 sm:grid-cols-2">
            <div>
              <Label htmlFor={fieldID("when")}>
                {t(
                  "pages.reviews.policies.condition",
                  "Deterministic condition",
                )}
              </Label>
              <Input
                id={fieldID("when")}
                aria-invalid={conditionIssue != null}
                aria-describedby={
                  conditionIssue == null ? undefined : fieldID("when-error")
                }
                value={gate.when}
                onChange={(event) =>
                  onChange({ ...gate, when: event.target.value })
                }
                placeholder="true"
                spellCheck={false}
              />
              <ReviewAttentionFieldIssue
                id={fieldID("when-error")}
                issue={conditionIssue}
              />
            </div>
            <div>
              <Label htmlFor={fieldID("title")}>
                {t("pages.reviews.policies.attention_title", "Attention title")}
              </Label>
              <Input
                id={fieldID("title")}
                aria-invalid={titleIssue != null}
                aria-describedby={
                  titleIssue == null ? undefined : fieldID("title-error")
                }
                value={gate.title}
                onChange={(event) =>
                  onChange({ ...gate, title: event.target.value })
                }
                placeholder="A deterministic condition needs attention"
              />
              <ReviewAttentionFieldIssue
                id={fieldID("title-error")}
                issue={titleIssue}
              />
            </div>
          </div>
          <QuestionEditor
            id={fieldID("questions")}
            value={gate.questionsSource}
            required
            issue={questionsIssue}
            onChange={(questionsSource) =>
              onChange({ ...gate, questionsSource })
            }
          />
        </>
      )}
    </fieldset>
  )
}

function QuestionEditor({
  id,
  value,
  required,
  issue,
  onChange,
}: {
  id: string
  value: string
  required: boolean
  issue?: ReviewAttentionPolicyIssue
  onChange: (value: string) => void
}) {
  const { t } = useTranslation()
  return (
    <div>
      <Label htmlFor={id}>
        {required
          ? t("pages.reviews.policies.questions_required", "Questions JSON")
          : t(
              "pages.reviews.policies.questions_optional",
              "Questions JSON (optional)",
            )}
      </Label>
      <Textarea
        id={id}
        aria-invalid={issue != null}
        aria-describedby={
          issue == null ? `${id}-help` : `${id}-help ${id}-error`
        }
        value={value}
        onChange={(event) => onChange(event.target.value)}
        rows={4}
        required={required}
        spellCheck={false}
        className="font-mono text-xs"
      />
      <p id={`${id}-help`} className="text-muted-foreground mt-1 text-xs">
        {t(
          "pages.reviews.policies.questions_help",
          "JSON numbers and case-sensitive object keys are preserved exactly.",
        )}
      </p>
      <ReviewAttentionFieldIssue id={`${id}-error`} issue={issue} />
    </div>
  )
}

function findReviewAttentionPolicyIssue(
  issues: readonly ReviewAttentionPolicyIssue[],
  path: string,
): ReviewAttentionPolicyIssue | undefined {
  return issues.find((issue) => issue.path === path)
}

function reviewAttentionIssueLocation(
  draft: ReviewAttentionPolicyDraft,
  path: string,
): string {
  const globalMatch = /^global\[(\d+)](?:\.gates\[(\d+)])?(?:\.(\w+))?/.exec(
    path,
  )
  if (globalMatch != null) {
    const policyIndex = Number(globalMatch[1])
    const policy = draft.global[policyIndex]
    const parts = [
      "Global",
      policy?.decisionPoint || `decision ${policyIndex + 1}`,
    ]
    if (globalMatch[2] !== undefined) {
      const gateIndex = Number(globalMatch[2])
      parts.push(policy?.gates[gateIndex]?.id || `gate ${gateIndex + 1}`)
    }
    if (globalMatch[3] !== undefined) parts.push(globalMatch[3])
    return parts.join(" · ")
  }

  const repositoryMatch =
    /^repositories\[(\d+)](?:\.policies\[(\d+)])?(?:\.gates\[(\d+)])?(?:\.(\w+))?/.exec(
      path,
    )
  if (repositoryMatch != null) {
    const repositoryIndex = Number(repositoryMatch[1])
    const repository = draft.repositories[repositoryIndex]
    const parts = [
      "Repository",
      repository?.repository || `scope ${repositoryIndex + 1}`,
    ]
    if (repositoryMatch[2] !== undefined) {
      const policyIndex = Number(repositoryMatch[2])
      const policy = repository?.policies[policyIndex]
      parts.push(policy?.decisionPoint || `decision ${policyIndex + 1}`)
      if (repositoryMatch[3] !== undefined) {
        const gateIndex = Number(repositoryMatch[3])
        parts.push(policy?.gates[gateIndex]?.id || `gate ${gateIndex + 1}`)
      }
    }
    const field = repositoryMatch[4]
    if (field !== undefined) parts.push(field)
    return parts.join(" · ")
  }
  return path === "catalog" ? "Policy catalog" : path
}

function ReviewAttentionFieldIssue({
  id,
  issue,
}: {
  id: string
  issue?: ReviewAttentionPolicyIssue
}) {
  if (issue == null) return null
  return (
    <p id={id} className="text-destructive mt-1 text-xs">
      {issue.message}
    </p>
  )
}

function PolicyPageState({
  title,
  loading = false,
  action,
  onShowInbox,
  onShowDevelopment,
}: {
  title: string
  loading?: boolean
  action?: ReactNode
  onShowInbox: () => void
  onShowDevelopment: () => void
}) {
  const { t } = useTranslation()
  return (
    <div className="bg-background flex h-full min-h-0 flex-col">
      <PageHeader title={t("pages.reviews.title", "Pull request reviews")} />
      <ReviewWorkbenchTabs
        active="policies"
        onChange={(view) => {
          if (view === "inbox") {
            onShowInbox()
          } else if (view === "development") {
            onShowDevelopment()
          }
        }}
      />
      <div className="flex flex-1 flex-col items-center justify-center gap-3 p-6 text-center">
        {loading && (
          <IconLoader2 className="text-muted-foreground size-6 animate-spin" />
        )}
        <p className="text-muted-foreground text-sm">{title}</p>
        {action}
      </div>
    </div>
  )
}

function modeDescription(
  mode: ReviewAttentionPolicyMode,
  t: ReturnType<typeof useTranslation>["t"],
): string {
  switch (mode) {
    case "inherit":
      return t(
        "pages.reviews.policies.mode_inherit_help",
        "Use the global decision policy unchanged.",
      )
    case "overlay":
      return t(
        "pages.reviews.policies.mode_overlay_help",
        "Replace matching global gate IDs in place, then append new repository gates.",
      )
    case "replace":
      return t(
        "pages.reviews.policies.mode_replace_help",
        "Use only this repository's ordered gates.",
      )
    case "disable":
      return t(
        "pages.reviews.policies.mode_disable_help",
        "Request no attention for this repository and decision point.",
      )
  }
}

function collectReviewAttentionAgentIDs(
  draft: ReviewAttentionPolicyDraft | null,
): ReadonlySet<string> {
  const ids = new Set<string>()
  if (draft == null) return ids
  const collect = (gates: readonly ReviewAttentionGateDraft[]) => {
    for (const gate of gates) {
      if (
        (gate.kind === "ai_working_context" ||
          gate.kind === "ai_isolated_context") &&
        gate.agentID !== ""
      ) {
        ids.add(gate.agentID)
      }
    }
  }
  for (const policy of draft.global) collect(policy.gates)
  for (const repository of draft.repositories) {
    for (const policy of repository.policies) collect(policy.gates)
  }
  return ids
}

function previousReviewAttentionAgentCursor(
  cursor: string | undefined,
): string | undefined {
  if (cursor === undefined || cursor === "256") return undefined
  return String(Number(cursor) - 256)
}
