import {
  IconArrowLeft,
  IconExternalLink,
  IconGitBranch,
  IconGitPullRequest,
  IconMessageCircle,
  IconRefresh,
  IconSend,
  IconTool,
} from "@tabler/icons-react"
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"
import {
  type FormEvent,
  type UIEvent,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"
import { useTranslation } from "react-i18next"

import {
  MAXIMUM_PR_DEVELOPMENT_CHAT_BYTES,
  MAXIMUM_PR_DEVELOPMENT_REPAIR_INSTRUCTION_BYTES,
  PRDevelopmentAPIError,
  type PRDevelopmentCIStatus,
  type PRDevelopmentCase,
  type PRDevelopmentCaseDetail,
  type PRDevelopmentCasePage,
  type PRDevelopmentCaseSummary,
  type PRDevelopmentLocalDevelopment,
  type PRDevelopmentMessage,
  type PRDevelopmentRepairAttempt,
  type PRDevelopmentRepairStatus,
  type PRDevelopmentReviewState,
  chatAboutPRDevelopmentCase,
  createPRDevelopmentRepairRequestID,
  getPRDevelopmentCase,
  listPRDevelopmentCases,
  normalizePRDevelopmentChatContent,
  startPRDevelopmentRepair,
} from "@/api/pr-development"
import {
  PRDevelopmentAttentionAPIError,
  PR_DEVELOPMENT_ATTENTION_RESPONSE_MAXIMUM_BYTES,
  getPRDevelopmentAttention,
  respondToPRDevelopmentAttention,
} from "@/api/pr-development-attention"
import { trimGoSpace } from "@/api/review-attention-json"
import { PageHeader } from "@/components/page-header"
import { AttentionConversation } from "@/components/reviews/attention-conversation"
import {
  attentionProjectionContainsResponse,
  attentionProjectionIsVisible,
  attentionProjectionPollInterval,
  findActionableAttentionTurn,
} from "@/components/reviews/attention-conversation-model"
import { ReviewWorkbenchTabs } from "@/components/reviews/review-workbench-tabs"
import type { ReviewsRouteSearch } from "@/components/reviews/reviews-page"
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
import { cn } from "@/lib/utils"

const DEVELOPMENT_PAGE_SIZE = 40
const DEVELOPMENT_LIST_POLL_INTERVAL_MS = 5_000
const MAXIMUM_PULL_NUMBER = 2_147_483_647

export function PRDevelopmentPage({
  search,
  onSearchChange,
}: {
  search: ReviewsRouteSearch & { view: "development" }
  onSearchChange: (search: ReviewsRouteSearch, replace?: boolean) => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const wideLayout = useWideDevelopmentLayout()
  const restoreFocusCaseRef = useRef<string | null>(null)
  const [repositoryDraft, setRepositoryDraft] = useState(
    search.repository ?? "",
  )
  const [pullNumberDraft, setPullNumberDraft] = useState(
    search.pull_number?.toString() ?? "",
  )
  const listFilters = useMemo(
    () => ({
      ...(search.repository ? { repository: search.repository } : {}),
      ...(search.pull_number ? { pull_number: search.pull_number } : {}),
      limit: DEVELOPMENT_PAGE_SIZE,
    }),
    [search.pull_number, search.repository],
  )
  const casesQuery = useInfiniteQuery({
    queryKey: ["pr-development", "list", listFilters],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      listPRDevelopmentCases({
        ...listFilters,
        cursor: pageParam || undefined,
      }),
    getNextPageParam: (lastPage: PRDevelopmentCasePage) =>
      lastPage.next_cursor || undefined,
    refetchInterval: DEVELOPMENT_LIST_POLL_INTERVAL_MS,
  })
  const cases = useMemo(
    () =>
      deduplicateCases(
        casesQuery.data?.pages.flatMap((page) => page.cases) ?? [],
      ),
    [casesQuery.data?.pages],
  )

  useEffect(() => {
    setRepositoryDraft(search.repository ?? "")
  }, [search.repository])

  useEffect(() => {
    setPullNumberDraft(search.pull_number?.toString() ?? "")
  }, [search.pull_number])

  useEffect(() => {
    if (wideLayout && !search.case && cases.length > 0) {
      onSearchChange({ ...search, case: cases[0].id }, true)
    }
  }, [cases, onSearchChange, search, wideLayout])

  useEffect(() => {
    const caseID = restoreFocusCaseRef.current
    if (search.case || caseID == null) return
    const button = document.querySelector<HTMLButtonElement>(
      `button[data-pr-development-case-id="${caseID}"]`,
    )
    if (button) {
      restoreFocusCaseRef.current = null
      button.focus()
    }
  }, [cases, search.case])

  const refresh = async () => {
    await queryClient.invalidateQueries({ queryKey: ["pr-development"] })
  }

  const applyFilters = (event: FormEvent) => {
    event.preventDefault()
    const repository = repositoryDraft.trim()
    const pullNumber =
      pullNumberDraft === "" ? undefined : Number(pullNumberDraft)
    if (
      pullNumber !== undefined &&
      (!Number.isInteger(pullNumber) ||
        pullNumber < 1 ||
        pullNumber > MAXIMUM_PULL_NUMBER)
    ) {
      return
    }
    onSearchChange(
      {
        view: "development",
        ...(repository ? { repository } : {}),
        ...(pullNumber ? { pull_number: pullNumber } : {}),
      },
      true,
    )
  }

  return (
    <div className="bg-background flex h-full min-h-0 flex-col">
      <PageHeader
        title={t("pages.reviews.title", "Pull request reviews")}
        titleExtra={
          <Badge variant="secondary">
            {t(
              "pages.reviews.development.badge",
              "Captured feedback · AI workbench",
            )}
          </Badge>
        }
      >
        <Button
          type="button"
          variant="outline"
          size="icon"
          aria-label={t(
            "pages.reviews.development.reload",
            "Reload captured feedback",
          )}
          title={t(
            "pages.reviews.development.reload",
            "Reload captured feedback",
          )}
          onClick={() => void refresh()}
          disabled={casesQuery.isFetching}
        >
          <IconRefresh
            className={cn("size-4", casesQuery.isFetching && "animate-spin")}
          />
        </Button>
      </PageHeader>

      <ReviewWorkbenchTabs
        active="development"
        onChange={(view) => {
          if (view === "inbox") {
            onSearchChange({})
          } else if (view === "policies") {
            onSearchChange({ view: "policies" })
          }
        }}
      />

      <div className="flex min-h-0 flex-1 flex-col">
        <div className="border-border flex shrink-0 border-b px-3 py-2 sm:px-4">
          <form
            className="flex min-w-0 flex-1 flex-wrap gap-2"
            onSubmit={applyFilters}
          >
            <Label htmlFor="development-repository-filter" className="sr-only">
              {t("pages.reviews.development.repository", "Repository")}
            </Label>
            <Input
              id="development-repository-filter"
              value={repositoryDraft}
              maxLength={256}
              placeholder={t(
                "pages.reviews.filters.repository_placeholder",
                "owner/repository",
              )}
              onChange={(event) => setRepositoryDraft(event.target.value)}
              className="max-w-md"
            />
            <Label htmlFor="development-pull-number-filter" className="sr-only">
              {t(
                "pages.reviews.development.pull_number",
                "Pull request number",
              )}
            </Label>
            <Input
              id="development-pull-number-filter"
              type="number"
              inputMode="numeric"
              min={1}
              max={MAXIMUM_PULL_NUMBER}
              step={1}
              value={pullNumberDraft}
              placeholder={t(
                "pages.reviews.development.pull_number_placeholder",
                "PR number",
              )}
              onChange={(event) => setPullNumberDraft(event.target.value)}
              className="w-32"
            />
            <Button type="submit" variant="outline">
              {t("pages.reviews.filters.apply", "Apply")}
            </Button>
            {search.repository || search.pull_number ? (
              <Button
                type="button"
                variant="ghost"
                onClick={() => {
                  setRepositoryDraft("")
                  setPullNumberDraft("")
                  onSearchChange({ view: "development" }, true)
                }}
              >
                {t("pages.reviews.filters.reset", "Reset")}
              </Button>
            ) : null}
          </form>
        </div>

        <div className="min-h-0 flex-1 overflow-auto p-3 lg:overflow-hidden lg:p-4">
          <div className="flex min-h-full min-w-0 flex-col gap-3 lg:grid lg:h-full lg:min-h-0 lg:grid-cols-[minmax(300px,0.72fr)_minmax(0,1.55fr)]">
            <DevelopmentCaseList
              cases={cases}
              selectedCaseID={search.case}
              hiddenOnMobile={Boolean(search.case)}
              loading={casesQuery.isPending}
              error={casesQuery.error}
              hasMore={Boolean(casesQuery.hasNextPage)}
              loadingMore={casesQuery.isFetchingNextPage}
              onSelect={(caseID, attentionRequired) => {
                if (attentionRequired) {
                  onSearchChange(
                    { view: "development", case: caseID, focus: "chat" },
                    false,
                  )
                  return
                }
                const next = { ...search, case: caseID }
                delete next.focus
                onSearchChange(next, false)
              }}
              onRetry={() => void casesQuery.refetch()}
              onLoadMore={() => void casesQuery.fetchNextPage()}
            />
            <DevelopmentDetailPanel
              caseID={search.case}
              focusChat={search.focus === "chat"}
              hiddenOnMobile={!search.case}
              focusOnOpen={!wideLayout}
              onBack={() => {
                restoreFocusCaseRef.current = search.case ?? null
                const next = { ...search }
                delete next.case
                delete next.focus
                onSearchChange(next, true)
              }}
            />
          </div>
        </div>
      </div>
    </div>
  )
}

function DevelopmentCaseList({
  cases,
  selectedCaseID,
  hiddenOnMobile,
  loading,
  error,
  hasMore,
  loadingMore,
  onSelect,
  onRetry,
  onLoadMore,
}: {
  cases: PRDevelopmentCaseSummary[]
  selectedCaseID?: string
  hiddenOnMobile: boolean
  loading: boolean
  error: unknown
  hasMore: boolean
  loadingMore: boolean
  onSelect: (caseID: string, attentionRequired: boolean) => void
  onRetry: () => void
  onLoadMore: () => void
}) {
  const { t } = useTranslation()
  const loadLockedRef = useRef(false)

  useEffect(() => {
    if (!loadingMore) loadLockedRef.current = false
  }, [loadingMore])

  const loadMore = () => {
    if (!hasMore || loadingMore || loadLockedRef.current) return
    loadLockedRef.current = true
    onLoadMore()
  }

  const handleScroll = (event: UIEvent<HTMLDivElement>) => {
    const node = event.currentTarget
    if (node.scrollHeight - node.scrollTop - node.clientHeight <= 180) {
      loadMore()
    }
  }

  return (
    <section
      className={cn(
        "border-border bg-card/40 min-h-[24rem] min-w-0 flex-col overflow-hidden rounded-lg border lg:flex lg:min-h-0",
        hiddenOnMobile ? "hidden" : "flex",
      )}
    >
      <div className="border-border flex h-12 shrink-0 items-center justify-between border-b px-3">
        <div className="flex items-center gap-2">
          <IconGitPullRequest className="text-muted-foreground size-4" />
          <h2 className="text-sm font-medium">
            {t("pages.reviews.development.list_title", "Feedback on my PRs")}
          </h2>
        </div>
        <Badge variant="outline" className="font-mono">
          {cases.length}
        </Badge>
      </div>
      <div
        role="region"
        aria-label={t(
          "pages.reviews.development.list_region",
          "Captured PR feedback",
        )}
        className="min-h-0 flex-1 overflow-auto p-2"
        onScroll={handleScroll}
      >
        {loading ? (
          <DevelopmentMessage>
            {t("pages.reviews.development.loading", "Loading PR feedback…")}
          </DevelopmentMessage>
        ) : error && cases.length === 0 ? (
          <DevelopmentError error={error} onRetry={onRetry} />
        ) : cases.length === 0 ? (
          <DevelopmentMessage>
            {t(
              "pages.reviews.development.empty",
              "No captured PR feedback matches these filters.",
            )}
          </DevelopmentMessage>
        ) : (
          <div className="flex min-w-0 flex-col gap-1.5">
            {cases.map((developmentCase) => {
              const selected = developmentCase.id === selectedCaseID
              return (
                <button
                  type="button"
                  key={developmentCase.id}
                  data-pr-development-case-id={developmentCase.id}
                  aria-current={selected ? "true" : undefined}
                  onClick={() =>
                    onSelect(
                      developmentCase.id,
                      developmentCase.attention_required,
                    )
                  }
                  className={cn(
                    "border-border/70 hover:bg-muted/60 focus-visible:border-ring focus-visible:ring-ring/30 grid min-w-0 gap-1.5 rounded-md border px-3 py-2 text-left outline-none focus-visible:ring-2",
                    selected && "bg-accent/70 text-accent-foreground",
                  )}
                >
                  <div className="flex min-w-0 items-center justify-between gap-2">
                    <span className="min-w-0 truncate text-sm font-medium">
                      {developmentCase.repository} #
                      {developmentCase.pull_number}
                    </span>
                    <div className="flex shrink-0 items-center gap-1">
                      {developmentCase.attention_required ? (
                        <Badge variant="secondary">
                          {t(
                            "pages.reviews.development.ai_attention",
                            "AI attention",
                          )}
                        </Badge>
                      ) : null}
                      <span className="text-muted-foreground text-[10px]">
                        {t(
                          "pages.reviews.development.at_capture",
                          "At capture",
                        )}
                      </span>
                      <ReviewStateBadge
                        state={developmentCase.current_review_state}
                      />
                    </div>
                  </div>
                  <p className="text-muted-foreground min-w-0 truncate text-xs">
                    {t(
                      "pages.reviews.development.reviewed_by",
                      "Reviewed by {{reviewer}}",
                      { reviewer: developmentCase.review_author },
                    )}
                  </p>
                  <div className="text-muted-foreground flex min-w-0 items-center justify-between gap-2 text-[11px]">
                    <span className="min-w-0 truncate font-mono">
                      {t(
                        "pages.reviews.development.head_at_capture",
                        "Head at capture: {{ref}} · {{sha}}",
                        {
                          ref: developmentCase.head_ref,
                          sha: shortSHA(developmentCase.head_sha),
                        },
                      )}
                    </span>
                    <time
                      className="shrink-0"
                      dateTime={developmentCase.captured_at}
                    >
                      {formatDate(developmentCase.captured_at)}
                    </time>
                  </div>
                </button>
              )
            })}
            {error ? (
              <DevelopmentError compact error={error} onRetry={onRetry} />
            ) : null}
            {hasMore ? (
              <Button
                type="button"
                variant="ghost"
                size="sm"
                disabled={loadingMore}
                onClick={loadMore}
                className="mt-1 w-full"
              >
                {loadingMore
                  ? t("pages.reviews.list.loading_more", "Loading more…")
                  : t("pages.reviews.list.load_more", "Load more")}
              </Button>
            ) : null}
          </div>
        )}
      </div>
    </section>
  )
}

function retainNewestPRDevelopmentDetail(
  previous: unknown,
  incoming: unknown,
): unknown {
  const previousDetail = previous as PRDevelopmentCaseDetail | undefined
  const incomingDetail = incoming as PRDevelopmentCaseDetail | undefined
  if (previousDetail == null || incomingDetail == null) return incoming
  return mergePRDevelopmentDetail(previousDetail, incomingDetail, {
    capabilityFreshness: "authoritative",
  })
}

function mergePRDevelopmentDetail(
  previous: PRDevelopmentCaseDetail,
  incoming: PRDevelopmentCaseDetail,
  options: { capabilityFreshness: "authoritative" | "downgrade-only" },
): PRDevelopmentCaseDetail {
  if (previous.case.id !== incoming.case.id) return incoming
  const conversationAdvanced =
    incoming.conversation_version > previous.conversation_version
  const repairAdvanced = incoming.repair_revision > previous.repair_revision
  const localDevelopmentAdvanced = isNewerLocalDevelopment(
    previous.local_development,
    incoming.local_development,
  )
  const capabilitySource =
    options.capabilityFreshness === "authoritative" ||
    (previous.repair_available && !incoming.repair_available)
      ? incoming
      : previous
  const capabilityChanged =
    capabilitySource.repair_available !== previous.repair_available ||
    capabilitySource.repair_unavailable_reason !==
      previous.repair_unavailable_reason
  if (
    !conversationAdvanced &&
    !repairAdvanced &&
    !localDevelopmentAdvanced &&
    !capabilityChanged
  ) {
    return previous
  }

  const conversationSource = conversationAdvanced ? incoming : previous
  const repairSource = repairAdvanced ? incoming : previous
  const localDevelopmentSource =
    repairAdvanced || localDevelopmentAdvanced ? incoming : previous
  return {
    case: incoming.case,
    conversation_version: conversationSource.conversation_version,
    messages: conversationSource.messages,
    repair_available: capabilitySource.repair_available,
    ...(capabilitySource.repair_unavailable_reason === undefined
      ? {}
      : {
          repair_unavailable_reason: capabilitySource.repair_unavailable_reason,
        }),
    repair_revision: repairSource.repair_revision,
    ...(repairSource.repair_session === undefined
      ? {}
      : { repair_session: repairSource.repair_session }),
    ...(localDevelopmentSource.local_development === undefined
      ? {}
      : { local_development: localDevelopmentSource.local_development }),
  }
}

function isNewerLocalDevelopment(
  previous: PRDevelopmentLocalDevelopment | undefined,
  incoming: PRDevelopmentLocalDevelopment | undefined,
): boolean {
  if (incoming === undefined) return false
  if (previous === undefined) return true
  if (incoming.attempt_ordinal !== previous.attempt_ordinal) {
    return incoming.attempt_ordinal > previous.attempt_ordinal
  }
  const incomingStage = localDevelopmentEvidenceStage(incoming)
  const previousStage = localDevelopmentEvidenceStage(previous)
  if (incomingStage !== previousStage) return incomingStage > previousStage
  const incomingTime = Date.parse(incoming.updated_at)
  const previousTime = Date.parse(previous.updated_at)
  return incomingTime > previousTime
}

function localDevelopmentEvidenceStage(
  evidence: PRDevelopmentLocalDevelopment,
): number {
  switch (evidence.review_status) {
    case "not_started":
      return 0
    case "pending":
      return 1
    case "completed":
      return 2
  }
}

type DevelopmentMutationKind = "repair" | "chat" | "attention"

interface DevelopmentMutationState {
  repair: boolean
  chat: boolean
  attention: boolean
}

const idleDevelopmentMutationState: DevelopmentMutationState = {
  repair: false,
  chat: false,
  attention: false,
}

function DevelopmentDetailPanel({
  caseID,
  focusChat,
  hiddenOnMobile,
  focusOnOpen,
  onBack,
}: {
  caseID?: string
  focusChat: boolean
  hiddenOnMobile: boolean
  focusOnOpen: boolean
  onBack: () => void
}) {
  const { t } = useTranslation()
  const backButtonRef = useRef<HTMLButtonElement>(null)
  const focusedCaseRef = useRef<string | null>(null)
  const [repairDrafts, setRepairDrafts] = useState(
    () => new Map<string, string>(),
  )
  const [repairIntents, setRepairIntents] = useState(
    () => new Map<string, RepairStartIntent>(),
  )
  const [mutationStates, setMutationStates] = useState(
    () => new Map<string, DevelopmentMutationState>(),
  )
  const rememberRepairDraft = useCallback((id: string, value: string) => {
    setRepairDrafts((current) => {
      const next = new Map(current)
      if (value === "") next.delete(id)
      else next.set(id, value)
      return next
    })
  }, [])
  const rememberRepairIntent = useCallback(
    (id: string, value: RepairStartIntent | null) => {
      setRepairIntents((current) => {
        const next = new Map(current)
        if (value == null) next.delete(id)
        else next.set(id, value)
        return next
      })
    },
    [],
  )
  const rememberMutationPending = useCallback(
    (id: string, kind: DevelopmentMutationKind, pending: boolean) => {
      setMutationStates((current) => {
        const currentState = current.get(id) ?? idleDevelopmentMutationState
        if (currentState[kind] === pending) return current

        const nextState = { ...currentState, [kind]: pending }
        const next = new Map(current)
        if (!nextState.repair && !nextState.chat && !nextState.attention) {
          next.delete(id)
        } else next.set(id, nextState)
        return next
      })
    },
    [],
  )
  const detailQuery = useQuery({
    queryKey: ["pr-development", "detail", caseID],
    queryFn: () => getPRDevelopmentCase(caseID ?? ""),
    enabled: Boolean(caseID),
    structuralSharing: retainNewestPRDevelopmentDetail,
    refetchInterval: (query) =>
      shouldPollLocalDevelopment(query.state.data) ? 2000 : false,
  })
  const detail = detailQuery.data
  const developmentCase = detail?.case

  useEffect(() => {
    if (!caseID) {
      focusedCaseRef.current = null
      return
    }
    if (focusOnOpen && focusedCaseRef.current !== caseID) {
      focusedCaseRef.current = caseID
      backButtonRef.current?.focus()
    }
  }, [caseID, focusOnOpen])

  return (
    <section
      aria-label={t(
        "pages.reviews.development.detail_region",
        "PR feedback detail",
      )}
      className={cn(
        "border-border bg-card/40 min-h-[24rem] min-w-0 flex-col overflow-hidden rounded-lg border lg:flex lg:min-h-0",
        hiddenOnMobile ? "hidden" : "flex",
      )}
    >
      <div className="border-border flex min-h-12 shrink-0 items-center gap-2 border-b px-3 py-2">
        {caseID ? (
          <Button
            ref={backButtonRef}
            type="button"
            variant="ghost"
            size="icon-sm"
            className="lg:hidden"
            aria-label={t(
              "pages.reviews.development.back",
              "Back to PR feedback",
            )}
            onClick={onBack}
          >
            <IconArrowLeft className="size-4" />
          </Button>
        ) : null}
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-sm font-medium">
            {developmentCase
              ? `${developmentCase.repository} #${developmentCase.pull_number}`
              : t(
                  "pages.reviews.development.detail_title",
                  "PR feedback detail",
                )}
          </h2>
          {developmentCase ? (
            <p className="text-muted-foreground truncate text-xs">
              {t(
                "pages.reviews.development.reviewed_by",
                "Reviewed by {{reviewer}}",
                { reviewer: developmentCase.review_author },
              )}
            </p>
          ) : null}
        </div>
        {developmentCase ? (
          <div className="flex shrink-0 items-center gap-1">
            <Button asChild type="button" variant="outline" size="sm">
              <a
                href={developmentCase.pull_url}
                target="_blank"
                rel="noreferrer"
              >
                {t("pages.reviews.detail.open_pr", "Open PR")}
                <IconExternalLink className="size-4" />
              </a>
            </Button>
            <Button asChild type="button" variant="outline" size="sm">
              <a
                href={developmentCase.review_url}
                target="_blank"
                rel="noreferrer"
              >
                {t("pages.reviews.development.open_review", "Open review")}
                <IconExternalLink className="size-4" />
              </a>
            </Button>
          </div>
        ) : null}
      </div>

      <div className="min-h-0 flex-1 overflow-auto p-3 sm:p-4">
        {!caseID ? (
          <DevelopmentMessage>
            {t(
              "pages.reviews.development.select_prompt",
              "Select captured feedback to inspect it.",
            )}
          </DevelopmentMessage>
        ) : detailQuery.isPending ? (
          <DevelopmentMessage>
            {t(
              "pages.reviews.development.detail_loading",
              "Loading feedback detail…",
            )}
          </DevelopmentMessage>
        ) : detail ? (
          <DevelopmentCaseDetail
            detail={detail}
            focusRequested={focusChat}
            refreshError={detailQuery.isRefetchError ? detailQuery.error : null}
            refreshing={detailQuery.isFetching}
            repairDraft={repairDrafts.get(detail.case.id) ?? ""}
            repairIntent={repairIntents.get(detail.case.id) ?? null}
            mutationState={
              mutationStates.get(detail.case.id) ?? idleDevelopmentMutationState
            }
            onRememberRepairDraft={rememberRepairDraft}
            onRememberRepairIntent={rememberRepairIntent}
            onRememberMutationPending={rememberMutationPending}
            onRetryRefresh={() => void detailQuery.refetch()}
          />
        ) : detailQuery.error ? (
          <DevelopmentError
            error={detailQuery.error}
            onRetry={() => void detailQuery.refetch()}
          />
        ) : null}
      </div>
    </section>
  )
}

function DevelopmentCaseDetail({
  detail,
  focusRequested,
  refreshError,
  refreshing,
  repairDraft,
  repairIntent,
  mutationState,
  onRememberRepairDraft,
  onRememberRepairIntent,
  onRememberMutationPending,
  onRetryRefresh,
}: {
  detail: PRDevelopmentCaseDetail
  focusRequested: boolean
  refreshError: unknown
  refreshing: boolean
  repairDraft: string
  repairIntent: RepairStartIntent | null
  mutationState: DevelopmentMutationState
  onRememberRepairDraft: (caseID: string, value: string) => void
  onRememberRepairIntent: (
    caseID: string,
    value: RepairStartIntent | null,
  ) => void
  onRememberMutationPending: (
    caseID: string,
    kind: DevelopmentMutationKind,
    pending: boolean,
  ) => void
  onRetryRefresh: () => void
}) {
  const { t } = useTranslation()
  const developmentCase = detail.case
  const forked =
    developmentCase.head_repository.toLowerCase() !==
    developmentCase.base_repository.toLowerCase()

  return (
    <div className="mx-auto flex w-full max-w-4xl flex-col gap-4">
      <div aria-live="polite" aria-atomic="true">
        {refreshError ? (
          <div className="border-destructive/40 bg-destructive/5 rounded-md border">
            <DevelopmentError
              compact
              error={refreshError}
              onRetry={onRetryRefresh}
            />
          </div>
        ) : null}
      </div>
      <p className="text-muted-foreground text-xs font-medium">
        {t("pages.reviews.development.state_at_capture", "State at capture")}
      </p>
      <div className="flex flex-wrap gap-2">
        <Badge variant="outline">{pullStateLabel(developmentCase)}</Badge>
        {developmentCase.pull_draft ? (
          <Badge variant="secondary">
            {t("pages.reviews.development.draft", "Draft")}
          </Badge>
        ) : null}
        <ReviewStateBadge state={developmentCase.current_review_state} />
        {developmentCase.current_review_state !==
        developmentCase.submitted_review_state ? (
          <Badge variant="outline">
            {t(
              "pages.reviews.development.submitted_state",
              "Submitted as {{state}}",
              {
                state: reviewStateLabel(developmentCase.submitted_review_state),
              },
            )}
          </Badge>
        ) : null}
      </div>

      <section className="border-border rounded-lg border p-4">
        <h3 className="text-sm font-semibold">
          {t("pages.reviews.development.feedback", "Reviewer feedback")}
        </h3>
        <p className="text-muted-foreground mt-1 text-xs">
          {t(
            "pages.reviews.development.feedback_source",
            "Captured from the submitted provider review and shown as plain text.",
          )}
        </p>
        <div
          data-testid="pr-development-feedback"
          className="bg-muted/40 mt-3 min-h-24 rounded-md p-3 text-sm break-words whitespace-pre-wrap"
        >
          {developmentCase.feedback ||
            t(
              "pages.reviews.development.feedback_empty",
              "No review body was submitted.",
            )}
        </div>
      </section>

      <DevelopmentLocalRepair
        key={`repair-${developmentCase.id}`}
        detail={detail}
        refreshing={refreshing}
        storedDraft={repairDraft}
        storedIntent={repairIntent}
        mutationPending={mutationState.repair}
        otherMutationPending={mutationState.chat || mutationState.attention}
        onRememberDraft={onRememberRepairDraft}
        onRememberIntent={onRememberRepairIntent}
        onRememberMutationPending={onRememberMutationPending}
        onReload={onRetryRefresh}
      />

      <DevelopmentConversation
        key={`chat-${developmentCase.id}`}
        detail={detail}
        focusRequested={focusRequested}
        mutationPending={mutationState.chat}
        otherMutationPending={mutationState.repair || mutationState.attention}
        onRememberMutationPending={onRememberMutationPending}
      />

      <section className="border-border rounded-lg border p-4">
        <div className="flex items-center gap-2">
          <IconGitBranch className="text-muted-foreground size-4" />
          <h3 className="text-sm font-semibold">
            {t("pages.reviews.development.snapshot", "Captured snapshot")}
          </h3>
        </div>
        <p className="text-muted-foreground mt-1 text-xs">
          {t(
            "pages.reviews.development.snapshot_help",
            "This is provider state captured at {{captured}}. Open the PR for current state.",
            { captured: formatDate(developmentCase.captured_at) },
          )}
        </p>
        {forked ? (
          <p className="border-border bg-muted/30 mt-3 rounded-md border px-3 py-2 text-xs">
            {t(
              "pages.reviews.development.fork_notice",
              "This PR uses a fork: {{head}} targets {{base}}.",
              {
                head: developmentCase.head_repository,
                base: developmentCase.base_repository,
              },
            )}
          </p>
        ) : null}
        <dl className="mt-4 grid gap-3 text-sm sm:grid-cols-2">
          <SnapshotField
            label={t("pages.reviews.development.pull_author", "PR author")}
            value={developmentCase.pull_author}
          />
          <SnapshotField
            label={t("pages.reviews.development.reviewer", "Reviewer")}
            value={developmentCase.review_author}
          />
          <SnapshotField
            label={t("pages.reviews.development.base", "Base")}
            value={`${developmentCase.base_repository}:${developmentCase.base_ref}`}
            monospace
          />
          <SnapshotField
            label={t("pages.reviews.development.head", "Head")}
            value={`${developmentCase.head_repository}:${developmentCase.head_ref}`}
            monospace
          />
          <SnapshotField
            label={t("pages.reviews.development.base_sha", "Base commit")}
            value={developmentCase.base_sha}
            monospace
          />
          <SnapshotField
            label={t("pages.reviews.development.head_sha", "Head commit")}
            value={developmentCase.head_sha}
            monospace
          />
          <SnapshotField
            label={t(
              "pages.reviews.development.review_commit_sha",
              "Reviewed commit",
            )}
            value={developmentCase.review_commit_sha}
            monospace
          />
          <SnapshotField
            label={t(
              "pages.reviews.development.submitted_at",
              "Review submitted",
            )}
            value={formatDate(developmentCase.review_submitted_at)}
          />
        </dl>
      </section>
    </div>
  )
}

interface RepairStartIntent {
  expectedConversationVersion: number
  expectedRepairRevision: number
  expectedAttemptOrdinal: number
  requestID: string
  instruction: string
}

function DevelopmentLocalRepair({
  detail,
  refreshing,
  storedDraft,
  storedIntent,
  mutationPending,
  otherMutationPending,
  onRememberDraft,
  onRememberIntent,
  onRememberMutationPending,
  onReload,
}: {
  detail: PRDevelopmentCaseDetail
  refreshing: boolean
  storedDraft: string
  storedIntent: RepairStartIntent | null
  mutationPending: boolean
  otherMutationPending: boolean
  onRememberDraft: (caseID: string, value: string) => void
  onRememberIntent: (caseID: string, value: RepairStartIntent | null) => void
  onRememberMutationPending: (
    caseID: string,
    kind: DevelopmentMutationKind,
    pending: boolean,
  ) => void
  onReload: () => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState(storedDraft)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [intent, setIntentState] = useState<RepairStartIntent | null>(
    storedIntent,
  )
  const [localError, setLocalError] = useState<string>()
  const developmentCase = detail.case
  const detailQueryKey = [
    "pr-development",
    "detail",
    developmentCase.id,
  ] as const
  const normalizedDraft = normalizePRDevelopmentChatContent(draft)
  const draftBytes = new TextEncoder().encode(normalizedDraft).byteLength
  const draftInvalid =
    normalizedDraft === "" ||
    normalizedDraft.includes("\0") ||
    draftBytes > MAXIMUM_PR_DEVELOPMENT_REPAIR_INSTRUCTION_BYTES
  const latestAttempt = detail.repair_session?.attempts.at(-1)
  const repairActive = isActiveRepairStatus(latestAttempt?.status)
  const recoveryRequired = latestAttempt?.status === "recovery_required"
  const rememberIntent = useCallback(
    (next: RepairStartIntent | null) => {
      setIntentState(next)
      onRememberIntent(developmentCase.id, next)
    },
    [developmentCase.id, onRememberIntent],
  )

  const updateDetail = (incoming: PRDevelopmentCaseDetail) => {
    queryClient.setQueryData<PRDevelopmentCaseDetail>(
      detailQueryKey,
      (current) =>
        current == null
          ? incoming
          : mergePRDevelopmentDetail(current, incoming, {
              capabilityFreshness: "downgrade-only",
            }),
    )
  }

  const repairMutation = useMutation({
    mutationFn: (submitted: RepairStartIntent) =>
      startPRDevelopmentRepair(developmentCase.id, submitted),
    retry: false,
    onMutate: () => {
      onRememberMutationPending(developmentCase.id, "repair", true)
      setLocalError(undefined)
    },
    onSuccess: (updatedDetail) => {
      updateDetail(updatedDetail)
      rememberIntent(null)
      onRememberDraft(developmentCase.id, "")
      setDraft("")
    },
    onError: (error) => {
      if (error instanceof PRDevelopmentAPIError && error.detail) {
        updateDetail(error.detail)
      }
    },
    onSettled: () => {
      onRememberMutationPending(developmentCase.id, "repair", false)
    },
  })
  const resetRepairMutation = repairMutation.reset
  const repairPending = mutationPending || repairMutation.isPending

  useEffect(() => {
    if (
      intent == null ||
      detail.repair_revision <= intent.expectedRepairRevision
    ) {
      return
    }
    const matchingAttempt =
      detail.repair_session?.attempts[intent.expectedAttemptOrdinal]
    if (
      matchingAttempt?.conversation_version !==
        intent.expectedConversationVersion ||
      matchingAttempt.instruction !== intent.instruction
    ) {
      setLocalError(
        t(
          "pages.reviews.development.repair_history_changed",
          "Repair history changed in another tab. Your draft and retry identity were preserved; review the latest history before retrying.",
        ),
      )
      return
    }
    rememberIntent(null)
    setDraft((current) => {
      if (normalizePRDevelopmentChatContent(current) !== intent.instruction) {
        return current
      }
      onRememberDraft(developmentCase.id, "")
      return ""
    })
    resetRepairMutation()
  }, [
    detail.repair_revision,
    detail.repair_session?.attempts,
    developmentCase.id,
    intent,
    rememberIntent,
    onRememberDraft,
    resetRepairMutation,
    t,
  ])

  const confirmStart = () => {
    if (
      draftInvalid ||
      repairActive ||
      repairPending ||
      otherMutationPending ||
      !detail.repair_available
    ) {
      return
    }
    let submitted = intent
    if (submitted == null || submitted.instruction !== normalizedDraft) {
      try {
        submitted = {
          expectedConversationVersion: detail.conversation_version,
          expectedRepairRevision: detail.repair_revision,
          expectedAttemptOrdinal: detail.repair_session?.attempts.length ?? 0,
          requestID: createPRDevelopmentRepairRequestID(),
          instruction: normalizedDraft,
        }
      } catch (error) {
        setLocalError(
          error instanceof Error
            ? error.message
            : t(
                "pages.reviews.development.repair_start_error",
                "The local repair could not be started.",
              ),
        )
        setConfirmOpen(false)
        return
      }
      rememberIntent(submitted)
    }
    setConfirmOpen(false)
    repairMutation.mutate(submitted)
  }

  const retryStart = () => {
    if (
      intent == null ||
      repairActive ||
      repairPending ||
      otherMutationPending ||
      !detail.repair_available
    ) {
      return
    }
    repairMutation.mutate(intent)
  }

  return (
    <section className="border-border rounded-lg border p-4">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <IconTool className="text-muted-foreground size-4" />
            <h3 className="text-sm font-semibold">
              {t("pages.reviews.development.repair_title", "Local development")}
            </h3>
          </div>
          <p className="text-muted-foreground mt-1 text-xs">
            {t(
              "pages.reviews.development.repair_help",
              "Ask AI to edit a pinned local copy, run discovered local checks, record the exact commit, and review the result. This page does not push changes.",
            )}
          </p>
        </div>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          disabled={refreshing}
          onClick={onReload}
        >
          <IconRefresh className={cn("size-4", refreshing && "animate-spin")} />
          {t("pages.reviews.development.repair_reload", "Reload status")}
        </Button>
      </div>

      {detail.local_development ? (
        <LocalDevelopmentStatusCard evidence={detail.local_development} />
      ) : (
        <div
          role="status"
          aria-live="polite"
          aria-atomic="true"
          className="bg-muted/30 mt-3 rounded-md px-3 py-2 text-sm"
        >
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant={repairStatusBadgeVariant(latestAttempt?.status)}>
              {repairStatusLabel(latestAttempt?.status)}
            </Badge>
            <span>{repairStatusDescription(latestAttempt?.status)}</span>
          </div>
        </div>
      )}

      {detail.repair_session?.head_sha ? (
        <div className="border-border mt-3 rounded-md border px-3 py-2 text-xs">
          <p className="text-muted-foreground">
            {t("pages.reviews.development.repair_pinned_head", "Pinned head")}
          </p>
          <p className="mt-1 font-mono break-all">
            {detail.repair_session.head_repository}:
            {detail.repair_session.head_ref} · {detail.repair_session.head_sha}
          </p>
          <p className="text-muted-foreground mt-1">
            {t("pages.reviews.development.repair_agent", "Agent {{agent}}", {
              agent: detail.repair_session.agent_id,
            })}
          </p>
        </div>
      ) : detail.repair_session ? (
        <p className="text-muted-foreground mt-3 text-xs">
          {t("pages.reviews.development.repair_agent", "Agent {{agent}}", {
            agent: detail.repair_session.agent_id,
          })}
        </p>
      ) : null}

      {!detail.repair_available ? (
        <p className="border-border bg-muted/30 mt-3 rounded-md border px-3 py-2 text-xs">
          {t(
            "pages.reviews.development.repair_unavailable",
            "Starting a new local repair is unavailable because one or more required repair dependencies are unavailable. Existing history remains visible.",
          )}
        </p>
      ) : null}

      {recoveryRequired ? (
        <p className="border-destructive/40 bg-destructive/5 mt-3 rounded-md border px-3 py-2 text-xs">
          {t(
            "pages.reviews.development.repair_recovery_warning",
            "Partial local edits may already exist. Tell AI to inspect and preserve them before making further changes.",
          )}
        </p>
      ) : null}

      <div className="mt-4 flex flex-col gap-2">
        <Label htmlFor={`development-repair-${developmentCase.id}`}>
          {t(
            "pages.reviews.development.repair_instruction",
            "Local repair instruction",
          )}
        </Label>
        <Textarea
          id={`development-repair-${developmentCase.id}`}
          value={draft}
          rows={4}
          aria-invalid={
            normalizedDraft !== "" &&
            (draftBytes > MAXIMUM_PR_DEVELOPMENT_REPAIR_INSTRUCTION_BYTES ||
              normalizedDraft.includes("\0"))
          }
          placeholder={t(
            "pages.reviews.development.repair_placeholder",
            "Describe what the AI should change in the local checkout…",
          )}
          disabled={
            repairPending ||
            otherMutationPending ||
            repairActive ||
            !detail.repair_available
          }
          onChange={(event) => {
            const next = event.target.value
            setDraft(next)
            onRememberDraft(developmentCase.id, next)
            setLocalError(undefined)
            if (
              intent != null &&
              normalizePRDevelopmentChatContent(next) !== intent.instruction
            ) {
              rememberIntent(null)
              resetRepairMutation()
            }
          }}
        />
        {draftBytes > MAXIMUM_PR_DEVELOPMENT_REPAIR_INSTRUCTION_BYTES ? (
          <p className="text-destructive text-xs" role="alert">
            {t(
              "pages.reviews.development.repair_too_large",
              "The instruction must be at most 4 KiB.",
            )}
          </p>
        ) : null}
        {localError || repairMutation.isError ? (
          <div
            className="border-destructive/40 bg-destructive/5 flex flex-wrap items-center gap-2 rounded-md border p-2"
            role="alert"
          >
            <p className="text-destructive min-w-0 flex-1 text-xs">
              {localError ??
                (repairMutation.error instanceof Error
                  ? repairMutation.error.message
                  : t(
                      "pages.reviews.development.repair_start_error",
                      "The local repair could not be started.",
                    ))}
            </p>
            {intent != null ? (
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={
                  repairPending ||
                  otherMutationPending ||
                  repairActive ||
                  !detail.repair_available
                }
                onClick={retryStart}
              >
                {repairPending
                  ? t("pages.reviews.development.repair_starting", "Starting…")
                  : t(
                      "pages.reviews.development.repair_retry_start",
                      "Retry start",
                    )}
              </Button>
            ) : null}
          </div>
        ) : null}
        <div className="flex justify-end">
          <Button
            type="button"
            disabled={
              draftInvalid ||
              repairPending ||
              otherMutationPending ||
              repairActive ||
              !detail.repair_available
            }
            onClick={() => setConfirmOpen(true)}
          >
            <IconTool className="size-4" />
            {repairPending
              ? t("pages.reviews.development.repair_starting", "Starting…")
              : recoveryRequired
                ? t(
                    "pages.reviews.development.repair_continue",
                    "Continue local repair",
                  )
                : t(
                    "pages.reviews.development.repair_start",
                    "Start local repair",
                  )}
          </Button>
        </div>
      </div>

      {detail.repair_session?.attempts.length ? (
        <div className="mt-5">
          <h4 className="text-sm font-medium">
            {t("pages.reviews.development.repair_history", "Repair history")}
          </h4>
          <ol
            aria-label={t(
              "pages.reviews.development.repair_history",
              "Repair history",
            )}
            className="mt-2 flex flex-col gap-2"
          >
            {[...detail.repair_session.attempts].reverse().map((attempt) => (
              <RepairAttemptCard key={attempt.id} attempt={attempt} />
            ))}
          </ol>
        </div>
      ) : null}

      <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {recoveryRequired
                ? t(
                    "pages.reviews.development.repair_recovery_confirm_title",
                    "Continue from this recovery-required repair?",
                  )
                : t(
                    "pages.reviews.development.repair_confirm_title",
                    "Start this local repair?",
                  )}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {recoveryRequired
                ? t(
                    "pages.reviews.development.repair_recovery_confirm_description",
                    "Partial edits may exist in the same pinned local workspace. Tell AI to inspect and preserve them before continuing. The attempt will then run local checks, record a commit when files changed, and receive AI review. Nothing will be changed on GitHub.",
                  )
                : t(
                    "pages.reviews.development.repair_confirm_description",
                    "AI will edit a pinned local copy using this instruction, run discovered local checks, record a commit when files changed, and review the result. Nothing will be changed on GitHub.",
                  )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={repairPending}>
              {t("pages.reviews.development.repair_cancel", "Cancel")}
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={
                draftInvalid ||
                repairPending ||
                otherMutationPending ||
                repairActive ||
                !detail.repair_available
              }
              onClick={(event) => {
                event.preventDefault()
                confirmStart()
              }}
            >
              {repairPending
                ? t("pages.reviews.development.repair_starting", "Starting…")
                : recoveryRequired
                  ? t(
                      "pages.reviews.development.repair_continue",
                      "Continue local repair",
                    )
                  : t(
                      "pages.reviews.development.repair_confirm",
                      "Start local repair",
                    )}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}

function LocalDevelopmentStatusCard({
  evidence,
}: {
  evidence: PRDevelopmentLocalDevelopment
}) {
  const { t } = useTranslation()
  return (
    <div
      role="status"
      aria-live="polite"
      aria-atomic="true"
      data-testid="local-development-status"
      className="border-border bg-muted/30 mt-3 rounded-md border p-3"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <p className="text-sm font-medium">
            {t(
              "pages.reviews.development.local_status",
              "Local development status",
            )}
          </p>
          <Badge variant={localDevelopmentBadgeVariant(evidence)}>
            {localDevelopmentLabel(evidence)}
          </Badge>
        </div>
        <time
          className="text-muted-foreground text-xs"
          dateTime={evidence.updated_at}
        >
          {formatDate(evidence.updated_at)}
        </time>
      </div>

      <dl className="mt-3 grid gap-3 text-sm sm:grid-cols-3">
        <div>
          <dt className="text-muted-foreground text-xs">
            {t("pages.reviews.development.local_attempt", "Latest attempt")}
          </dt>
          <dd className="mt-1 flex flex-wrap items-center gap-2">
            <span>
              {t(
                "pages.reviews.development.repair_attempt",
                "Attempt {{number}}",
                { number: evidence.attempt_ordinal + 1 },
              )}
            </span>
            <Badge variant={repairStatusBadgeVariant(evidence.attempt_status)}>
              {repairStatusLabel(evidence.attempt_status)}
            </Badge>
          </dd>
        </div>
        <div>
          <dt className="text-muted-foreground text-xs">
            {t("pages.reviews.development.local_commit", "Local commit")}
          </dt>
          <dd className="mt-1">
            {localCommitLabel(evidence)}
            {evidence.commit_sha ? (
              <code className="ml-1 font-mono" title={evidence.commit_sha}>
                {" "}
                {shortSHA(evidence.commit_sha)}
              </code>
            ) : null}
          </dd>
        </div>
        <div>
          <dt className="text-muted-foreground text-xs">
            {t("pages.reviews.development.local_ci", "Local CI")}
          </dt>
          <dd className="mt-1">
            <Badge variant={ciStatusBadgeVariant(evidence.ci_status)}>
              {ciStatusLabel(evidence.ci_status)}
            </Badge>
          </dd>
        </div>
        <div className="sm:col-span-3">
          <dt className="text-muted-foreground text-xs">
            {t("pages.reviews.development.local_ai_review", "AI review")}
          </dt>
          <dd className="mt-1 flex flex-wrap items-center gap-2">
            <Badge variant={localReviewBadgeVariant(evidence)}>
              {localReviewLabel(evidence)}
            </Badge>
            {evidence.review_finding_count > 0 ? (
              <span className="text-muted-foreground text-xs">
                {t(
                  "pages.reviews.development.local_findings",
                  "{{count}} finding(s)",
                  { count: evidence.review_finding_count },
                )}
              </span>
            ) : null}
          </dd>
        </div>
      </dl>

      {evidence.review_summary ? (
        <p className="mt-2 text-sm break-words whitespace-pre-wrap">
          {evidence.review_summary}
        </p>
      ) : null}
      {evidence.ci_plan_digest && evidence.ci_result_digest ? (
        <p className="text-muted-foreground mt-2 text-xs">
          {t(
            "pages.reviews.development.local_ci_evidence",
            "CI evidence: plan {{plan}} · result {{result}}",
            {
              plan: shortSHA(evidence.ci_plan_digest),
              result: shortSHA(evidence.ci_result_digest),
            },
          )}
        </p>
      ) : null}
      <p className="text-muted-foreground mt-2 text-xs">
        {localDevelopmentDescription(evidence)}{" "}
        {t(
          "pages.reviews.development.local_only_notice",
          "This status covers local work only; it does not mean changes were pushed.",
        )}
      </p>
    </div>
  )
}

function RepairAttemptCard({
  attempt,
}: {
  attempt: PRDevelopmentRepairAttempt
}) {
  const { t } = useTranslation()
  return (
    <li className="border-border min-w-0 rounded-md border p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <span className="text-xs font-medium">
            {t(
              "pages.reviews.development.repair_attempt",
              "Attempt {{number}}",
              { number: attempt.ordinal + 1 },
            )}
          </span>
          <Badge variant={repairStatusBadgeVariant(attempt.status)}>
            {repairStatusLabel(attempt.status)}
          </Badge>
        </div>
        <time
          className="text-muted-foreground text-xs"
          dateTime={attempt.updated_at}
        >
          {formatDate(attempt.updated_at)}
        </time>
      </div>
      <p className="mt-2 text-sm break-words whitespace-pre-wrap">
        {attempt.instruction}
      </p>
      {attempt.summary ? (
        <div className="bg-muted/30 mt-2 rounded-md p-2">
          <p className="text-muted-foreground text-xs font-medium">
            {t("pages.reviews.development.repair_summary", "Outcome summary")}
          </p>
          <p className="mt-1 text-sm break-words whitespace-pre-wrap">
            {attempt.summary}
          </p>
        </div>
      ) : null}
      {attempt.error_code ? (
        <p className="text-destructive mt-2 text-xs">
          {repairErrorLabel(attempt.error_code)}
        </p>
      ) : null}
    </li>
  )
}

function DevelopmentConversation({
  detail,
  focusRequested,
  mutationPending,
  otherMutationPending,
  onRememberMutationPending,
}: {
  detail: PRDevelopmentCaseDetail
  focusRequested: boolean
  mutationPending: boolean
  otherMutationPending: boolean
  onRememberMutationPending: (
    caseID: string,
    kind: DevelopmentMutationKind,
    pending: boolean,
  ) => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState("")
  const [attentionResponse, setAttentionResponse] = useState("")
  const [attentionError, setAttentionError] = useState<string>()
  const [attentionPending, setAttentionPending] = useState(false)
  const [failedMessageCommitted, setFailedMessageCommitted] = useState(false)
  const [restoreFocusAfterRetry, setRestoreFocusAfterRetry] = useState(false)
  const sectionRef = useRef<HTMLElement>(null)
  const headingRef = useRef<HTMLHeadingElement>(null)
  const composerRef = useRef<HTMLTextAreaElement>(null)
  const attentionInputRef = useRef<HTMLTextAreaElement>(null)
  const recoveryButtonRef = useRef<HTMLButtonElement>(null)
  const focusedTargetsRef = useRef(new Set<string>())
  const [uncertainSubmission, setUncertainSubmission] = useState<{
    expectedVersion: number
    content: string
  } | null>(null)
  const developmentCase = detail.case
  const detailQueryKey = [
    "pr-development",
    "detail",
    developmentCase.id,
  ] as const
  const attentionQueryKey = [
    "pr-development",
    "attention",
    developmentCase.id,
  ] as const
  const attentionQuery = useQuery({
    queryKey: attentionQueryKey,
    queryFn: ({ signal }) =>
      getPRDevelopmentAttention(developmentCase.id, signal),
    retry: false,
    refetchInterval: (query) =>
      attentionProjectionPollInterval(query.state.data),
  })
  const attention = attentionQuery.data
  const actionableAttentionTurn = findActionableAttentionTurn(attention)
  const showAttention =
    focusRequested ||
    attentionQuery.isPending ||
    Boolean(attentionQuery.error) ||
    attentionProjectionIsVisible(attention)
  const normalizedDraft = normalizePRDevelopmentChatContent(draft)
  const draftBytes = new TextEncoder().encode(normalizedDraft).byteLength
  const draftInvalid =
    normalizedDraft === "" ||
    normalizedDraft.includes("\0") ||
    draftBytes > MAXIMUM_PR_DEVELOPMENT_CHAT_BYTES
  const uncertainMessageCommitted =
    uncertainSubmission != null &&
    conversationContainsSubmittedMessage(detail, uncertainSubmission)
  const uncertainTurnCompleted =
    uncertainSubmission != null &&
    conversationContainsCompletedTurn(detail, uncertainSubmission)
  const messageWasCommitted =
    failedMessageCommitted || uncertainMessageCommitted
  const updateConversation = (incoming: PRDevelopmentCaseDetail) => {
    queryClient.setQueryData<PRDevelopmentCaseDetail>(
      detailQueryKey,
      (current) =>
        current == null
          ? incoming
          : mergePRDevelopmentDetail(current, incoming, {
              capabilityFreshness: "downgrade-only",
            }),
    )
  }

  const chatMutation = useMutation({
    mutationFn: ({
      content,
      expectedVersion,
    }: {
      content: string
      expectedVersion: number
    }) =>
      chatAboutPRDevelopmentCase(developmentCase.id, expectedVersion, content),
    retry: false,
    onMutate: () => {
      onRememberMutationPending(developmentCase.id, "chat", true)
      setFailedMessageCommitted(false)
    },
    onSuccess: (updatedDetail) => {
      updateConversation(updatedDetail)
      setUncertainSubmission(null)
      setDraft("")
    },
    onError: (error, submitted) => {
      if (error instanceof PRDevelopmentAPIError && error.detail) {
        updateConversation(error.detail)
        const committed = conversationContainsSubmittedMessage(
          error.detail,
          submitted,
        )
        setFailedMessageCommitted(committed)
        if (committed) {
          setUncertainSubmission(submitted)
          setDraft("")
        } else if (error.status === 409) {
          setUncertainSubmission(null)
        } else {
          setUncertainSubmission(submitted)
        }
      } else {
        setUncertainSubmission(submitted)
      }
    },
    onSettled: () => {
      onRememberMutationPending(developmentCase.id, "chat", false)
    },
  })
  const resetChatMutation = chatMutation.reset
  const chatPending = mutationPending || chatMutation.isPending

  useEffect(() => {
    if (!focusRequested) return
    const responseToken = actionableAttentionTurn?.response_token
    const focusKey = responseToken
      ? `${developmentCase.id}:${responseToken}`
      : `${developmentCase.id}:chat`
    if (focusedTargetsRef.current.has(focusKey)) return
    const target =
      actionableAttentionTurn?.status === "waiting" && attention?.can_respond
        ? attentionInputRef.current
        : actionableAttentionTurn?.status === "recovery_required" &&
            attention?.can_respond
          ? recoveryButtonRef.current
          : (composerRef.current ?? headingRef.current)
    if (!target || !sectionRef.current) return
    const frame = window.requestAnimationFrame(() => {
      if (!target.isConnected || !sectionRef.current) return
      sectionRef.current.scrollIntoView({ block: "start" })
      target.focus({ preventScroll: true })
      focusedTargetsRef.current.add(focusKey)
    })
    return () => window.cancelAnimationFrame(frame)
  }, [
    actionableAttentionTurn,
    attention?.can_respond,
    developmentCase.id,
    focusRequested,
  ])

  useEffect(() => {
    if (uncertainSubmission == null || !uncertainMessageCommitted) {
      return
    }
    setUncertainSubmission(null)
    setFailedMessageCommitted(!uncertainTurnCompleted)
    setDraft((current) =>
      normalizePRDevelopmentChatContent(current) === uncertainSubmission.content
        ? ""
        : current,
    )
    if (uncertainTurnCompleted) {
      resetChatMutation()
    }
  }, [
    resetChatMutation,
    uncertainMessageCommitted,
    uncertainSubmission,
    uncertainTurnCompleted,
  ])

  useEffect(() => {
    if (chatPending || !restoreFocusAfterRetry) {
      return
    }
    setRestoreFocusAfterRetry(false)
    composerRef.current?.focus()
  }, [chatPending, restoreFocusAfterRetry])

  const send = (event?: FormEvent) => {
    event?.preventDefault()
    if (
      chatPending ||
      attentionPending ||
      otherMutationPending ||
      draftInvalid ||
      (messageWasCommitted && uncertainSubmission?.content === normalizedDraft)
    ) {
      return
    }
    chatMutation.mutate({
      content: normalizedDraft,
      expectedVersion: detail.conversation_version,
    })
  }

  const respondToAttention = async (response: string) => {
    if (
      !attention?.can_respond ||
      !actionableAttentionTurn?.response_token ||
      attentionPending ||
      chatPending ||
      otherMutationPending
    ) {
      return
    }
    const targetIndex = attention.turns.lastIndexOf(actionableAttentionTurn)
    setAttentionPending(true)
    setAttentionError(undefined)
    onRememberMutationPending(developmentCase.id, "attention", true)
    try {
      const next = await respondToPRDevelopmentAttention(
        developmentCase.id,
        attention.case_version,
        actionableAttentionTurn.response_token,
        response,
      )
      queryClient.setQueryData(attentionQueryKey, next)
      setAttentionResponse("")
    } catch (error) {
      setAttentionError(prDevelopmentAttentionErrorMessage(error, t))
      const refreshed = await attentionQuery.refetch()
      if (
        !refreshed.isError &&
        targetIndex >= 0 &&
        attentionProjectionContainsResponse(
          refreshed.data,
          targetIndex,
          trimGoSpace(response),
          actionableAttentionTurn.status,
        )
      ) {
        setAttentionError(undefined)
        setAttentionResponse("")
      }
    } finally {
      setAttentionPending(false)
      onRememberMutationPending(developmentCase.id, "attention", false)
    }
  }

  const submitAttention = (event: FormEvent) => {
    event.preventDefault()
    const normalized = trimGoSpace(attentionResponse)
    const responseBytes = new TextEncoder().encode(normalized).byteLength
    if (
      normalized !== "" &&
      responseBytes <= PR_DEVELOPMENT_ATTENTION_RESPONSE_MAXIMUM_BYTES
    ) {
      void respondToAttention(normalized)
    }
  }

  return (
    <section
      ref={sectionRef}
      aria-labelledby={`development-chat-heading-${developmentCase.id}`}
      className="border-border scroll-mt-3 rounded-lg border p-4"
    >
      <div className="flex items-center gap-2">
        <IconMessageCircle className="text-muted-foreground size-4" />
        <h3
          ref={headingRef}
          id={`development-chat-heading-${developmentCase.id}`}
          tabIndex={-1}
          className="text-sm font-semibold outline-none"
        >
          {t("pages.reviews.development.chat_title", "Discuss with AI")}
        </h3>
      </div>
      <p className="text-muted-foreground mt-1 text-xs">
        {t(
          "pages.reviews.development.chat_advisory",
          "AI advice is based on this captured snapshot and conversation. It cannot inspect a checkout, read current GitHub state, edit code, run checks, or push changes.",
        )}
      </p>

      {showAttention ? (
        <AttentionConversation
          projection={attention}
          loading={attentionQuery.isPending}
          loadError={attentionQuery.error}
          actionError={attentionError}
          response={attentionResponse}
          pending={attentionPending || chatPending || otherMutationPending}
          maximumResponseBytes={PR_DEVELOPMENT_ATTENTION_RESPONSE_MAXIMUM_BYTES}
          idPrefix={`pr-development-attention-${developmentCase.id}`}
          attentionInputRef={attentionInputRef}
          recoveryButtonRef={recoveryButtonRef}
          onResponseChange={setAttentionResponse}
          onSubmit={submitAttention}
          onRetryLoad={() => void attentionQuery.refetch()}
          onRetryContinuation={() => {
            if (actionableAttentionTurn?.response) {
              void respondToAttention(actionableAttentionTurn.response)
            }
          }}
        />
      ) : null}

      {detail.messages.length === 0 ? (
        <p className="text-muted-foreground bg-muted/30 mt-3 rounded-md p-3 text-sm">
          {t(
            "pages.reviews.development.chat_empty",
            "Ask about the reviewer’s feedback or discuss how to approach it.",
          )}
        </p>
      ) : null}
      <div
        role="log"
        aria-live="polite"
        aria-relevant="additions text"
        aria-atomic="false"
        aria-busy={chatPending || attentionPending}
        aria-label={t(
          "pages.reviews.development.chat_history",
          "Development conversation",
        )}
        className={cn(
          "flex flex-col gap-2",
          detail.messages.length === 0 ? "sr-only" : "mt-3",
        )}
      >
        <ol className="flex flex-col gap-2">
          {detail.messages.map((message) => (
            <DevelopmentConversationMessage
              key={message.id}
              message={message}
            />
          ))}
        </ol>
      </div>

      <form className="mt-4 flex flex-col gap-2" onSubmit={send}>
        <Label htmlFor={`development-chat-${developmentCase.id}`}>
          {t(
            "pages.reviews.development.chat_message",
            "Message AI about this feedback",
          )}
        </Label>
        <Textarea
          ref={composerRef}
          id={`development-chat-${developmentCase.id}`}
          value={draft}
          rows={3}
          aria-invalid={
            normalizedDraft !== "" &&
            (draftBytes > MAXIMUM_PR_DEVELOPMENT_CHAT_BYTES ||
              normalizedDraft.includes("\0"))
          }
          placeholder={t(
            "pages.reviews.development.chat_placeholder",
            "Ask a question or steer the discussion…",
          )}
          onChange={(event) => setDraft(event.target.value)}
          disabled={chatPending || attentionPending || otherMutationPending}
        />
        {draftBytes > MAXIMUM_PR_DEVELOPMENT_CHAT_BYTES ? (
          <p className="text-destructive text-xs" role="alert">
            {t(
              "pages.reviews.development.chat_too_large",
              "The message must be at most 32 KiB.",
            )}
          </p>
        ) : null}
        {(chatMutation.isError || (chatPending && restoreFocusAfterRetry)) &&
        !uncertainTurnCompleted ? (
          <div
            className="border-destructive/40 bg-destructive/5 flex flex-wrap items-center gap-2 rounded-md border p-2"
            role="alert"
          >
            <p className="text-destructive min-w-0 flex-1 text-xs">
              {messageWasCommitted
                ? t(
                    "pages.reviews.development.chat_saved_response_failed",
                    "Your message was saved, but the AI response could not be completed. Reload before deciding whether to send a new message.",
                  )
                : chatMutation.error instanceof Error
                  ? chatMutation.error.message
                  : t(
                      "pages.reviews.development.chat_error",
                      "The AI response could not be completed.",
                    )}
            </p>
            {!messageWasCommitted ? (
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={
                  draftInvalid ||
                  chatPending ||
                  attentionPending ||
                  otherMutationPending
                }
                onClick={() => {
                  setRestoreFocusAfterRetry(true)
                  send()
                }}
              >
                {chatPending
                  ? t("pages.reviews.development.chat_sending", "Sending…")
                  : t("pages.reviews.retry", "Retry")}
              </Button>
            ) : null}
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => {
                void queryClient.invalidateQueries({ queryKey: detailQueryKey })
              }}
            >
              {t(
                "pages.reviews.development.chat_reload",
                "Reload conversation",
              )}
            </Button>
          </div>
        ) : null}
        <div className="flex justify-end">
          <Button
            type="submit"
            disabled={
              draftInvalid ||
              chatPending ||
              attentionPending ||
              otherMutationPending
            }
          >
            <IconSend className="size-4" />
            {chatPending
              ? t("pages.reviews.development.chat_sending", "Sending…")
              : t("pages.reviews.development.chat_send", "Send")}
          </Button>
        </div>
      </form>
    </section>
  )
}

function conversationContainsSubmittedMessage(
  detail: PRDevelopmentCaseDetail,
  submitted: { expectedVersion: number; content: string },
): boolean {
  const message = detail.messages[submitted.expectedVersion]
  return (
    detail.conversation_version > submitted.expectedVersion &&
    message?.role === "user" &&
    message.content === submitted.content
  )
}

function conversationContainsCompletedTurn(
  detail: PRDevelopmentCaseDetail,
  submitted: { expectedVersion: number; content: string },
): boolean {
  const assistant = detail.messages[submitted.expectedVersion + 1]
  return (
    conversationContainsSubmittedMessage(detail, submitted) &&
    detail.conversation_version > submitted.expectedVersion + 1 &&
    assistant?.role === "assistant"
  )
}

function DevelopmentConversationMessage({
  message,
}: {
  message: PRDevelopmentMessage
}) {
  const { t } = useTranslation()
  const fromUser = message.role === "user"
  return (
    <li
      className={cn(
        "border-border min-w-0 rounded-md border p-3",
        fromUser ? "bg-muted/50" : "bg-card",
      )}
    >
      <div className="flex items-center justify-between gap-2 text-xs">
        <span className="font-medium">
          {fromUser
            ? t("pages.reviews.development.chat_user", "You")
            : t("pages.reviews.development.chat_assistant", "AI")}
        </span>
        <time className="text-muted-foreground" dateTime={message.created_at}>
          {formatDate(message.created_at)}
        </time>
      </div>
      <div
        data-testid={`pr-development-message-${message.ordinal}`}
        className="mt-2 text-sm break-words whitespace-pre-wrap"
      >
        {message.content}
      </div>
    </li>
  )
}

function SnapshotField({
  label,
  value,
  monospace = false,
}: {
  label: string
  value: string
  monospace?: boolean
}) {
  return (
    <div className="min-w-0">
      <dt className="text-muted-foreground text-xs">{label}</dt>
      <dd className={cn("mt-0.5 break-all", monospace && "font-mono text-xs")}>
        {value}
      </dd>
    </div>
  )
}

function ReviewStateBadge({ state }: { state: PRDevelopmentReviewState }) {
  const variant = state === "approved" ? "secondary" : "outline"
  return <Badge variant={variant}>{reviewStateLabel(state)}</Badge>
}

function DevelopmentError({
  error,
  onRetry,
  compact = false,
}: {
  error: unknown
  onRetry: () => void
  compact?: boolean
}) {
  const { t } = useTranslation()
  return (
    <div
      role="alert"
      className={cn(
        "flex flex-col items-center justify-center gap-3 text-center",
        compact ? "p-3" : "min-h-40 p-6",
      )}
    >
      <p className="text-destructive text-sm">
        {error instanceof Error && error.message
          ? error.message
          : t(
              "pages.reviews.development.error",
              "PR feedback could not be loaded.",
            )}
      </p>
      <Button type="button" variant="outline" size="sm" onClick={onRetry}>
        {t("pages.reviews.retry", "Retry")}
      </Button>
    </div>
  )
}

function DevelopmentMessage({ children }: { children: string }) {
  return (
    <div className="text-muted-foreground flex min-h-40 items-center justify-center p-6 text-center text-sm">
      {children}
    </div>
  )
}

function deduplicateCases(
  cases: PRDevelopmentCaseSummary[],
): PRDevelopmentCaseSummary[] {
  const seen = new Set<string>()
  return cases.filter((developmentCase) => {
    if (seen.has(developmentCase.id)) return false
    seen.add(developmentCase.id)
    return true
  })
}

function reviewStateLabel(state: PRDevelopmentReviewState): string {
  switch (state) {
    case "approved":
      return "Approved"
    case "changes_requested":
      return "Changes requested"
    case "commented":
      return "Commented"
    case "dismissed":
      return "Dismissed"
  }
}

function pullStateLabel(developmentCase: PRDevelopmentCase): string {
  if (developmentCase.pull_merged) return "Merged"
  return developmentCase.pull_state === "open" ? "Open" : "Closed"
}

function formatDate(value: string): string {
  const date = new Date(value)
  return Number.isFinite(date.getTime()) ? date.toLocaleString() : value
}

function shortSHA(value: string): string {
  return value.slice(0, 8)
}

function shouldPollLocalDevelopment(
  detail: PRDevelopmentCaseDetail | undefined,
): boolean {
  const evidence = detail?.local_development
  return (
    isActiveRepairStatus(detail?.repair_session?.attempts.at(-1)?.status) ||
    (evidence?.attempt_status === "completed" &&
      evidence.review_status === "pending")
  )
}

function isActiveRepairStatus(
  status: PRDevelopmentRepairStatus | undefined,
): boolean {
  return status === "queued" || status === "preparing" || status === "running"
}

function repairStatusLabel(
  status: PRDevelopmentRepairStatus | undefined,
): string {
  switch (status) {
    case undefined:
      return "Not started"
    case "queued":
      return "Queued"
    case "preparing":
      return "Preparing"
    case "running":
      return "Running"
    case "completed":
      return "Completed"
    case "failed":
      return "Failed"
    case "recovery_required":
      return "Recovery required"
  }
}

function repairStatusDescription(
  status: PRDevelopmentRepairStatus | undefined,
): string {
  switch (status) {
    case undefined:
      return "No local repair has been started."
    case "queued":
      return "The local repair is waiting to start."
    case "preparing":
      return "Current PR state is being verified before local editing starts."
    case "running":
      return "AI is editing the pinned local copy."
    case "completed":
      return "The local attempt completed, but bound commit, CI, and AI-review evidence is not available."
    case "failed":
      return "The repair stopped before AI editing began."
    case "recovery_required":
      return "Recovery is required. Partial local edits may already exist in the pinned workspace."
  }
}

function repairStatusBadgeVariant(
  status: PRDevelopmentRepairStatus | undefined,
): "outline" | "secondary" | "destructive" {
  if (status === "completed") return "secondary"
  if (status === "failed" || status === "recovery_required") {
    return "destructive"
  }
  return "outline"
}

function localDevelopmentLabel(
  evidence: PRDevelopmentLocalDevelopment,
): string {
  if (evidence.local_ready) return "Ready locally"
  if (evidence.review_outcome === "attention_required") {
    return "Needs your attention"
  }
  if (evidence.review_outcome === "changes_required") {
    return "Changes required"
  }
  if (evidence.ci_status && evidence.ci_status !== "passed") {
    return "Local checks not green"
  }
  if (evidence.review_status === "pending") return "AI review pending"
  return repairStatusLabel(evidence.attempt_status)
}

function localDevelopmentBadgeVariant(
  evidence: PRDevelopmentLocalDevelopment,
): "default" | "outline" | "secondary" | "destructive" {
  if (evidence.local_ready) return "default"
  if (
    evidence.review_outcome === "changes_required" ||
    (evidence.ci_status !== undefined && evidence.ci_status !== "passed") ||
    evidence.attempt_status === "failed" ||
    evidence.attempt_status === "recovery_required"
  ) {
    return "destructive"
  }
  if (evidence.review_outcome === "attention_required") return "secondary"
  return "outline"
}

function localCommitLabel(evidence: PRDevelopmentLocalDevelopment): string {
  if (!evidence.commit_sha) return "Evidence unavailable"
  return evidence.no_changes ? "No file changes · retained" : "Recorded"
}

function ciStatusLabel(status: PRDevelopmentCIStatus | undefined): string {
  switch (status) {
    case undefined:
      return "Evidence unavailable"
    case "passed":
      return "Passed"
    case "failed":
      return "Failed"
    case "incomplete":
      return "Incomplete"
    case "plan_changed":
      return "Plan changed"
    case "timed_out":
      return "Timed out"
    case "canceled":
      return "Canceled"
    case "output_limit_exceeded":
      return "Output limit exceeded"
    case "environment_unavailable":
      return "Environment unavailable"
    case "infrastructure_error":
      return "Infrastructure error"
  }
}

function ciStatusBadgeVariant(
  status: PRDevelopmentCIStatus | undefined,
): "outline" | "secondary" | "destructive" {
  if (status === "passed") return "secondary"
  if (status !== undefined) return "destructive"
  return "outline"
}

function localReviewLabel(evidence: PRDevelopmentLocalDevelopment): string {
  if (evidence.review_status === "not_started") return "Not started"
  if (evidence.review_status === "pending") return "Pending"
  switch (evidence.review_outcome) {
    case "passed":
      return "Passed"
    case "changes_required":
      return "Changes required"
    case "attention_required":
      return "Needs attention"
    case undefined:
      return "Completed"
  }
}

function localReviewBadgeVariant(
  evidence: PRDevelopmentLocalDevelopment,
): "outline" | "secondary" | "destructive" {
  if (evidence.review_outcome === "passed") return "secondary"
  if (evidence.review_outcome === "changes_required") return "destructive"
  return "outline"
}

function localDevelopmentDescription(
  evidence: PRDevelopmentLocalDevelopment,
): string {
  if (evidence.local_ready) {
    return "Local CI and AI review passed for this exact commit."
  }
  if (evidence.review_outcome === "attention_required") {
    return "AI review needs your direction in the PR chat."
  }
  if (evidence.review_outcome === "changes_required") {
    return "AI review requested another local repair."
  }
  if (evidence.ci_status && evidence.ci_status !== "passed") {
    return "The recorded local checks are not green."
  }
  if (evidence.review_status === "pending") {
    return "The committed local candidate is waiting for AI review."
  }
  return repairStatusDescription(evidence.attempt_status)
}

function repairErrorLabel(
  code: NonNullable<PRDevelopmentRepairAttempt["error_code"]>,
): string {
  switch (code) {
    case "provider_changed":
      return "The pull request provider state changed before repair could continue."
    case "not_actionable":
      return "The captured feedback is no longer actionable."
    case "runtime_unavailable":
      return "The repair runtime is unavailable."
    case "workspace_unavailable":
      return "The pinned local workspace is unavailable."
    case "repair_failed":
      return "The AI repair could not be completed."
    case "recovery_required":
      return "Partial local edits may exist. Use an explicit instruction to inspect and preserve them before continuing."
    case "internal_error":
      return "The local repair stopped because of an internal error."
  }
}

function prDevelopmentAttentionErrorMessage(
  error: unknown,
  t: Translate,
): string {
  if (error instanceof PRDevelopmentAttentionAPIError && error.status === 409) {
    return t(
      "pages.reviews.development.attention_conflict",
      "This attention request changed. The latest state was loaded and your reply is preserved.",
    )
  }
  return t(
    "pages.reviews.development.attention_respond_error",
    "The reply could not be sent. The latest state was loaded and your text is preserved.",
  )
}

function useWideDevelopmentLayout(): boolean {
  const query = "(min-width: 1024px)"
  const [wide, setWide] = useState(() =>
    typeof window !== "undefined" && typeof window.matchMedia === "function"
      ? window.matchMedia(query).matches
      : false,
  )

  useEffect(() => {
    const media = window.matchMedia(query)
    const update = () => setWide(media.matches)
    update()
    media.addEventListener("change", update)
    return () => media.removeEventListener("change", update)
  }, [])

  return wide
}

type Translate = (
  key: string,
  fallback: string,
  options?: Record<string, unknown>,
) => string
