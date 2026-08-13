import {
  IconArrowDown,
  IconArrowUp,
  IconCopy,
  IconDeviceFloppy,
  IconExternalLink,
  IconInfoCircle,
  IconLoader2,
  IconPlus,
  IconRefresh,
  IconStar,
  IconTrash,
} from "@tabler/icons-react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { Link, useBlocker } from "@tanstack/react-router"
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
  type ReviewAttentionPolicySnapshot,
  getReviewAttentionPolicies,
  putReviewAttentionPolicies,
  reviewAttentionBuiltInRuleSetID,
} from "@/api/review-attention-policies"
import { ConfigChangeNotice } from "@/components/config-change-notice"
import { PageHeader } from "@/components/page-header"
import {
  type ReviewAttentionGateDraft,
  type ReviewAttentionPolicyDraft,
  type ReviewAttentionPolicyIssue,
  type ReviewAttentionRuleDraft,
  type ReviewAttentionRuleSetDraft,
  convertReviewAttentionGateKind,
  createReviewAttentionEditorKeyFactory,
  createReviewAttentionGateDraft,
  createReviewAttentionRepositoryAssignmentDraft,
  createReviewAttentionRuleDraft,
  duplicateReviewAttentionRuleSetDraft,
  foldReviewAttentionRuleSetName,
  reorderReviewAttentionGates,
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
import { Textarea } from "@/components/ui/textarea"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { cn } from "@/lib/utils"

const policyQueryKey = ["reviews", "attention-policies"] as const
const agentQueryKey = ["reviews", "attention-policy-agents"] as const
const policyEditorPageSize = 8
const customDecisionPointChoice = "__custom__"
const repositoryNamePattern = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/
const decisionPointPattern = /^[a-z][a-z0-9._-]{0,127}$/
const textEncoder = new TextEncoder()
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

export function ReviewAttentionPoliciesPage({
  onShowInbox,
  onShowDevelopment,
  standalone = false,
}: {
  onShowInbox: () => void
  onShowDevelopment: () => void
  standalone?: boolean
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
  const [selectedRuleSetKey, setSelectedRuleSetKey] = useState("")
  const [assignmentDialogOpen, setAssignmentDialogOpen] = useState(false)
  const [assignmentRepository, setAssignmentRepository] = useState("")
  const [assignmentRuleSetID, setAssignmentRuleSetID] = useState("")
  const [duplicateDialogOpen, setDuplicateDialogOpen] = useState(false)
  const [duplicateSourceKey, setDuplicateSourceKey] = useState("")
  const [duplicateName, setDuplicateName] = useState("")
  const [pendingDeleteRuleSetKey, setPendingDeleteRuleSetKey] = useState("")
  const [ruleDialogOpen, setRuleDialogOpen] = useState(false)
  const [newDecisionChoice, setNewDecisionChoice] = useState("")
  const [newCustomDecisionPoint, setNewCustomDecisionPoint] = useState("")
  const [saving, setSaving] = useState(false)
  const [reloading, setReloading] = useState(false)
  const [serverError, setServerError] = useState("")
  const [conflicted, setConflicted] = useState(false)
  const [reloadConfirmOpen, setReloadConfirmOpen] = useState(false)
  const [discardNavigationOpen, setDiscardNavigationOpen] = useState(false)
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
      setSelectedRuleSetKey(
        nextDraft.ruleSets.find(
          (ruleSet) => ruleSet.id === reviewAttentionBuiltInRuleSetID,
        )?.editorKey ??
          nextDraft.ruleSets[0]?.editorKey ??
          "",
      )
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
  const agentPagingAvailable =
    agentCursor !== undefined || agentsQuery.data?.next_cursor !== undefined
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
  const hasAIGates = useMemo(
    () =>
      draft != null &&
      draft.ruleSets
        .flatMap((ruleSet) => ruleSet.rules)
        .flatMap((policy) => policy.gates)
        .some(
          (gate) =>
            gate.kind === "ai_working_context" ||
            gate.kind === "ai_isolated_context",
        ),
    [draft],
  )

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
            "The latest rules could not be loaded. Your draft is still preserved.",
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
            "A matching rule and agent configuration generation could not be loaded. Your draft is still preserved; retry the reload.",
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
            "A matching rule and agent configuration generation could not be loaded. Your draft is still preserved; retry the reload.",
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
            "The rules were saved, but a newer configuration generation was observed before the response completed. Your draft is preserved; reload the latest rules.",
          ),
        )
      }
      showSaveSuccessOrRestartToast(
        t,
        t("pages.reviews.policies.saved", "Attention rules saved."),
        t("pages.reviews.policies.name", "attention rules"),
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
            "These rules changed elsewhere. Your draft is preserved; reload the latest version before saving.",
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
              "These rules changed elsewhere, but the latest generation could not be loaded. Your draft is preserved; retry the reload before saving.",
            ),
          )
        }
      } else {
        setServerError(
          t(
            "pages.reviews.policies.save_error",
            "The rules could not be saved. Your draft is still preserved.",
          ),
        )
      }
    } finally {
      setSaving(false)
    }
  }

  const validationIssues = displayedValidation?.issues ?? []
  const selectedRuleSet =
    draft?.ruleSets.find((item) => item.editorKey === selectedRuleSetKey) ??
    draft?.ruleSets[0] ??
    null
  const selectedRuleSetIndex =
    draft?.ruleSets.findIndex(
      (item) => item.editorKey === selectedRuleSet?.editorKey,
    ) ?? -1
  const assignedRepositoryCount =
    draft?.repositoryAssignments.filter(
      (assignment) => assignment.ruleSetID === selectedRuleSet?.id,
    ).length ?? 0
  const deleteRuleSetDisabledReason =
    selectedRuleSet?.id === reviewAttentionBuiltInRuleSetID
      ? t(
          "pages.reviews.policies.rule_sets_delete_builtin_help",
          "The built-in Default set cannot be deleted.",
        )
      : selectedRuleSet?.id === draft?.defaultRuleSetID
        ? t(
            "pages.reviews.policies.rule_sets_delete_current_help",
            "Choose another default before deleting this set.",
          )
        : assignedRepositoryCount > 0
          ? t(
              "pages.reviews.policies.rule_sets_delete_assigned_help",
              "Remove its repository assignments before deleting this set.",
            )
          : ""

  const openAssignmentDialog = () => {
    setAssignmentRepository("")
    setAssignmentRuleSetID(selectedRuleSet?.id ?? draft?.defaultRuleSetID ?? "")
    setAssignmentDialogOpen(true)
  }

  const normalizedNewRepository = assignmentRepository.trim()
  const newRepositoryIssue = (() => {
    if (normalizedNewRepository === "") return ""
    if (
      !repositoryNamePattern.test(normalizedNewRepository) ||
      textEncoder.encode(normalizedNewRepository).byteLength > 256
    ) {
      return t(
        "pages.reviews.policies.repository_format_error",
        "Use the exact GitHub owner/repository name with letters, numbers, dot, underscore, or hyphen.",
      )
    }
    if (
      draft?.repositoryAssignments.some(
        (assignment) =>
          assignment.repository.toLowerCase() ===
          normalizedNewRepository.toLowerCase(),
      )
    ) {
      return t(
        "pages.reviews.policies.repository_exists",
        "This repository already has a rule-set assignment.",
      )
    }
    return ""
  })()

  const addAssignment = () => {
    if (
      normalizedNewRepository === "" ||
      newRepositoryIssue !== "" ||
      !draft?.ruleSets.some((item) => item.id === assignmentRuleSetID)
    )
      return
    const assignment =
      createReviewAttentionRepositoryAssignmentDraft(nextEditorKey)
    assignment.repository = normalizedNewRepository
    assignment.ruleSetID = assignmentRuleSetID
    setDraft((current) =>
      current == null
        ? current
        : {
            ...current,
            repositoryAssignments: [
              ...current.repositoryAssignments,
              assignment,
            ],
          },
    )
    setAssignmentRepository("")
  }

  const removeAssignment = (assignmentKey: string) => {
    setDraft((current) =>
      current == null
        ? current
        : {
            ...current,
            repositoryAssignments: current.repositoryAssignments.filter(
              (assignment) => assignment.editorKey !== assignmentKey,
            ),
          },
    )
  }

  const updateSelectedRuleSet = (
    update: (
      ruleSet: ReviewAttentionRuleSetDraft,
    ) => ReviewAttentionRuleSetDraft,
  ) => {
    if (selectedRuleSet == null) return
    setDraft((current) =>
      current == null
        ? current
        : {
            ...current,
            ruleSets: current.ruleSets.map((ruleSet) =>
              ruleSet.editorKey === selectedRuleSet.editorKey
                ? update(ruleSet)
                : ruleSet,
            ),
          },
    )
  }

  const openRuleDialog = () => {
    setNewDecisionChoice("")
    setNewCustomDecisionPoint("")
    setRuleDialogOpen(true)
  }

  const openKnownRuleDialog = (decisionPoint: string) => {
    setNewDecisionChoice(decisionPoint)
    setNewCustomDecisionPoint("")
    setRuleDialogOpen(true)
  }

  const newDecisionPoint =
    newDecisionChoice === customDecisionPointChoice
      ? newCustomDecisionPoint.trim()
      : newDecisionChoice
  const newDecisionIssue = (() => {
    if (newDecisionPoint === "") return ""
    if (
      !decisionPointPattern.test(newDecisionPoint) ||
      textEncoder.encode(newDecisionPoint).byteLength > 128
    ) {
      return t(
        "pages.reviews.policies.custom_decision_error",
        "Use a lowercase identifier beginning with a letter; dots, underscores, and hyphens are allowed.",
      )
    }
    const currentPolicies = selectedRuleSet?.rules
    if (
      currentPolicies?.some(
        (policy) => policy.decisionPoint === newDecisionPoint,
      )
    ) {
      return t(
        "pages.reviews.policies.decision_exists",
        "This rule set already has a rule for that moment.",
      )
    }
    return ""
  })()

  const addPolicy = () => {
    if (draft == null || selectedRuleSet == null) return
    if (newDecisionPoint === "" || newDecisionIssue !== "") return
    const policy = createReviewAttentionRuleDraft(nextEditorKey)
    policy.decisionPoint = newDecisionPoint
    setPolicyPage(
      Math.floor(selectedRuleSet.rules.length / policyEditorPageSize),
    )
    updateSelectedRuleSet((ruleSet) => ({
      ...ruleSet,
      rules: [...ruleSet.rules, policy],
    }))
    setRuleDialogOpen(false)
  }

  const updatePolicy = (policyKey: string, next: ReviewAttentionRuleDraft) => {
    updateSelectedRuleSet((ruleSet) => ({
      ...ruleSet,
      rules: ruleSet.rules.map((policy) =>
        policy.editorKey === policyKey ? next : policy,
      ),
    }))
  }

  const removePolicy = (policyKey: string) => {
    updateSelectedRuleSet((ruleSet) => ({
      ...ruleSet,
      rules: ruleSet.rules.filter((policy) => policy.editorKey !== policyKey),
    }))
  }

  const openDuplicateDialog = (source: ReviewAttentionRuleSetDraft) => {
    setDuplicateSourceKey(source.editorKey)
    setDuplicateName("")
    setDuplicateDialogOpen(true)
  }

  const duplicateSource =
    draft?.ruleSets.find((item) => item.editorKey === duplicateSourceKey) ??
    null
  const normalizedDuplicateName = duplicateName.trim()
  const duplicateNameIssue = (() => {
    if (normalizedDuplicateName === "") return ""
    if (textEncoder.encode(normalizedDuplicateName).byteLength > 128)
      return "Keep the permanent name within 128 bytes."
    if (
      draft?.ruleSets.some(
        (item) =>
          foldReviewAttentionRuleSetName(item.name) ===
          foldReviewAttentionRuleSetName(normalizedDuplicateName),
      )
    )
      return "Rule-set names must be unique."
    return ""
  })()

  const duplicateRuleSet = () => {
    if (
      draft == null ||
      duplicateSource == null ||
      normalizedDuplicateName === "" ||
      duplicateNameIssue !== ""
    )
      return
    const id = createOpaqueRuleSetID(draft.ruleSets)
    const copy = duplicateReviewAttentionRuleSetDraft(
      duplicateSource,
      id,
      normalizedDuplicateName,
      nextEditorKey,
    )
    setDraft({ ...draft, ruleSets: [...draft.ruleSets, copy] })
    setSelectedRuleSetKey(copy.editorKey)
    setPolicyPage(0)
    setDuplicateDialogOpen(false)
  }

  const deletePendingRuleSet = () => {
    if (draft == null || pendingDeleteRuleSetKey === "") return
    const target = draft.ruleSets.find(
      (item) => item.editorKey === pendingDeleteRuleSetKey,
    )
    if (target == null) return
    const targetAssignmentCount = draft.repositoryAssignments.filter(
      (assignment) => assignment.ruleSetID === target.id,
    ).length
    if (
      target.id === draft.defaultRuleSetID ||
      targetAssignmentCount > 0 ||
      target.id === reviewAttentionBuiltInRuleSetID
    )
      return
    const remaining = draft.ruleSets.filter(
      (item) => item.editorKey !== target.editorKey,
    )
    setDraft({ ...draft, ruleSets: remaining })
    setSelectedRuleSetKey(remaining[0]?.editorKey ?? "")
    setPolicyPage(0)
    setPendingDeleteRuleSetKey("")
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
  const legacyMigrationBlocked =
    policyQuery.error instanceof ReviewAttentionPoliciesAPIError &&
    policyQuery.error.code ===
      "legacy_attention_policies_require_simplification"

  if (policyQuery.isPending || draft == null || snapshot == null) {
    if (policyQuery.isError || initialAgentHydrationFailed) {
      return (
        <PolicyPageState
          onShowInbox={onShowInbox}
          onShowDevelopment={onShowDevelopment}
          standalone={standalone}
          title={t(
            legacyMigrationBlocked
              ? "pages.reviews.policies.legacy_migration_error"
              : initialAgentHydrationFailed
                ? "pages.reviews.policies.hydration_error"
                : "pages.reviews.policies.load_error",
            legacyMigrationBlocked
              ? "The legacy attention catalog is valid and still active, but it is too large to convert into standalone rule sets. Simplify repeated repository overrides in configuration before using this editor."
              : initialAgentHydrationFailed
                ? "Attention rules and configured agents could not be loaded as one trusted generation."
                : "Attention rules are unavailable.",
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
        standalone={standalone}
        title={t("pages.reviews.policies.loading", "Loading attention rules…")}
        loading
      />
    )
  }

  const policies = selectedRuleSet?.rules ?? []
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
        title={t("pages.reviews.policies.page_title", "Attention rules")}
        className="h-auto min-h-14 flex-wrap gap-2 py-2 [&>div:last-child]:flex-wrap"
        titleExtra={
          <Badge variant="secondary">
            {t("pages.reviews.policies.badge", "Human input")}
          </Badge>
        }
      >
        {standalone ? (
          <Button
            type="button"
            variant="outline"
            disabled={editorBusy}
            onClick={onShowInbox}
          >
            <IconArrowDown className="size-4 rotate-90" />
            {t("pages.reviews.policies.back", "Pull request work")}
          </Button>
        ) : null}
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
            : t("pages.reviews.policies.rule_sets_save", "Save rule sets")}
        </Button>
      </PageHeader>

      {!standalone ? (
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
      ) : null}

      <form
        id="review-attention-policy-form"
        onSubmit={submit}
        className="flex min-h-0 flex-1 flex-col"
      >
        <fieldset disabled={editorBusy} className="contents">
          <div className="min-h-0 flex-1 overflow-auto p-3 lg:p-4">
            <div className="mx-auto flex w-full max-w-[1500px] flex-col gap-3">
              <section
                aria-labelledby="attention-rules-introduction"
                className="border-border bg-card rounded-xl border"
              >
                <div className="flex flex-col gap-3 p-3 sm:flex-row sm:items-start sm:justify-between sm:p-4">
                  <div className="min-w-0">
                    <h2
                      id="attention-rules-introduction"
                      className="font-semibold"
                    >
                      {t(
                        "pages.reviews.policies.rule_sets_intro_title",
                        "Build reusable attention rule sets",
                      )}
                    </h2>
                    <p className="text-muted-foreground mt-1 max-w-3xl text-sm">
                      {t(
                        "pages.reviews.policies.rule_sets_intro_description",
                        "The built-in Default set always exists and begins with every known workflow moment Off. Configure it directly, or duplicate any set to create another permanent name, then make a set the default or assign it to repositories.",
                      )}
                    </p>
                  </div>
                  <Button type="button" variant="outline" size="sm" asChild>
                    <Link to="/event-sources">
                      {t(
                        "pages.reviews.policies.rule_sets_manage_repositories",
                        "Manage repository intake",
                      )}
                      <IconExternalLink className="size-4" />
                    </Link>
                  </Button>
                </div>
                <ol className="border-border bg-border grid gap-px border-t sm:grid-cols-3">
                  {[
                    [
                      t(
                        "pages.reviews.policies.rule_sets_step_pick",
                        "1 · Pick a set",
                      ),
                      t(
                        "pages.reviews.policies.rule_sets_step_pick_help",
                        "Edit Default, or duplicate a set and give the copy a unique permanent name.",
                      ),
                    ],
                    [
                      t(
                        "pages.reviews.policies.rule_sets_step_configure",
                        "2 · Configure moments",
                      ),
                      t(
                        "pages.reviews.policies.rule_sets_step_configure_help",
                        "Add known or custom workflow moments and the checks that decide whether to pause.",
                      ),
                    ],
                    [
                      t(
                        "pages.reviews.policies.rule_sets_step_apply",
                        "3 · Apply the set",
                      ),
                      t(
                        "pages.reviews.policies.rule_sets_step_apply_help",
                        "Make one set the default and optionally assign another set to exact repositories.",
                      ),
                    ],
                  ].map(([title, description]) => (
                    <li key={title} className="bg-card p-3">
                      <p className="text-xs font-semibold">{title}</p>
                      <p className="text-muted-foreground mt-1 text-xs/5">
                        {description}
                      </p>
                    </li>
                  ))}
                </ol>
                <details className="border-border border-t px-3 py-2 text-xs sm:px-4">
                  <summary className="text-muted-foreground cursor-pointer font-medium">
                    {t(
                      "pages.reviews.policies.rule_sets_save_effect_summary",
                      "What saving changes",
                    )}
                  </summary>
                  <p className="text-muted-foreground mt-2 max-w-5xl leading-5">
                    {t(
                      "pages.reviews.policies.rule_sets_save_effect_help",
                      "Saving replaces the full rule-set catalog and assignments for future decisions that have not already pinned rules. It does not run AI, edit code, run CI, push, acknowledge a review, resolve a thread, or merge a pull request.",
                    )}
                  </p>
                </details>
              </section>
              {snapshot.effects.gateway_effect === "restart_required" && (
                <ConfigChangeNotice
                  kind="restart"
                  title={t(
                    "pages.reviews.policies.restart_required",
                    "The running gateway still needs a restart to use this rule generation.",
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
                        "A newer rule generation is available. Your draft has not been replaced.",
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
                      "Configured agents could not be loaded. AI check validation and saving are paused; your rule draft is preserved.",
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
                          "The configuration changed while loading agents. Your draft is preserved; reload the latest rules.",
                        )
                      : t(
                          "pages.reviews.policies.agent_generation_mismatch",
                          "Rule and agent catalogs came from different configuration generations. Saving is paused; retry agents or reload the latest rules.",
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
                        "Reload rules",
                      )}
                    </Button>
                  </div>
                </div>
              )}
              {hasAIGates &&
                agentPagingAvailable &&
                (agentsUsable ||
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
                      "Fix rule errors before saving.",
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

              <div className="grid min-h-[480px] min-w-0 gap-3 lg:grid-cols-[minmax(280px,0.42fr)_minmax(0,1.58fr)]">
                <aside
                  aria-label={t(
                    "pages.reviews.policies.rule_sets_list_aria",
                    "Rule sets",
                  )}
                  className="border-border bg-card flex min-w-0 flex-col rounded-xl border"
                >
                  <div className="border-border space-y-1 border-b p-3">
                    <h2 className="text-sm font-semibold">
                      {t("pages.reviews.policies.rule_sets_title", "Rule sets")}
                    </h2>
                    <p className="text-muted-foreground text-xs/5">
                      {t(
                        "pages.reviews.policies.rule_sets_help",
                        "Build a reusable set once, then make it the default or assign it to any number of repositories.",
                      )}
                    </p>
                  </div>
                  <div className="min-h-0 flex-1 space-y-1 overflow-auto p-2">
                    {draft.ruleSets.map((ruleSet) => {
                      const assignmentCount =
                        draft.repositoryAssignments.filter(
                          (assignment) => assignment.ruleSetID === ruleSet.id,
                        ).length
                      return (
                        <Button
                          key={ruleSet.editorKey}
                          type="button"
                          aria-pressed={
                            selectedRuleSet?.editorKey === ruleSet.editorKey
                          }
                          variant={
                            selectedRuleSet?.editorKey === ruleSet.editorKey
                              ? "secondary"
                              : "ghost"
                          }
                          className="h-auto w-full min-w-0 justify-start py-2"
                          onClick={() => {
                            setSelectedRuleSetKey(ruleSet.editorKey)
                            setPolicyPage(0)
                          }}
                        >
                          <span className="min-w-0 flex-1 text-left">
                            <span className="block truncate font-medium">
                              {ruleSet.name}
                            </span>
                            <span className="text-muted-foreground block text-xs font-normal">
                              {ruleSet.id === draft.defaultRuleSetID
                                ? t(
                                    "pages.reviews.policies.rule_sets_current_default_short",
                                    "Default for unassigned repositories",
                                  )
                                : t(
                                    "pages.reviews.policies.rule_sets_repository_count",
                                    {
                                      defaultValue:
                                        "{{count}} assigned repository",
                                      defaultValue_other:
                                        "{{count}} assigned repositories",
                                      count: assignmentCount,
                                    },
                                  )}
                            </span>
                          </span>
                          <Badge variant="outline" className="ml-2 shrink-0">
                            {ruleSet.rules.length}
                          </Badge>
                        </Button>
                      )
                    })}
                  </div>
                  <div className="border-border space-y-2 border-t p-2">
                    <Button
                      type="button"
                      variant="outline"
                      className="w-full"
                      disabled={selectedRuleSet == null}
                      onClick={() => {
                        if (selectedRuleSet != null)
                          openDuplicateDialog(selectedRuleSet)
                      }}
                    >
                      <IconCopy className="size-4" />
                      {t(
                        "pages.reviews.policies.rule_sets_duplicate",
                        "Duplicate selected set",
                      )}
                    </Button>
                  </div>
                </aside>

                <div className="min-w-0 space-y-3">
                  {selectedRuleSet != null && (
                    <section
                      aria-labelledby="selected-rule-set-heading"
                      className="min-w-0 space-y-3"
                    >
                      <div className="border-border bg-card flex flex-col gap-3 rounded-xl border p-3 sm:p-4">
                        <div className="flex min-w-0 flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
                          <div className="min-w-0">
                            <div className="flex flex-wrap items-center gap-2">
                              <h2
                                id="selected-rule-set-heading"
                                className="truncate font-semibold"
                              >
                                {selectedRuleSet.name}
                              </h2>
                              {selectedRuleSet.id ===
                                draft.defaultRuleSetID && (
                                <Badge>
                                  {t(
                                    "pages.reviews.policies.rule_sets_current_default_badge",
                                    "Current default",
                                  )}
                                </Badge>
                              )}
                              <Badge variant="outline">
                                {t(
                                  "pages.reviews.policies.rule_sets_repository_count",
                                  {
                                    defaultValue:
                                      "{{count}} assigned repository",
                                    defaultValue_other:
                                      "{{count}} assigned repositories",
                                    count: assignedRepositoryCount,
                                  },
                                )}
                              </Badge>
                            </div>
                            <p className="text-muted-foreground mt-1 text-xs/5">
                              {t(
                                "pages.reviews.policies.rule_sets_name_locked",
                                "This name is permanent. Duplicate the set when you need another named version.",
                              )}
                            </p>
                            <p className="text-muted-foreground mt-1 font-mono text-[11px]">
                              {selectedRuleSet.id}
                            </p>
                          </div>
                          <div className="flex flex-wrap gap-2">
                            {selectedRuleSet.id !== draft.defaultRuleSetID && (
                              <Button
                                type="button"
                                variant="outline"
                                onClick={() =>
                                  setDraft({
                                    ...draft,
                                    defaultRuleSetID: selectedRuleSet.id,
                                  })
                                }
                              >
                                <IconStar className="size-4" />
                                {t(
                                  "pages.reviews.policies.rule_sets_make_default",
                                  "Make default",
                                )}
                              </Button>
                            )}
                            <Button
                              type="button"
                              variant="outline"
                              onClick={() =>
                                openDuplicateDialog(selectedRuleSet)
                              }
                            >
                              <IconCopy className="size-4" />
                              {t(
                                "pages.reviews.policies.rule_sets_duplicate_short",
                                "Duplicate",
                              )}
                            </Button>
                            <Button
                              type="button"
                              variant="outline"
                              disabled={
                                selectedRuleSet.id ===
                                  reviewAttentionBuiltInRuleSetID ||
                                selectedRuleSet.id === draft.defaultRuleSetID ||
                                assignedRepositoryCount > 0
                              }
                              title={deleteRuleSetDisabledReason || undefined}
                              onClick={() =>
                                setPendingDeleteRuleSetKey(
                                  selectedRuleSet.editorKey,
                                )
                              }
                            >
                              <IconTrash className="size-4" />
                              {t(
                                "pages.reviews.policies.rule_sets_delete",
                                "Delete",
                              )}
                            </Button>
                          </div>
                        </div>
                        {deleteRuleSetDisabledReason !== "" && (
                          <p className="text-muted-foreground text-right text-xs">
                            {deleteRuleSetDisabledReason}
                          </p>
                        )}
                        <div className="border-border bg-muted/35 rounded-lg border p-3 text-xs/5">
                          {t(
                            "pages.reviews.policies.rule_sets_shared_edit_warning",
                            "Edits affect every repository assigned to this set. Unassigned repositories use whichever set is the current default. Saved changes apply to future runs that have not already pinned rules.",
                          )}
                        </div>
                        <div className="flex flex-wrap justify-end gap-2">
                          <Button
                            type="button"
                            variant="outline"
                            onClick={openAssignmentDialog}
                          >
                            <IconPlus className="size-4" />
                            {t(
                              "pages.reviews.policies.rule_sets_assign_repository",
                              "Assign repositories",
                            )}
                          </Button>
                          <Button
                            type="button"
                            variant="outline"
                            onClick={openRuleDialog}
                          >
                            <IconPlus className="size-4" />
                            {t(
                              "pages.reviews.policies.rule_sets_add_moment",
                              "Add workflow moment",
                            )}
                          </Button>
                        </div>
                      </div>

                      <RepositoryAssignmentsPanel
                        draft={draft}
                        onChange={setDraft}
                        onAdd={openAssignmentDialog}
                        onRemove={removeAssignment}
                      />

                      <section
                        aria-labelledby="known-workflow-moments-heading"
                        className="border-border bg-card rounded-xl border"
                      >
                        <div className="border-border border-b p-3 sm:p-4">
                          <h2
                            id="known-workflow-moments-heading"
                            className="text-sm font-semibold"
                          >
                            {t(
                              "pages.reviews.policies.rule_sets_known_moments",
                              "Known workflow moments",
                            )}
                          </h2>
                          <p className="text-muted-foreground mt-1 text-xs/5">
                            {t(
                              "pages.reviews.policies.rule_sets_known_moments_help",
                              "Off means this set cannot request human input at that moment. Configure a moment and add at least one check to turn it on.",
                            )}
                          </p>
                        </div>
                        <ul className="divide-border divide-y">
                          {knownAttentionDecisionPoints.map((decisionPoint) => {
                            const ruleIndex = policies.findIndex(
                              (rule) =>
                                rule.decisionPoint === decisionPoint.value,
                            )
                            const configuredRule = policies[ruleIndex]
                            const active =
                              configuredRule?.gates.some(
                                (gate) => gate.kind !== "zero",
                              ) ?? false
                            return (
                              <li
                                key={decisionPoint.value}
                                className="flex flex-col gap-2 p-3 sm:flex-row sm:items-center sm:justify-between"
                              >
                                <div className="min-w-0">
                                  <p className="text-sm font-medium">
                                    {t(
                                      decisionPoint.labelKey,
                                      decisionPoint.label,
                                    )}
                                  </p>
                                  <p className="text-muted-foreground mt-0.5 font-mono text-[11px] break-all">
                                    {decisionPoint.value}
                                  </p>
                                </div>
                                <div className="flex shrink-0 items-center gap-2">
                                  <Badge
                                    variant={active ? "secondary" : "outline"}
                                  >
                                    {active
                                      ? t(
                                          "pages.reviews.policies.rule_sets_on_checks",
                                          {
                                            defaultValue:
                                              "On · {{count}} check",
                                            defaultValue_other:
                                              "On · {{count}} checks",
                                            count: configuredRule.gates.length,
                                          },
                                        )
                                      : t(
                                          "pages.reviews.policies.rule_sets_off",
                                          "Off",
                                        )}
                                  </Badge>
                                  {configuredRule == null ? (
                                    <Button
                                      type="button"
                                      variant="outline"
                                      size="sm"
                                      onClick={() =>
                                        openKnownRuleDialog(decisionPoint.value)
                                      }
                                    >
                                      {t(
                                        "pages.reviews.policies.rule_sets_configure_moment",
                                        "Configure",
                                      )}
                                    </Button>
                                  ) : (
                                    <Button
                                      type="button"
                                      variant="ghost"
                                      size="sm"
                                      onClick={() =>
                                        setPolicyPage(
                                          Math.floor(
                                            ruleIndex / policyEditorPageSize,
                                          ),
                                        )
                                      }
                                    >
                                      {t(
                                        "pages.reviews.policies.rule_sets_edit_moment",
                                        "Edit below",
                                      )}
                                    </Button>
                                  )}
                                </div>
                              </li>
                            )
                          })}
                        </ul>
                      </section>

                      {policyPageCount > 1 && (
                        <div
                          role="group"
                          aria-label={t(
                            "pages.reviews.policies.rule_sets_rule_pages",
                            "Rule pages",
                          )}
                          className="border-border bg-card flex flex-wrap items-center justify-between gap-2 rounded-lg border p-3 text-sm"
                        >
                          <span className="text-muted-foreground">
                            {t("pages.reviews.policies.rule_sets_rule_page", {
                              defaultValue:
                                "Workflow moments {{first}}–{{last}} of {{count}}",
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
                              onClick={() =>
                                setPolicyPage(activePolicyPage - 1)
                              }
                            >
                              {t(
                                "pages.reviews.policies.rule_sets_previous_rules",
                                "Previous",
                              )}
                            </Button>
                            <Button
                              type="button"
                              variant="outline"
                              size="sm"
                              disabled={
                                activePolicyPage + 1 === policyPageCount
                              }
                              onClick={() =>
                                setPolicyPage(activePolicyPage + 1)
                              }
                            >
                              {t(
                                "pages.reviews.policies.rule_sets_next_rules",
                                "Next",
                              )}
                            </Button>
                          </div>
                        </div>
                      )}

                      {policies.length === 0 ? (
                        <div className="border-border bg-card rounded-xl border border-dashed p-6 text-center">
                          <Badge variant="outline">
                            {t(
                              "pages.reviews.policies.rule_sets_all_off",
                              "All moments Off",
                            )}
                          </Badge>
                          <p className="mt-3 text-sm font-medium">
                            {t(
                              "pages.reviews.policies.rule_sets_empty",
                              "Nothing triggers human attention in this rule set.",
                            )}
                          </p>
                          <p className="text-muted-foreground mt-1 text-xs/5">
                            {selectedRuleSet.id ===
                            reviewAttentionBuiltInRuleSetID
                              ? t(
                                  "pages.reviews.policies.rule_sets_empty_default_help",
                                  "This is the built-in Default starting behavior. Add a workflow moment only when PicoClaw should evaluate one or more checks.",
                                )
                              : t(
                                  "pages.reviews.policies.rule_sets_empty_copy_help",
                                  "Add a workflow moment only when PicoClaw should evaluate one or more checks for repositories using this set.",
                                )}
                          </p>
                          <Button
                            type="button"
                            variant="outline"
                            className="mt-3"
                            onClick={openRuleDialog}
                          >
                            <IconPlus className="size-4" />
                            {t(
                              "pages.reviews.policies.rule_sets_add_moment",
                              "Add workflow moment",
                            )}
                          </Button>
                        </div>
                      ) : (
                        visiblePolicies.map((policy, visiblePolicyIndex) => (
                          <RuleEditorCard
                            key={policy.editorKey}
                            policy={policy}
                            policyIndex={policyPageStart + visiblePolicyIndex}
                            policyPath={`ruleSets[${selectedRuleSetIndex}].rules[${policyPageStart + visiblePolicyIndex}]`}
                            issues={validationIssues}
                            agents={
                              agentsUsable
                                ? (agentsQuery.data?.agents ?? [])
                                : []
                            }
                            agentsLoading={
                              agentsQuery.isPending || agentsQuery.isFetching
                            }
                            agentsUnavailable={
                              agentsQuery.isError || agentGenerationMismatch
                            }
                            defaultAgentID={defaultAgentID}
                            nextEditorKey={nextEditorKey}
                            onSelectAgent={trustSelectedAgent}
                            onChange={(next) =>
                              updatePolicy(policy.editorKey, next)
                            }
                            onRemove={() => removePolicy(policy.editorKey)}
                          />
                        ))
                      )}
                    </section>
                  )}
                </div>
              </div>
            </div>
          </div>
        </fieldset>
      </form>

      <Dialog
        open={assignmentDialogOpen}
        onOpenChange={setAssignmentDialogOpen}
      >
        <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>
              {t(
                "pages.reviews.policies.rule_sets_assignment_title",
                "Assign repositories to rule sets",
              )}
            </DialogTitle>
            <DialogDescription>
              {t(
                "pages.reviews.policies.rule_sets_assignment_description",
                "Enter an exact owner/repository and choose the reusable set it should use. Add another row after each assignment if needed.",
              )}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <Label htmlFor="new-rule-set-repository">
              {t(
                "pages.reviews.policies.rule_sets_repository_label",
                "Repository",
              )}
            </Label>
            <Input
              id="new-rule-set-repository"
              value={assignmentRepository}
              onChange={(event) => setAssignmentRepository(event.target.value)}
              placeholder="owner/repository"
              aria-invalid={newRepositoryIssue !== ""}
              aria-describedby="new-rule-set-repository-help"
              spellCheck={false}
              autoComplete="off"
            />
            <p
              id="new-rule-set-repository-help"
              className={cn(
                "text-xs",
                newRepositoryIssue
                  ? "text-destructive"
                  : "text-muted-foreground",
              )}
            >
              {newRepositoryIssue ||
                t(
                  "pages.reviews.policies.rule_sets_repository_help",
                  "Use the exact owner/repository name, for example acme/widgets.",
                )}
            </p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="new-repository-rule-set">
              {t(
                "pages.reviews.policies.rule_sets_assignment_set_label",
                "Rule set",
              )}
            </Label>
            <select
              id="new-repository-rule-set"
              value={assignmentRuleSetID}
              onChange={(event) => setAssignmentRuleSetID(event.target.value)}
              className="border-input bg-background focus-visible:border-ring focus-visible:ring-ring/25 h-9 w-full rounded-lg border px-3 text-sm outline-none focus-visible:ring-2"
            >
              {draft.ruleSets.map((ruleSet) => (
                <option key={ruleSet.id} value={ruleSet.id}>
                  {ruleSet.name}
                </option>
              ))}
            </select>
          </div>
          <div className="border-border bg-muted/35 flex gap-2 rounded-lg border p-3 text-xs/5">
            <IconInfoCircle className="mt-0.5 size-4 shrink-0" />
            <p>
              {t(
                "pages.reviews.policies.rule_sets_intake_explanation",
                "This assignment only chooses attention behavior. Configure GitHub intake separately under Event sources.",
              )}{" "}
              <Link
                to="/event-sources"
                className="font-medium underline underline-offset-2"
              >
                {t(
                  "pages.reviews.policies.rule_sets_open_event_sources",
                  "Open Event sources",
                )}
              </Link>
            </p>
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => setAssignmentDialogOpen(false)}
            >
              {t("common.done", "Done")}
            </Button>
            <Button
              type="button"
              disabled={
                normalizedNewRepository === "" || newRepositoryIssue !== ""
              }
              onClick={addAssignment}
            >
              <IconPlus className="size-4" />
              {t(
                "pages.reviews.policies.rule_sets_add_assignment",
                "Add assignment",
              )}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={duplicateDialogOpen} onOpenChange={setDuplicateDialogOpen}>
        <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>
              {t(
                "pages.reviews.policies.rule_sets_duplicate_title",
                "Duplicate rule set",
              )}
            </DialogTitle>
            <DialogDescription>
              {t("pages.reviews.policies.rule_sets_duplicate_description", {
                defaultValue:
                  "Copy every rule and check from {{name}}. The new set will not become the default and will not receive any repository assignments.",
                name: duplicateSource?.name ?? "",
              })}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <Label htmlFor="duplicate-rule-set-name">
              {t(
                "pages.reviews.policies.rule_sets_duplicate_name",
                "New permanent name",
              )}
            </Label>
            <Input
              id="duplicate-rule-set-name"
              value={duplicateName}
              onChange={(event) => setDuplicateName(event.target.value)}
              aria-invalid={duplicateNameIssue !== ""}
              aria-describedby="duplicate-rule-set-name-help"
              autoComplete="off"
            />
            <p
              id="duplicate-rule-set-name-help"
              className={cn(
                "text-xs",
                duplicateNameIssue
                  ? "text-destructive"
                  : "text-muted-foreground",
              )}
            >
              {duplicateNameIssue ||
                t(
                  "pages.reviews.policies.rule_sets_duplicate_name_help",
                  "Names cannot be edited later. Pick a unique name that explains when this set should be used.",
                )}
            </p>
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => setDuplicateDialogOpen(false)}
            >
              {t("common.cancel", "Cancel")}
            </Button>
            <Button
              type="button"
              disabled={
                normalizedDuplicateName === "" || duplicateNameIssue !== ""
              }
              onClick={duplicateRuleSet}
            >
              <IconCopy className="size-4" />
              {t(
                "pages.reviews.policies.rule_sets_duplicate_action",
                "Create duplicate",
              )}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={ruleDialogOpen} onOpenChange={setRuleDialogOpen}>
        <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>
              {t(
                "pages.reviews.policies.add_rule_title",
                "Add an attention rule",
              )}
            </DialogTitle>
            <DialogDescription>
              {t("pages.reviews.policies.rule_sets_add_rule_description", {
                defaultValue:
                  "Choose a moment to configure inside {{name}}. A moment with no checks is Off.",
                name: selectedRuleSet?.name ?? "",
              })}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <Label htmlFor="new-attention-rule-moment">
              {t(
                "pages.reviews.policies.rule_moment",
                "When should this rule run?",
              )}
            </Label>
            <select
              id="new-attention-rule-moment"
              value={newDecisionChoice}
              onChange={(event) => setNewDecisionChoice(event.target.value)}
              aria-invalid={
                newDecisionChoice !== customDecisionPointChoice &&
                newDecisionIssue !== ""
              }
              aria-describedby={
                newDecisionChoice !== customDecisionPointChoice &&
                newDecisionIssue !== ""
                  ? "new-attention-rule-error"
                  : undefined
              }
              className="border-input bg-background focus-visible:border-ring focus-visible:ring-ring/25 h-9 w-full rounded-lg border px-3 text-sm outline-none focus-visible:ring-2"
            >
              <option value="">
                {t("pages.reviews.policies.select_moment", "Select a moment")}
              </option>
              {knownAttentionDecisionPoints.map((decisionPoint) => (
                <option key={decisionPoint.value} value={decisionPoint.value}>
                  {t(decisionPoint.labelKey, decisionPoint.label)}
                </option>
              ))}
              <option value={customDecisionPointChoice}>
                {t(
                  "pages.reviews.policies.custom_decision",
                  "Custom workflow moment (advanced)",
                )}
              </option>
            </select>
          </div>
          {newDecisionChoice === customDecisionPointChoice && (
            <div className="space-y-2">
              <Label htmlFor="new-custom-decision-point">
                {t(
                  "pages.reviews.policies.custom_decision_label",
                  "Custom workflow identifier",
                )}
              </Label>
              <Input
                id="new-custom-decision-point"
                value={newCustomDecisionPoint}
                onChange={(event) =>
                  setNewCustomDecisionPoint(event.target.value)
                }
                placeholder="custom.release_check"
                aria-invalid={newDecisionIssue !== ""}
                aria-describedby={
                  newDecisionIssue === ""
                    ? undefined
                    : "new-attention-rule-error"
                }
                spellCheck={false}
                autoComplete="off"
              />
            </div>
          )}
          {newDecisionIssue !== "" && (
            <p
              id="new-attention-rule-error"
              className="text-destructive text-xs"
              role="alert"
            >
              {newDecisionIssue}
            </p>
          )}
          <div className="border-border bg-muted/35 rounded-lg border p-3 text-xs/5">
            {t(
              "pages.reviews.policies.rule_sets_rule_next",
              "Next, add one or more checks that decide whether PicoClaw should ask for human input. Remove the moment to turn it Off again.",
            )}
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => setRuleDialogOpen(false)}
            >
              {t("common.cancel", "Cancel")}
            </Button>
            <Button
              type="button"
              disabled={newDecisionPoint === "" || newDecisionIssue !== ""}
              onClick={addPolicy}
            >
              {t("pages.reviews.policies.add_rule", "Add rule")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <AlertDialog open={reloadConfirmOpen} onOpenChange={setReloadConfirmOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(
                "pages.reviews.policies.reload_title",
                "Discard this rule draft?",
              )}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(
                "pages.reviews.policies.reload_description",
                "Reloading replaces every unsaved rule edit with the latest trusted configuration.",
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
                "Discard unsaved rule changes?",
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
        open={pendingDeleteRuleSetKey !== ""}
        onOpenChange={(open) => {
          if (!open) setPendingDeleteRuleSetKey("")
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(
                "pages.reviews.policies.rule_sets_delete_title",
                "Delete this rule set?",
              )}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(
                "pages.reviews.policies.rule_sets_delete_description",
                "This permanently removes the rule set and all of its configured workflow moments from this draft. This action takes effect when you save.",
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>
              {t("common.cancel", "Cancel")}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={deletePendingRuleSet}
            >
              {t(
                "pages.reviews.policies.rule_sets_delete_confirm",
                "Delete rule set",
              )}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function RuleEditorCard({
  policy,
  policyIndex,
  policyPath,
  issues,
  agents,
  agentsLoading,
  agentsUnavailable,
  defaultAgentID,
  nextEditorKey,
  onSelectAgent,
  onChange,
  onRemove,
}: {
  policy: ReviewAttentionRuleDraft
  policyIndex: number
  policyPath: string
  issues: readonly ReviewAttentionPolicyIssue[]
  agents: ReviewAttentionAgent[]
  agentsLoading: boolean
  agentsUnavailable: boolean
  defaultAgentID: string
  nextEditorKey: (prefix: string) => string
  onSelectAgent: (agentID: string) => void
  onChange: (policy: ReviewAttentionRuleDraft) => void
  onRemove: () => void
}) {
  const { t } = useTranslation()
  const [addGateOpen, setAddGateOpen] = useState(false)
  const decisionPointIssue = findReviewAttentionPolicyIssue(
    issues,
    `${policyPath}.decisionPoint`,
  )
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

  const addGate = (kind: ReviewAttentionGateKind) => {
    onChange({
      ...policy,
      gates: [
        ...policy.gates,
        createReviewAttentionStarterGate(
          kind,
          defaultAgentID,
          policy.gates,
          nextEditorKey,
        ),
      ],
    })
    setAddGateOpen(false)
  }

  return (
    <article
      aria-label={t("pages.reviews.policies.decision_aria", {
        defaultValue: "Attention rule {{number}}",
        number: policyIndex + 1,
      })}
      className="border-border bg-card min-w-0 space-y-4 rounded-xl border p-3 sm:p-4"
    >
      <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
        <div className="min-w-0 flex-1">
          <Label htmlFor={`${policy.editorKey}-decision`}>
            {t("pages.reviews.policies.decision_point", "When this happens")}
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
            {attentionDecisionPointHelp(policy.decisionPoint, t)}
          </p>
          {policy.decisionPoint === "pr_development.before_push" && (
            <p className="text-muted-foreground mt-1 text-xs">
              {t(
                "pages.reviews.policies.before_push_owner_help",
                "For a working-agent check here, select the same agent that owns this PR's local development. An owner mismatch fails closed before publication.",
              )}
            </p>
          )}
          <ReviewAttentionFieldIssue
            id={`${policy.editorKey}-decision-error`}
            issue={decisionPointIssue}
          />
        </div>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label={t("pages.reviews.policies.remove_decision", {
            defaultValue: "Remove attention rule {{number}}",
            number: policyIndex + 1,
          })}
          title={t("pages.reviews.policies.remove_decision_short", "Remove")}
          onClick={onRemove}
        >
          <IconTrash className="size-4" />
        </Button>
      </div>

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
              {t("pages.reviews.policies.gates", "Checks (run in order)")}
            </h3>
            <p className="text-muted-foreground text-xs">
              {t(
                "pages.reviews.policies.gates_help",
                "Each check decides whether PicoClaw should ask for your input. Add more only when you need multiple checks at the same moment.",
              )}
            </p>
          </div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => setAddGateOpen(true)}
          >
            <IconPlus className="size-4" />
            {t("pages.reviews.policies.add_gate", "Add check")}
          </Button>
        </div>
        {policy.gates.length === 0 ? (
          <p className="border-border text-muted-foreground rounded-lg border border-dashed p-4 text-center text-xs">
            {t(
              "pages.reviews.policies.no_gates",
              "No checks are configured, so this rule will not ask for attention.",
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

      <Dialog open={addGateOpen} onOpenChange={setAddGateOpen}>
        <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>
              {t(
                "pages.reviews.policies.add_check_title",
                "How should PicoClaw decide?",
              )}
            </DialogTitle>
            <DialogDescription>
              {t(
                "pages.reviews.policies.add_check_description",
                "Choose a starting point. You can edit its details after adding it.",
              )}
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-2">
            <CheckKindButton
              title={t(
                "pages.reviews.policies.check_working_title",
                "Ask the agent already working on the PR",
              )}
              description={t(
                "pages.reviews.policies.check_working_description",
                "Uses the current development conversation and asks you only when the agent needs human intent.",
              )}
              disabled={
                defaultAgentID === "" || agentsLoading || agentsUnavailable
              }
              onClick={() => addGate("ai_working_context")}
            />
            <CheckKindButton
              title={t(
                "pages.reviews.policies.check_isolated_title",
                "Run a fresh AI check",
              )}
              description={t(
                "pages.reviews.policies.check_isolated_description",
                "Uses a separate private AI context without the development conversation.",
              )}
              disabled={
                defaultAgentID === "" || agentsLoading || agentsUnavailable
              }
              onClick={() => addGate("ai_isolated_context")}
            />
            <CheckKindButton
              title={t(
                "pages.reviews.policies.check_fixed_title",
                "Ask when a fixed condition matches",
              )}
              description={t(
                "pages.reviews.policies.check_fixed_description",
                "Starts with an always-ask confirmation; edit the condition and question after adding it.",
              )}
              onClick={() => addGate("deterministic")}
            />
          </div>
          {(defaultAgentID === "" || agentsLoading || agentsUnavailable) && (
            <p className="text-muted-foreground text-xs">
              {t(
                "pages.reviews.policies.ai_check_unavailable",
                "AI check choices are available after the configured agent catalog loads.",
              )}
            </p>
          )}
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => setAddGateOpen(false)}
            >
              {t("common.cancel", "Cancel")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </article>
  )
}

function RepositoryAssignmentsPanel({
  draft,
  onChange,
  onAdd,
  onRemove,
}: {
  draft: ReviewAttentionPolicyDraft
  onChange: (draft: ReviewAttentionPolicyDraft) => void
  onAdd: () => void
  onRemove: (assignmentKey: string) => void
}) {
  const { t } = useTranslation()
  const defaultRuleSet = draft.ruleSets.find(
    (ruleSet) => ruleSet.id === draft.defaultRuleSetID,
  )

  return (
    <section
      aria-labelledby="repository-rule-set-assignments-heading"
      className="border-border bg-card rounded-xl border"
    >
      <div className="border-border flex flex-col gap-2 border-b p-3 sm:flex-row sm:items-start sm:justify-between sm:p-4">
        <div>
          <h2
            id="repository-rule-set-assignments-heading"
            className="text-sm font-semibold"
          >
            {t(
              "pages.reviews.policies.rule_sets_assignments_heading",
              "Repository assignments",
            )}
          </h2>
          <p className="text-muted-foreground mt-1 text-xs/5">
            {t("pages.reviews.policies.rule_sets_assignments_help", {
              defaultValue:
                "An assignment picks exactly one rule set. Repositories without an assignment follow the current default: {{name}}.",
              name: defaultRuleSet?.name ?? draft.defaultRuleSetID,
            })}
          </p>
        </div>
        <Button type="button" variant="outline" size="sm" onClick={onAdd}>
          <IconPlus className="size-4" />
          {t(
            "pages.reviews.policies.rule_sets_assignments_add",
            "Add assignment",
          )}
        </Button>
      </div>
      {draft.repositoryAssignments.length === 0 ? (
        <p className="text-muted-foreground p-4 text-center text-xs/5">
          {t(
            "pages.reviews.policies.rule_sets_assignments_empty",
            "No repository-specific assignments. Every repository follows the current default.",
          )}
        </p>
      ) : (
        <ul className="divide-border divide-y">
          {draft.repositoryAssignments.map((assignment) => (
            <li
              key={assignment.editorKey}
              className="grid min-w-0 gap-2 p-3 sm:grid-cols-[minmax(0,1fr)_minmax(180px,0.8fr)_auto] sm:items-center"
            >
              <span className="min-w-0 font-mono text-xs break-all">
                {assignment.repository}
              </span>
              <div>
                <Label
                  htmlFor={`${assignment.editorKey}-rule-set`}
                  className="sr-only"
                >
                  {t(
                    "pages.reviews.policies.rule_sets_assignment_for_repository",
                    {
                      defaultValue: "Rule set for {{repository}}",
                      repository: assignment.repository,
                    },
                  )}
                </Label>
                <select
                  id={`${assignment.editorKey}-rule-set`}
                  value={assignment.ruleSetID}
                  onChange={(event) =>
                    onChange({
                      ...draft,
                      repositoryAssignments: draft.repositoryAssignments.map(
                        (candidate) =>
                          candidate.editorKey === assignment.editorKey
                            ? {
                                ...candidate,
                                ruleSetID: event.target.value,
                              }
                            : candidate,
                      ),
                    })
                  }
                  className="border-input bg-background focus-visible:border-ring focus-visible:ring-ring/25 h-9 w-full rounded-lg border px-3 text-sm outline-none focus-visible:ring-2"
                >
                  {draft.ruleSets.map((ruleSet) => (
                    <option key={ruleSet.id} value={ruleSet.id}>
                      {ruleSet.name}
                    </option>
                  ))}
                </select>
              </div>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                aria-label={t(
                  "pages.reviews.policies.rule_sets_remove_assignment",
                  {
                    defaultValue:
                      "Remove {{repository}} assignment; it will follow the current default",
                    repository: assignment.repository,
                  },
                )}
                title={t(
                  "pages.reviews.policies.rule_sets_remove_assignment_short",
                  "Remove assignment; follow default",
                )}
                onClick={() => onRemove(assignment.editorKey)}
              >
                <IconTrash className="size-4" />
              </Button>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function CheckKindButton({
  title,
  description,
  disabled = false,
  onClick,
}: {
  title: string
  description: string
  disabled?: boolean
  onClick: () => void
}) {
  return (
    <Button
      type="button"
      variant="outline"
      className="h-auto min-w-0 justify-start p-3 text-left whitespace-normal"
      disabled={disabled}
      onClick={onClick}
    >
      <span className="min-w-0">
        <span className="block font-medium">{title}</span>
        <span className="text-muted-foreground mt-1 block text-xs font-normal">
          {description}
        </span>
      </span>
    </Button>
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
  const [pendingKind, setPendingKind] =
    useState<ReviewAttentionGateKind | null>(null)
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
  const applyKindChange = (nextKind: ReviewAttentionGateKind) => {
    const converted = convertReviewAttentionGateKind(
      gate,
      nextKind,
      defaultAgentID,
    )
    onChange(
      gate.kind === "zero"
        ? withReviewAttentionGateStarterDefaults(converted)
        : converted,
    )
    setPendingKind(null)
  }
  const requestKindChange = (nextKind: ReviewAttentionGateKind) => {
    if (nextKind === gate.kind) return
    if (reviewAttentionKindChangeLosesDetails(gate.kind, nextKind)) {
      setPendingKind(nextKind)
      return
    }
    applyKindChange(nextKind)
  }
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
      "This is an explicit no-op check and will not ask for attention.",
    ),
  }[gate.kind]
  return (
    <fieldset className="border-border min-w-0 space-y-3 rounded-lg border p-3">
      <legend className="px-1 text-xs font-medium">
        {t("pages.reviews.policies.gate_number", {
          defaultValue: "Check {{number}}",
          number: gateIndex + 1,
        })}
      </legend>
      <div className="grid min-w-0 gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(190px,0.7fr)_auto] sm:items-end">
        <div>
          <Label htmlFor={fieldID("id")}>
            {t("pages.reviews.policies.gate_id", "Check ID")}
          </Label>
          <Input
            id={fieldID("id")}
            aria-invalid={idIssue != null}
            aria-describedby={
              idIssue == null
                ? fieldID("id-help")
                : `${fieldID("id-help")} ${fieldID("id-error")}`
            }
            value={gate.id}
            onChange={(event) => onChange({ ...gate, id: event.target.value })}
            placeholder="ask_owner"
            spellCheck={false}
            autoComplete="off"
          />
          <p
            id={fieldID("id-help")}
            className="text-muted-foreground mt-1 text-xs"
          >
            {t(
              "pages.reviews.policies.rule_sets_check_id_help",
              "Stable identifier for this check inside the rule set.",
            )}
          </p>
          <ReviewAttentionFieldIssue id={fieldID("id-error")} issue={idIssue} />
        </div>
        <div>
          <Label htmlFor={fieldID("kind")}>
            {t("pages.reviews.policies.gate_type", "How to decide")}
          </Label>
          <select
            id={fieldID("kind")}
            aria-invalid={kindIssue != null}
            aria-describedby={
              kindIssue == null ? undefined : fieldID("kind-error")
            }
            value={gate.kind}
            onChange={(event) =>
              requestKindChange(event.target.value as ReviewAttentionGateKind)
            }
            className="border-input bg-background focus-visible:border-ring focus-visible:ring-ring/25 h-9 w-full rounded-lg border px-3 text-sm outline-none focus-visible:ring-2"
          >
            <option value="ai_working_context">
              {t(
                "pages.reviews.policies.gate_type_working",
                "Ask the agent working on the PR",
              )}
            </option>
            <option value="ai_isolated_context">
              {t(
                "pages.reviews.policies.gate_type_isolated",
                "Run a fresh AI check",
              )}
            </option>
            <option value="deterministic">
              {t(
                "pages.reviews.policies.gate_type_fixed",
                "Match a fixed condition",
              )}
            </option>
            <option value="zero">
              {t(
                "pages.reviews.policies.gate_type_zero",
                "No action (advanced)",
              )}
            </option>
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
              "Move check up",
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
              "Move check down",
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
            aria-label={t("pages.reviews.policies.remove_gate", "Remove check")}
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
                  "Fixed condition (advanced)",
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

      <AlertDialog
        open={pendingKind != null}
        onOpenChange={(open) => {
          if (!open) setPendingKind(null)
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(
                "pages.reviews.policies.change_check_type_title",
                "Change how this check decides?",
              )}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(
                "pages.reviews.policies.change_check_type_description",
                "This removes details used only by the current choice, such as its AI instructions or fixed condition. Those values will not be restored if you switch back.",
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>
              {t(
                "pages.reviews.policies.keep_check_type",
                "Keep current choice",
              )}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                if (pendingKind != null) applyKindChange(pendingKind)
              }}
            >
              {t(
                "pages.reviews.policies.confirm_check_type",
                "Change check type",
              )}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
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
  const ruleSetMatch =
    /^ruleSets\[(\d+)](?:\.rules\[(\d+)])?(?:\.gates\[(\d+)])?(?:\.(\w+))?/.exec(
      path,
    )
  if (ruleSetMatch != null) {
    const ruleSetIndex = Number(ruleSetMatch[1])
    const ruleSet = draft.ruleSets[ruleSetIndex]
    const parts = [ruleSet?.name || `rule set ${ruleSetIndex + 1}`]
    if (ruleSetMatch[2] !== undefined) {
      const ruleIndex = Number(ruleSetMatch[2])
      const rule = ruleSet?.rules[ruleIndex]
      parts.push(rule?.decisionPoint || `workflow moment ${ruleIndex + 1}`)
      if (ruleSetMatch[3] !== undefined) {
        const gateIndex = Number(ruleSetMatch[3])
        parts.push(rule?.gates[gateIndex]?.id || `check ${gateIndex + 1}`)
      }
    }
    if (ruleSetMatch[4] !== undefined)
      parts.push(reviewAttentionIssueFieldLabel(ruleSetMatch[4]))
    return parts.join(" · ")
  }

  const assignmentMatch = /^repositoryAssignments\[(\d+)](?:\.(\w+))?/.exec(
    path,
  )
  if (assignmentMatch != null) {
    const assignmentIndex = Number(assignmentMatch[1])
    const assignment = draft.repositoryAssignments[assignmentIndex]
    const parts = [
      "Repository assignment",
      assignment?.repository || `assignment ${assignmentIndex + 1}`,
    ]
    const field = assignmentMatch[2]
    if (field !== undefined) parts.push(reviewAttentionIssueFieldLabel(field))
    return parts.join(" · ")
  }
  if (path === "catalog") return "Rule catalog"
  if (path === "ruleSets") return "Rule sets"
  if (path === "defaultRuleSetID") return "Current default"
  if (path === "repositoryAssignments") return "Repository assignments"
  return path
}

function reviewAttentionIssueFieldLabel(field: string): string {
  const labels: Record<string, string> = {
    agentID: "AI agent",
    criteria: "AI criteria",
    decisionPoint: "workflow moment",
    gates: "checks",
    id: "Check ID",
    kind: "how to decide",
    name: "permanent name",
    questionsSource: "questions",
    repository: "repository name",
    ruleSetID: "rule set",
    title: "attention title",
    when: "fixed condition",
  }
  return labels[field] ?? field
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

function createReviewAttentionStarterGate(
  kind: ReviewAttentionGateKind,
  defaultAgentID: string,
  existingGates: readonly ReviewAttentionGateDraft[],
  nextEditorKey: (prefix: string) => string,
): ReviewAttentionGateDraft {
  const gate = createReviewAttentionGateDraft(
    kind,
    defaultAgentID,
    nextEditorKey,
  )
  const baseID = {
    ai_working_context: "ask_working_agent",
    ai_isolated_context: "independent_check",
    deterministic: "confirm_with_owner",
    zero: "no_attention",
  }[kind]
  const usedIDs = new Set(existingGates.map((candidate) => candidate.id))
  let id = baseID
  for (let suffix = 2; usedIDs.has(id); suffix += 1) {
    id = `${baseID}_${suffix}`
  }
  return withReviewAttentionGateStarterDefaults({ ...gate, id })
}

function reviewAttentionKindChangeLosesDetails(
  currentKind: ReviewAttentionGateKind,
  nextKind: ReviewAttentionGateKind,
): boolean {
  if (currentKind === nextKind || currentKind === "zero") return false
  const currentIsAI =
    currentKind === "ai_working_context" ||
    currentKind === "ai_isolated_context"
  const nextIsAI =
    nextKind === "ai_working_context" || nextKind === "ai_isolated_context"
  return !(currentIsAI && nextIsAI)
}

function withReviewAttentionGateStarterDefaults(
  gate: ReviewAttentionGateDraft,
): ReviewAttentionGateDraft {
  switch (gate.kind) {
    case "ai_working_context":
    case "ai_isolated_context":
      return {
        ...gate,
        criteria:
          gate.criteria ||
          "Ask only when the decision requires human intent or preference that cannot be inferred safely.",
        title: gate.title || "Your input is needed",
      }
    case "deterministic":
      return {
        ...gate,
        title: gate.title || "Confirmation needed",
        questionsSource:
          gate.questionsSource === "[]"
            ? '["Is it okay to continue?"]'
            : gate.questionsSource,
      }
    case "zero":
      return gate
  }
}

function attentionDecisionPointHelp(
  value: string,
  t: ReturnType<typeof useTranslation>["t"],
): string {
  const known = knownAttentionDecisionPoints.find(
    (decisionPoint) => decisionPoint.value === value,
  )
  if (known == null) {
    return t(
      "pages.reviews.policies.decision_point_help",
      "This is a custom workflow moment. Use its exact lowercase decision identifier.",
    )
  }
  return t("pages.reviews.policies.known_decision_point_help", {
    defaultValue: "{{label}} · internal identifier: {{identifier}}",
    label: t(known.labelKey, known.label),
    identifier: known.value,
  })
}

function PolicyPageState({
  title,
  loading = false,
  action,
  onShowInbox,
  onShowDevelopment,
  standalone = false,
}: {
  title: string
  loading?: boolean
  action?: ReactNode
  onShowInbox: () => void
  onShowDevelopment: () => void
  standalone?: boolean
}) {
  const { t } = useTranslation()
  return (
    <div className="bg-background flex h-full min-h-0 flex-col">
      <PageHeader
        title={t("pages.reviews.policies.page_title", "Attention rules")}
        className="h-auto min-h-14 flex-wrap gap-2 py-2 [&>div:last-child]:flex-wrap"
      >
        {standalone ? (
          <Button type="button" variant="outline" onClick={onShowInbox}>
            <IconArrowDown className="size-4 rotate-90" />
            {t("pages.reviews.policies.back", "Pull request work")}
          </Button>
        ) : null}
      </PageHeader>
      {!standalone ? (
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
      ) : null}
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
  for (const ruleSet of draft.ruleSets) {
    for (const rule of ruleSet.rules) collect(rule.gates)
  }
  return ids
}

function createOpaqueRuleSetID(
  ruleSets: readonly ReviewAttentionRuleSetDraft[],
): string {
  const used = new Set(ruleSets.map((ruleSet) => ruleSet.id))
  for (;;) {
    const bytes = new Uint8Array(16)
    crypto.getRandomValues(bytes)
    const candidate = `ruleset_${Array.from(bytes, (byte) =>
      byte.toString(16).padStart(2, "0"),
    ).join("")}`
    if (!used.has(candidate)) return candidate
  }
}

function previousReviewAttentionAgentCursor(
  cursor: string | undefined,
): string | undefined {
  if (cursor === undefined || cursor === "256") return undefined
  return String(Number(cursor) - 256)
}
