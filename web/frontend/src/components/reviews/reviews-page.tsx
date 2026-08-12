import {
  IconArrowLeft,
  IconCheck,
  IconExternalLink,
  IconInbox,
  IconMessage,
  IconRefresh,
  IconRestore,
  IconSend,
  IconSparkles,
  IconTrash,
} from "@tabler/icons-react"
import {
  useInfiniteQuery,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"
import { useBlocker } from "@tanstack/react-router"
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
  REVIEW_ATTENTION_RESPONSE_MAXIMUM_BYTES,
  ReviewAttentionAPIError,
  getReviewAttention,
  respondToReviewAttention,
} from "@/api/review-attention"
import { trimGoSpace } from "@/api/review-attention-json"
import {
  ReviewAPIError,
  type ReviewCase,
  type ReviewCaseDetail,
  type ReviewCasePage,
  type ReviewCaseStatus,
  type ReviewFinding,
  type ReviewFindingDraft,
  type ReviewRephraseResult,
  type ReviewSeverity,
  chatAboutReview,
  dropReviewFinding,
  getReview,
  listReviews,
  reconcileReview,
  rephraseReviewFinding,
  restoreReviewFinding,
  submitReview,
  updateReviewFinding,
} from "@/api/reviews"
import { PageHeader } from "@/components/page-header"
import { AttentionConversation } from "@/components/reviews/attention-conversation"
import {
  attentionProjectionContainsResponse,
  attentionProjectionIsVisible,
  attentionProjectionPollInterval,
  findActionableAttentionTurn,
} from "@/components/reviews/attention-conversation-model"
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
import { cn } from "@/lib/utils"

const REVIEW_PAGE_SIZE = 40

export interface ReviewsRouteSearch {
  view?: "review" | "development" | "policies"
  case?: string
  focus?: "chat"
  status?: ReviewCaseStatus
  repository?: string
  pull_number?: number
  repo?: string
  pr?: number
  filter?: string
  role?: "review" | "develop"
  review_case?: string
}

export function ReviewsPage({
  search,
  onSearchChange,
}: {
  search: ReviewsRouteSearch
  onSearchChange: (search: ReviewsRouteSearch, replace?: boolean) => void
}) {
  if (search.view === "review") {
    return (
      <StandaloneReviewPage search={search} onSearchChange={onSearchChange} />
    )
  }
  return (
    <ReviewCaseWorkbenchPage search={search} onSearchChange={onSearchChange} />
  )
}

function StandaloneReviewPage({
  search,
  onSearchChange,
}: {
  search: ReviewsRouteSearch
  onSearchChange: (search: ReviewsRouteSearch, replace?: boolean) => void
}) {
  const { t } = useTranslation()
  const [dirtyFindingIDs, setDirtyFindingIDs] = useState<Set<string>>(
    () => new Set(),
  )
  const [discardNavigationOpen, setDiscardNavigationOpen] = useState(false)
  const proceedingNavigationRef = useRef(false)
  const dirty = dirtyFindingIDs.size > 0
  const shouldBlockNavigation = useCallback(() => dirty, [dirty])
  const navigationBlocker = useBlocker({
    shouldBlockFn: shouldBlockNavigation,
    enableBeforeUnload: shouldBlockNavigation,
    disabled: !dirty,
    withResolver: true,
  })
  useEffect(() => {
    if (navigationBlocker.status !== "blocked") return
    setDiscardNavigationOpen(true)
  }, [navigationBlocker])
  const trackFindingDirty = useCallback((findingID: string, value: boolean) => {
    setDirtyFindingIDs((current) => {
      const next = new Set(current)
      if (value) next.add(findingID)
      else next.delete(findingID)
      return next
    })
  }, [])
  const changeDiscardNavigationOpen = (open: boolean) => {
    if (!open && navigationBlocker.status === "blocked") {
      if (!proceedingNavigationRef.current) navigationBlocker.reset()
      proceedingNavigationRef.current = false
    }
    setDiscardNavigationOpen(open)
  }
  const discardAndNavigate = () => {
    if (navigationBlocker.status !== "blocked") return
    proceedingNavigationRef.current = true
    setDirtyFindingIDs(new Set())
    navigationBlocker.proceed()
    setDiscardNavigationOpen(false)
  }
  const backSearch =
    search.repo && search.pr
      ? {
          repo: search.repo,
          pr: search.pr,
          ...(search.filter ? { filter: search.filter } : {}),
          ...(search.role ? { role: search.role } : {}),
          ...(search.case ? { review_case: search.case } : {}),
        }
      : search.repo
        ? {
            repo: search.repo,
            ...(search.filter ? { filter: search.filter } : {}),
            ...(search.role ? { role: search.role } : {}),
          }
        : {}
  return (
    <div className="bg-background flex h-full min-h-0 flex-col">
      <PageHeader title={t("pages.reviews.title", "Pull request reviews")}>
        <Button
          type="button"
          variant="outline"
          onClick={() => onSearchChange(backSearch, true)}
        >
          <IconArrowLeft />
          {t("pages.reviews.detail.all_work", "All pull request work")}
        </Button>
      </PageHeader>
      <div className="min-h-0 flex-1 overflow-auto p-3 lg:p-4">
        <ReviewDetailPanel
          caseID={search.case}
          focusChat={search.focus === "chat"}
          hiddenOnMobile={false}
          onFindingDirtyChange={trackFindingDirty}
          onBack={() => onSearchChange(backSearch, true)}
        />
      </div>
      <AlertDialog
        open={discardNavigationOpen}
        onOpenChange={changeDiscardNavigationOpen}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Discard unsaved review edits?</AlertDialogTitle>
            <AlertDialogDescription>
              Unsaved finding text will be lost if you leave this editor.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep editing</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={discardAndNavigate}
            >
              Discard changes
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function ReviewCaseWorkbenchPage({
  search,
  onSearchChange,
}: {
  search: ReviewsRouteSearch
  onSearchChange: (search: ReviewsRouteSearch, replace?: boolean) => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [repositoryDraft, setRepositoryDraft] = useState(
    search.repository ?? "",
  )
  const listFilters = useMemo(
    () => ({
      ...(search.status ? { status: search.status } : {}),
      ...(search.repository ? { repository: search.repository } : {}),
      limit: REVIEW_PAGE_SIZE,
    }),
    [search.repository, search.status],
  )
  const casesQuery = useInfiniteQuery({
    queryKey: ["reviews", "list", listFilters],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      listReviews({
        ...listFilters,
        cursor: pageParam || undefined,
      }),
    getNextPageParam: (lastPage: ReviewCasePage) =>
      lastPage.next_cursor || undefined,
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
    if (!search.case && cases.length > 0) {
      onSearchChange({ ...search, case: cases[0].id }, true)
    }
  }, [cases, onSearchChange, search])

  const refresh = async () => {
    await queryClient.invalidateQueries({ queryKey: ["reviews"] })
  }

  const applyRepository = (event: FormEvent) => {
    event.preventDefault()
    const repository = repositoryDraft.trim()
    const next = reviewSearchWithoutFocus(search)
    onSearchChange(
      {
        ...next,
        ...(repository ? { repository } : {}),
        case: undefined,
      },
      true,
    )
  }

  return (
    <div className="bg-background flex h-full min-h-0 flex-col">
      <PageHeader
        title={t("pages.reviews.title", "Pull request reviews")}
        titleExtra={
          <Badge variant="secondary" className="hidden sm:inline-flex">
            {t("pages.reviews.human_gate", "Human approval required")}
          </Badge>
        }
      >
        <Button
          type="button"
          variant="outline"
          size="icon"
          aria-label={t("pages.reviews.refresh", "Refresh reviews")}
          title={t("pages.reviews.refresh", "Refresh reviews")}
          onClick={() => void refresh()}
          disabled={casesQuery.isFetching}
        >
          <IconRefresh className="size-4" />
        </Button>
      </PageHeader>

      <ReviewWorkbenchTabs
        active="inbox"
        onChange={(view) => {
          if (view === "development") {
            onSearchChange({ view: "development" })
          } else if (view === "policies") {
            onSearchChange({ view: "policies" })
          }
        }}
      />

      <div className="flex min-h-0 flex-1 flex-col">
        <div className="border-border flex shrink-0 flex-col gap-2 border-b px-3 py-2 sm:flex-row sm:items-center sm:px-4">
          <Label htmlFor="review-status-filter" className="sr-only">
            {t("pages.reviews.filters.status", "Status")}
          </Label>
          <select
            id="review-status-filter"
            value={search.status ?? ""}
            onChange={(event) =>
              onSearchChange(
                {
                  ...reviewSearchWithoutFocus(search),
                  status:
                    (event.target.value as ReviewCaseStatus | "") || undefined,
                  case: undefined,
                },
                true,
              )
            }
            className="border-input bg-background focus-visible:border-ring focus-visible:ring-ring/25 h-9 rounded-lg border px-3 text-sm outline-none focus-visible:ring-2"
          >
            <option value="">
              {t("pages.reviews.filters.all", "All states")}
            </option>
            {reviewCaseStatuses.map((status) => (
              <option key={status} value={status}>
                {reviewStatusLabel(status, t)}
              </option>
            ))}
          </select>
          <form
            className="flex min-w-0 flex-1 gap-2"
            onSubmit={applyRepository}
          >
            <Label htmlFor="review-repository-filter" className="sr-only">
              {t("pages.reviews.filters.repository", "Repository")}
            </Label>
            <Input
              id="review-repository-filter"
              value={repositoryDraft}
              maxLength={512}
              placeholder={t(
                "pages.reviews.filters.repository_placeholder",
                "owner/repository",
              )}
              onChange={(event) => setRepositoryDraft(event.target.value)}
              className="max-w-md"
            />
            <Button type="submit" variant="outline">
              {t("pages.reviews.filters.apply", "Apply")}
            </Button>
            {(search.status || search.repository) && (
              <Button
                type="button"
                variant="ghost"
                onClick={() => {
                  setRepositoryDraft("")
                  onSearchChange({}, true)
                }}
              >
                {t("pages.reviews.filters.reset", "Reset")}
              </Button>
            )}
          </form>
        </div>

        <div className="min-h-0 flex-1 overflow-auto p-3 lg:overflow-hidden lg:p-4">
          <div className="flex min-h-full min-w-0 flex-col gap-3 lg:grid lg:h-full lg:min-h-0 lg:grid-cols-[minmax(300px,0.72fr)_minmax(0,1.55fr)]">
            <ReviewCaseList
              cases={cases}
              selectedCaseID={search.case}
              hiddenOnMobile={Boolean(search.case)}
              loading={casesQuery.isPending}
              error={casesQuery.error}
              hasMore={Boolean(casesQuery.hasNextPage)}
              loadingMore={casesQuery.isFetchingNextPage}
              onSelect={(caseID) =>
                onSearchChange(
                  { ...reviewSearchWithoutFocus(search), case: caseID },
                  false,
                )
              }
              onRetry={() => {
                if (casesQuery.isFetchNextPageError) {
                  void casesQuery.fetchNextPage()
                } else {
                  void casesQuery.refetch()
                }
              }}
              onLoadMore={() => void casesQuery.fetchNextPage()}
            />
            <ReviewDetailPanel
              caseID={search.case}
              focusChat={search.focus === "chat"}
              hiddenOnMobile={!search.case}
              onBack={() => {
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

function ReviewCaseList({
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
  cases: ReviewCase[]
  selectedCaseID?: string
  hiddenOnMobile: boolean
  loading: boolean
  error: unknown
  hasMore: boolean
  loadingMore: boolean
  onSelect: (caseID: string) => void
  onRetry: () => void
  onLoadMore: () => void
}) {
  const { t } = useTranslation()
  const loadLockedRef = useRef(false)

  useEffect(() => {
    if (!loadingMore) {
      loadLockedRef.current = false
    }
  }, [loadingMore])

  const loadMore = () => {
    if (!hasMore || loadingMore || loadLockedRef.current) {
      return
    }
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
          <IconInbox className="text-muted-foreground size-4" />
          <h2 className="text-sm font-medium">
            {t("pages.reviews.list.title", "Review inbox")}
          </h2>
        </div>
        <Badge variant="outline" className="font-mono">
          {cases.length}
        </Badge>
      </div>
      <div
        role="region"
        aria-label={t("pages.reviews.list.region", "Review cases")}
        className="min-h-0 flex-1 overflow-auto p-2"
        onScroll={handleScroll}
      >
        {loading ? (
          <ReviewMessageBox>
            {t("pages.reviews.list.loading", "Loading reviews…")}
          </ReviewMessageBox>
        ) : error && cases.length === 0 ? (
          <ReviewError error={error} onRetry={onRetry} />
        ) : cases.length === 0 ? (
          <ReviewMessageBox>
            {t(
              "pages.reviews.list.empty",
              "No review cases match these filters.",
            )}
          </ReviewMessageBox>
        ) : (
          <div className="flex min-w-0 flex-col gap-1.5">
            {cases.map((reviewCase) => {
              const selected = reviewCase.id === selectedCaseID
              return (
                <button
                  type="button"
                  key={reviewCase.id}
                  aria-current={selected ? "true" : undefined}
                  onClick={() => onSelect(reviewCase.id)}
                  className={cn(
                    "border-border/70 hover:bg-muted/60 focus-visible:border-ring focus-visible:ring-ring/30 grid min-w-0 gap-1.5 rounded-md border px-3 py-2 text-left outline-none focus-visible:ring-2",
                    selected && "bg-accent/70 text-accent-foreground",
                  )}
                >
                  <div className="flex min-w-0 items-center justify-between gap-2">
                    <span className="min-w-0 truncate text-sm font-medium">
                      {reviewCase.repository} #{reviewCase.pull_number}
                    </span>
                    <ReviewStatusBadge status={reviewCase.status} />
                  </div>
                  <p className="text-muted-foreground line-clamp-2 text-xs">
                    {reviewCase.summary}
                  </p>
                  <div className="text-muted-foreground flex items-center justify-between gap-2 text-[11px]">
                    <span>
                      {t(
                        "pages.reviews.list.finding_count",
                        "{{active}} of {{total}} findings active",
                        {
                          active: reviewCase.active_findings,
                          total: reviewCase.total_findings,
                        },
                      )}
                    </span>
                    <time dateTime={reviewCase.updated_at}>
                      {formatDate(reviewCase.updated_at)}
                    </time>
                  </div>
                </button>
              )
            })}
            {error ? (
              <ReviewError compact error={error} onRetry={onRetry} />
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

function ReviewDetailPanel({
  caseID,
  focusChat,
  hiddenOnMobile,
  onFindingDirtyChange,
  onBack,
}: {
  caseID?: string
  focusChat: boolean
  hiddenOnMobile: boolean
  onFindingDirtyChange?: (findingID: string, dirty: boolean) => void
  onBack: () => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [busyAction, setBusyAction] = useState<string>()
  const [actionError, setActionError] = useState<string>()
  const [conflict, setConflict] = useState(false)
  const [submitOpen, setSubmitOpen] = useState(false)
  const [dirtyFindingIDs, setDirtyFindingIDs] = useState<Set<string>>(
    () => new Set(),
  )
  const [reconcileResolution, setReconcileResolution] = useState<
    "submitted" | "absent"
  >()

  const detailQuery = useQuery({
    queryKey: ["reviews", "detail", caseID],
    queryFn: () => getReview(caseID!),
    enabled: Boolean(caseID),
    staleTime: 1000,
    refetchInterval: (query) =>
      query.state.data?.case.status === "submitting" ? 1500 : false,
  })
  const detail = detailQuery.data

  useEffect(() => {
    setActionError(undefined)
    setConflict(false)
    setBusyAction(undefined)
    setSubmitOpen(false)
    setDirtyFindingIDs(new Set())
    setReconcileResolution(undefined)
  }, [caseID])

  const trackFindingDirty = useCallback(
    (findingID: string, dirty: boolean) => {
      setDirtyFindingIDs((current) => {
        const next = new Set(current)
        if (dirty) next.add(findingID)
        else next.delete(findingID)
        return next
      })
      onFindingDirtyChange?.(findingID, dirty)
    },
    [onFindingDirtyChange],
  )

  const acceptDetail = useCallback(
    (next: ReviewCaseDetail) => {
      queryClient.setQueryData(["reviews", "detail", next.case.id], next)
      void queryClient.invalidateQueries({ queryKey: ["reviews", "list"] })
      setActionError(undefined)
      setConflict(false)
      return next
    },
    [queryClient],
  )

  const runAction = useCallback(
    async <T,>(
      label: string,
      action: () => Promise<T>,
      detailFromResult: (result: T) => ReviewCaseDetail,
    ): Promise<T> => {
      if (busyAction) {
        throw new Error("Another review action is still running.")
      }
      setBusyAction(label)
      setActionError(undefined)
      setConflict(false)
      try {
        const result = await action()
        acceptDetail(detailFromResult(result))
        return result
      } catch (error) {
        const versionConflict =
          error instanceof ReviewAPIError && error.status === 409
        let reconciled = false
        if (versionConflict && error.detail) {
          acceptDetail(error.detail)
          reconciled = true
        } else {
          // A mutation may have reached the durable service even when its
          // response was lost. Keep actions locked until reconciliation
          // finishes so a second click cannot repeat an external transition.
          const refreshed = await detailQuery.refetch()
          if (refreshed.data) {
            acceptDetail(refreshed.data)
            reconciled = true
          }
        }
        void queryClient.invalidateQueries({ queryKey: ["reviews", "list"] })
        if (versionConflict) {
          setConflict(reconciled)
        }
        setActionError(reviewErrorMessage(error))
        throw error
      } finally {
        setBusyAction(undefined)
      }
    },
    [acceptDetail, busyAction, detailQuery, queryClient],
  )

  const saveFinding = useCallback(
    async (
      finding: ReviewFinding,
      draft: ReviewFindingDraft,
    ): Promise<ReviewFinding> => {
      if (!detail) {
        throw new Error("Review detail is unavailable.")
      }
      const next = await runAction(
        `save:${finding.id}`,
        () =>
          updateReviewFinding(
            detail.case.id,
            finding.id,
            detail.case.version,
            draft,
          ),
        (result) => result,
      )
      const saved = next.findings.find((item) => item.id === finding.id)
      if (!saved) {
        throw new Error("The saved finding is missing from the response.")
      }
      return saved
    },
    [detail, runAction],
  )

  const transitionFinding = useCallback(
    async (finding: ReviewFinding, restore: boolean) => {
      if (!detail) {
        return
      }
      await runAction(
        `${restore ? "restore" : "drop"}:${finding.id}`,
        () =>
          restore
            ? restoreReviewFinding(
                detail.case.id,
                finding.id,
                detail.case.version,
              )
            : dropReviewFinding(
                detail.case.id,
                finding.id,
                detail.case.version,
              ),
        (result) => result,
      )
    },
    [detail, runAction],
  )

  const rephraseFinding = useCallback(
    async (
      finding: ReviewFinding,
      instruction: string,
    ): Promise<ReviewRephraseResult> => {
      if (!detail) {
        throw new Error("Review detail is unavailable.")
      }
      return runAction(
        `rephrase:${finding.id}`,
        () =>
          rephraseReviewFinding(
            detail.case.id,
            finding.id,
            detail.case.version,
            instruction,
          ),
        (result) => result.detail,
      )
    },
    [detail, runAction],
  )

  const sendChat = useCallback(
    async (content: string, findingID?: string) => {
      if (!detail) {
        return
      }
      await runAction(
        "chat",
        () =>
          chatAboutReview(
            detail.case.id,
            detail.case.version,
            content,
            findingID,
          ),
        (result) => result,
      )
    },
    [detail, runAction],
  )

  const confirmSubmit = async () => {
    if (!detail || dirtyFindingIDs.size > 0) {
      return
    }
    try {
      await runAction(
        "submit",
        () => submitReview(detail.case.id, detail.case.version),
        (result) => result,
      )
      setSubmitOpen(false)
    } catch {
      // The durable error is shown in the detail panel.
    }
  }

  const confirmReconcile = async () => {
    if (!detail || !reconcileResolution) {
      return
    }
    const resolution = reconcileResolution
    try {
      await runAction(
        `reconcile:${resolution}`,
        () => reconcileReview(detail.case.id, detail.case.version, resolution),
        (result) => result,
      )
      setReconcileResolution(undefined)
    } catch {
      // The durable error is shown in the detail panel.
    }
  }

  const caseEditable =
    detail?.case.status === "open" || detail?.case.status === "all_dropped"
  const submitAllowed =
    detail?.case.status === "open" &&
    detail.case.active_findings > 0 &&
    dirtyFindingIDs.size === 0 &&
    !busyAction

  return (
    <section
      className={cn(
        "border-border bg-card/40 min-h-[32rem] min-w-0 flex-col overflow-hidden rounded-lg border lg:flex lg:min-h-0",
        hiddenOnMobile ? "hidden" : "flex",
      )}
    >
      <div className="border-border flex min-h-12 shrink-0 items-center justify-between gap-3 border-b px-3 py-2">
        <div className="flex min-w-0 items-center gap-2">
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            className="lg:hidden"
            onClick={onBack}
            aria-label={t("pages.reviews.detail.back", "Back to review inbox")}
          >
            <IconArrowLeft />
          </Button>
          <div className="min-w-0">
            <div className="flex min-w-0 items-center gap-2">
              <h2 className="min-w-0 truncate text-sm font-medium">
                {detail
                  ? `${detail.case.repository} #${detail.case.pull_number}`
                  : t("pages.reviews.detail.title", "Review detail")}
              </h2>
              {detail ? (
                <ReviewStatusBadge status={detail.case.status} />
              ) : null}
            </div>
            <p className="text-muted-foreground mt-0.5 truncate font-mono text-[11px]">
              {caseID ??
                t("pages.reviews.detail.select_prompt", "Select a review")}
            </p>
          </div>
        </div>
        {detail ? (
          <div className="flex shrink-0 items-center gap-2">
            <Button type="button" variant="outline" size="sm" asChild>
              <a
                href={detail.case.pull_url}
                target="_blank"
                rel="noreferrer"
                aria-label={t("pages.reviews.detail.open_pr", "Open PR")}
              >
                <IconExternalLink />
                <span className="hidden sm:inline">
                  {t("pages.reviews.detail.open_pr", "Open PR")}
                </span>
              </a>
            </Button>
            <Button
              type="button"
              size="sm"
              disabled={!submitAllowed}
              onClick={() => setSubmitOpen(true)}
            >
              <IconSend />
              {busyAction === "submit"
                ? t("pages.reviews.submit.starting", "Starting…")
                : t("pages.reviews.submit.action", "Submit review")}
            </Button>
          </div>
        ) : null}
      </div>

      <div
        role="region"
        aria-label={t("pages.reviews.detail.region", "Review detail")}
        className="min-h-0 flex-1 overflow-auto p-3"
      >
        {!caseID ? (
          <ReviewMessageBox>
            {t(
              "pages.reviews.detail.select_prompt",
              "Select a review case to inspect it.",
            )}
          </ReviewMessageBox>
        ) : detailQuery.isPending ? (
          <ReviewMessageBox>
            {t("pages.reviews.detail.loading", "Loading review detail…")}
          </ReviewMessageBox>
        ) : detailQuery.error || !detail ? (
          <ReviewError
            error={detailQuery.error}
            onRetry={() => void detailQuery.refetch()}
          />
        ) : (
          <div className="grid min-w-0 gap-3">
            {conflict ? (
              <div
                role="alert"
                className="border-border bg-muted/50 rounded-lg border px-3 py-2 text-sm"
              >
                <p className="font-medium">
                  {t(
                    "pages.reviews.conflict.title",
                    "This review changed elsewhere.",
                  )}
                </p>
                <p className="text-muted-foreground mt-1">
                  {t(
                    "pages.reviews.conflict.description",
                    "The latest version was loaded. Unsaved finding text remains in the editor so you can review it and save again.",
                  )}
                </p>
              </div>
            ) : null}
            {actionError ? (
              <div
                role="alert"
                className="bg-destructive/10 text-destructive rounded-lg px-3 py-2 text-sm break-words"
              >
                {actionError}
              </div>
            ) : null}
            <CaseOverview detail={detail} />
            {detail.case.status === "submitting" ? (
              <StatusNotice
                title={t(
                  "pages.reviews.status.submitting_title",
                  "Submission in progress",
                )}
                body={t(
                  "pages.reviews.status.submitting_body",
                  "This case is locked while PicoClaw posts the review. Status refreshes automatically.",
                )}
              />
            ) : null}
            {detail.case.status === "submission_unknown" ? (
              <div className="grid gap-2">
                <StatusNotice
                  title={t(
                    "pages.reviews.status.unknown_title",
                    "Submission outcome is unknown",
                  )}
                  body={t(
                    "pages.reviews.status.unknown_body",
                    "The request may have reached GitHub. Inspect the pull request, then record what you found.",
                  )}
                  destructive
                />
                <div className="flex flex-col gap-2 sm:flex-row">
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    disabled={Boolean(busyAction)}
                    onClick={() => setReconcileResolution("submitted")}
                  >
                    <IconCheck />
                    {t(
                      "pages.reviews.reconcile.found",
                      "I found the submitted review",
                    )}
                  </Button>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    disabled={Boolean(busyAction)}
                    onClick={() => setReconcileResolution("absent")}
                  >
                    <IconRestore />
                    {t(
                      "pages.reviews.reconcile.absent",
                      "I confirmed no review was posted",
                    )}
                  </Button>
                </div>
              </div>
            ) : null}
            {detail.case.status === "stale" ? (
              <StatusNotice
                title={t("pages.reviews.status.stale_title", "Review is stale")}
                body={t(
                  "pages.reviews.status.stale_body",
                  "The pull request head changed, so this captured review can no longer be submitted.",
                )}
                destructive
              />
            ) : null}
            {detail.case.status === "all_dropped" ? (
              <StatusNotice
                title={t(
                  "pages.reviews.status.all_dropped_title",
                  "All findings are dropped",
                )}
                body={t(
                  "pages.reviews.status.all_dropped_body",
                  "Nothing will be sent to GitHub. Restore a finding if the review should be submitted.",
                )}
              />
            ) : null}

            <section aria-labelledby="review-findings-heading">
              <div className="mb-2 flex items-center justify-between gap-2">
                <h3
                  id="review-findings-heading"
                  className="text-sm font-medium"
                >
                  {t("pages.reviews.findings.title", "Findings")}
                </h3>
                <Badge variant="outline">
                  {detail.case.active_findings}/{detail.case.total_findings}
                </Badge>
              </div>
              {detail.findings.length === 0 ? (
                <div className="border-border text-muted-foreground rounded-lg border border-dashed p-4 text-sm">
                  {t(
                    "pages.reviews.findings.empty",
                    "The workflow found no review findings.",
                  )}
                </div>
              ) : (
                <div className="grid gap-3">
                  {detail.findings.map((finding) => (
                    <FindingCard
                      key={finding.id}
                      finding={finding}
                      caseEditable={caseEditable}
                      locked={Boolean(busyAction)}
                      busyAction={busyAction}
                      onDirtyChange={trackFindingDirty}
                      onSave={saveFinding}
                      onTransition={transitionFinding}
                      onRephrase={rephraseFinding}
                    />
                  ))}
                </div>
              )}
            </section>
            <ReviewConversation
              key={detail.case.id}
              detail={detail}
              focusRequested={focusChat}
              editable={caseEditable}
              pending={busyAction === "chat"}
              locked={Boolean(busyAction)}
              onSend={sendChat}
            />
          </div>
        )}
      </div>

      <AlertDialog open={submitOpen} onOpenChange={setSubmitOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(
                "pages.reviews.submit.confirm_title",
                "Submit this review to GitHub?",
              )}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(
                "pages.reviews.submit.confirm_description",
                "PicoClaw will create a pending review, add the active inline findings, and then submit it as a comment. This action can affect the pull request.",
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busyAction === "submit"}>
              {t("pages.reviews.submit.cancel", "Cancel")}
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={!submitAllowed}
              onClick={(event) => {
                event.preventDefault()
                void confirmSubmit()
              }}
            >
              {busyAction === "submit"
                ? t("pages.reviews.submit.starting", "Starting…")
                : t("pages.reviews.submit.confirm", "Submit to GitHub")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <AlertDialog
        open={Boolean(reconcileResolution)}
        onOpenChange={(open) => {
          if (!open) {
            setReconcileResolution(undefined)
          }
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {reconcileResolution === "submitted"
                ? t(
                    "pages.reviews.reconcile.submitted_title",
                    "Confirm the review exists on GitHub?",
                  )
                : t(
                    "pages.reviews.reconcile.absent_title",
                    "Confirm no review was posted?",
                  )}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {reconcileResolution === "submitted"
                ? t(
                    "pages.reviews.reconcile.submitted_description",
                    "This records the case as submitted without making another GitHub call. Use it only after verifying the review on the pull request.",
                  )
                : t(
                    "pages.reviews.reconcile.absent_description",
                    "This reopens the case without making a GitHub call. Verify the review is absent first, or a later submission could duplicate it.",
                  )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busyAction?.startsWith("reconcile:")}>
              {t("pages.reviews.reconcile.cancel", "Cancel")}
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={
                !reconcileResolution ||
                Boolean(busyAction) ||
                detail?.case.status !== "submission_unknown"
              }
              onClick={(event) => {
                event.preventDefault()
                void confirmReconcile()
              }}
            >
              {busyAction?.startsWith("reconcile:")
                ? t("pages.reviews.reconcile.recording", "Recording…")
                : reconcileResolution === "submitted"
                  ? t(
                      "pages.reviews.reconcile.confirm_submitted",
                      "Mark as submitted",
                    )
                  : t(
                      "pages.reviews.reconcile.confirm_absent",
                      "Reopen review",
                    )}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}

function CaseOverview({ detail }: { detail: ReviewCaseDetail }) {
  const { t } = useTranslation()
  const reviewCase = detail.case
  return (
    <section className="border-border rounded-lg border p-3">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <h3 className="text-sm font-medium">
            {t("pages.reviews.overview.summary", "Review summary")}
          </h3>
          <p className="mt-2 text-sm whitespace-pre-wrap">
            {reviewCase.summary}
          </p>
        </div>
        <div className="text-muted-foreground shrink-0 text-right font-mono text-[11px]">
          <p title={reviewCase.base_sha}>
            base {shortSHA(reviewCase.base_sha)}
          </p>
          <p title={reviewCase.head_sha}>
            head {shortSHA(reviewCase.head_sha)}
          </p>
        </div>
      </div>
      {reviewCase.tests.length > 0 || reviewCase.residual_risks.length > 0 ? (
        <div className="mt-3 grid gap-3 border-t pt-3 md:grid-cols-2">
          <StringList
            title={t("pages.reviews.overview.tests", "Tests considered")}
            items={reviewCase.tests}
            empty={t("pages.reviews.overview.none", "None recorded")}
          />
          <StringList
            title={t("pages.reviews.overview.risks", "Residual risks")}
            items={reviewCase.residual_risks}
            empty={t("pages.reviews.overview.none", "None recorded")}
          />
        </div>
      ) : null}
      {detail.submission ? (
        <div className="border-border text-muted-foreground mt-3 flex flex-wrap items-center gap-2 border-t pt-3 text-xs">
          <span>{t("pages.reviews.overview.submission", "Submission")}:</span>
          <Badge variant="outline">{detail.submission.status}</Badge>
          <span>
            {t("pages.reviews.overview.attempts", "Attempts")}:{" "}
            {detail.submission.attempts}
          </span>
          {detail.submission.external_url ? (
            <a
              className="text-foreground underline underline-offset-2"
              href={detail.submission.external_url}
              target="_blank"
              rel="noreferrer"
            >
              {t("pages.reviews.overview.open_submission", "Open result")}
            </a>
          ) : null}
        </div>
      ) : null}
      {reviewCase.public_error_code || detail.submission?.public_error_code ? (
        <p className="bg-destructive/10 text-destructive mt-3 rounded-md px-3 py-2 font-mono text-xs break-words">
          {t("pages.reviews.overview.error_code", "Error code")}:{" "}
          {reviewCase.public_error_code ?? detail.submission?.public_error_code}
        </p>
      ) : null}
    </section>
  )
}

function FindingCard({
  finding,
  caseEditable,
  locked,
  busyAction,
  onDirtyChange,
  onSave,
  onTransition,
  onRephrase,
}: {
  finding: ReviewFinding
  caseEditable: boolean
  locked: boolean
  busyAction?: string
  onDirtyChange?: (findingID: string, dirty: boolean) => void
  onSave: (
    finding: ReviewFinding,
    draft: ReviewFindingDraft,
  ) => Promise<ReviewFinding>
  onTransition: (finding: ReviewFinding, restore: boolean) => Promise<void>
  onRephrase: (
    finding: ReviewFinding,
    instruction: string,
  ) => Promise<ReviewRephraseResult>
}) {
  const { t } = useTranslation()
  const [draft, setDraft] = useState(() => findingDraft(finding))
  const [dirty, setDirty] = useState(false)
  const [localError, setLocalError] = useState<string>()
  const [instruction, setInstruction] = useState("")
  const [suggestion, setSuggestion] =
    useState<ReviewRephraseResult["suggestion"]>()
  const revisionRef = useRef(finding.revision)

  useEffect(() => {
    if (finding.revision !== revisionRef.current) {
      revisionRef.current = finding.revision
      setSuggestion(undefined)
      if (!dirty) {
        setDraft(findingDraft(finding))
      }
    }
  }, [dirty, finding])

  useEffect(() => {
    onDirtyChange?.(finding.id, dirty)
    return () => onDirtyChange?.(finding.id, false)
  }, [dirty, finding.id, onDirtyChange])

  const updateDraft = (patch: Partial<ReviewFindingDraft>) => {
    setDraft((current) => ({ ...current, ...patch }))
    setDirty(true)
    setLocalError(undefined)
  }

  const saveDraft = async (nextDraft = draft) => {
    setLocalError(undefined)
    try {
      const saved = await onSave(finding, normalizedDraft(nextDraft))
      setDraft(findingDraft(saved))
      revisionRef.current = saved.revision
      setDirty(false)
      return saved
    } catch (error) {
      setLocalError(reviewErrorMessage(error))
      throw error
    }
  }

  const previewRephrase = async () => {
    const normalizedInstruction = instruction.trim()
    if (!normalizedInstruction) {
      setLocalError(
        t(
          "pages.reviews.rephrase.instruction_required",
          "Enter an instruction for the rephrase.",
        ),
      )
      return
    }
    setLocalError(undefined)
    try {
      const result = await onRephrase(finding, normalizedInstruction)
      setSuggestion(result.suggestion)
    } catch (error) {
      setLocalError(reviewErrorMessage(error))
    }
  }

  const applySuggestion = async () => {
    if (!suggestion) {
      return
    }
    const next = {
      ...draft,
      title: suggestion.title,
      message: suggestion.message,
    }
    setDraft(next)
    setDirty(true)
    try {
      await saveDraft(next)
      setSuggestion(undefined)
      setInstruction("")
    } catch {
      // Preserve the suggested text in the editor for conflict recovery.
    }
  }

  const active = finding.state === "active"
  const editorEnabled = active && caseEditable && !locked
  const transitionPending =
    busyAction === `drop:${finding.id}` ||
    busyAction === `restore:${finding.id}`

  return (
    <article
      className={cn(
        "border-border rounded-lg border p-3",
        !active && "bg-muted/30",
      )}
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <SeverityBadge severity={finding.severity} />
          <span className="text-muted-foreground font-mono text-[11px]">
            {finding.file
              ? `${finding.file}${finding.line ? `:${finding.line}` : ""}`
              : t("pages.reviews.findings.general", "General")}
          </span>
        </div>
        {active ? (
          <Button
            type="button"
            size="sm"
            variant="ghost"
            disabled={!caseEditable || locked || dirty}
            onClick={() =>
              void onTransition(finding, false).catch(() => undefined)
            }
          >
            <IconTrash />
            {transitionPending
              ? t("pages.reviews.findings.dropping", "Dropping…")
              : t("pages.reviews.findings.drop", "Drop")}
          </Button>
        ) : (
          <Button
            type="button"
            size="sm"
            variant="outline"
            disabled={!caseEditable || locked}
            onClick={() =>
              void onTransition(finding, true).catch(() => undefined)
            }
          >
            <IconRestore />
            {transitionPending
              ? t("pages.reviews.findings.restoring", "Restoring…")
              : t("pages.reviews.findings.restore", "Restore")}
          </Button>
        )}
      </div>

      {!active ? (
        <div className="mt-3">
          <h4 className="font-medium">{finding.title}</h4>
          <p className="text-muted-foreground mt-1 text-sm whitespace-pre-wrap">
            {finding.message}
          </p>
          {finding.dropped_reason ? (
            <p className="text-muted-foreground mt-2 text-xs">
              {t("pages.reviews.findings.drop_reason", "Reason")}:{" "}
              {finding.dropped_reason}
            </p>
          ) : null}
        </div>
      ) : (
        <div className="mt-3 grid gap-3">
          <div className="grid gap-3 sm:grid-cols-[10rem_minmax(0,1fr)]">
            <Field label={t("pages.reviews.findings.severity", "Severity")}>
              <select
                value={draft.severity}
                disabled={!editorEnabled}
                onChange={(event) =>
                  updateDraft({
                    severity: event.target.value as ReviewSeverity,
                  })
                }
                className="border-input bg-background focus-visible:border-ring focus-visible:ring-ring/25 h-9 w-full rounded-lg border px-3 text-sm outline-none focus-visible:ring-2 disabled:opacity-50"
              >
                {reviewSeverities.map((severity) => (
                  <option key={severity} value={severity}>
                    {severityLabel(severity, t)}
                  </option>
                ))}
              </select>
            </Field>
            <Field label={t("pages.reviews.findings.title_label", "Title")}>
              <Input
                value={draft.title}
                disabled={!editorEnabled}
                maxLength={8192}
                onChange={(event) => updateDraft({ title: event.target.value })}
              />
            </Field>
          </div>
          <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_8rem]">
            <Field label={t("pages.reviews.findings.file", "File")}>
              <Input
                value={draft.file ?? ""}
                disabled={!editorEnabled}
                maxLength={4096}
                placeholder="src/example.go"
                onChange={(event) =>
                  updateDraft({
                    file: event.target.value || undefined,
                    ...(event.target.value ? {} : { line: undefined }),
                  })
                }
              />
            </Field>
            <Field label={t("pages.reviews.findings.line", "Line")}>
              <Input
                type="number"
                min={1}
                value={draft.line ?? ""}
                disabled={!editorEnabled || !draft.file}
                onChange={(event) =>
                  updateDraft({
                    line: event.target.value
                      ? Number(event.target.value)
                      : undefined,
                  })
                }
              />
            </Field>
          </div>
          <Field label={t("pages.reviews.findings.message", "Comment")}>
            <Textarea
              value={draft.message}
              disabled={!editorEnabled}
              maxLength={64 << 10}
              className="min-h-28"
              onChange={(event) => updateDraft({ message: event.target.value })}
            />
          </Field>
          <details className="border-border rounded-md border px-3 py-2">
            <summary className="cursor-pointer text-sm font-medium">
              {t(
                "pages.reviews.findings.supporting",
                "Supporting review fields",
              )}
            </summary>
            <div className="mt-3 grid gap-3">
              <OptionalFindingField
                label={t("pages.reviews.findings.evidence", "Evidence")}
                value={draft.evidence}
                disabled={!editorEnabled}
                onChange={(value) => updateDraft({ evidence: value })}
              />
              <OptionalFindingField
                label={t("pages.reviews.findings.impact", "Impact")}
                value={draft.impact}
                disabled={!editorEnabled}
                onChange={(value) => updateDraft({ impact: value })}
              />
              <OptionalFindingField
                label={t(
                  "pages.reviews.findings.recommendation",
                  "Recommendation",
                )}
                value={draft.recommendation}
                disabled={!editorEnabled}
                onChange={(value) => updateDraft({ recommendation: value })}
              />
              <OptionalFindingField
                label={t("pages.reviews.findings.validation", "Validation")}
                value={draft.validation}
                disabled={!editorEnabled}
                onChange={(value) => updateDraft({ validation: value })}
              />
            </div>
          </details>

          {localError ? (
            <p role="alert" className="text-destructive text-sm">
              {localError}
            </p>
          ) : null}
          <div className="flex flex-wrap justify-end gap-2">
            <Button
              type="button"
              variant="ghost"
              disabled={!dirty || !editorEnabled}
              onClick={() => {
                setDraft(findingDraft(finding))
                setDirty(false)
                setLocalError(undefined)
              }}
            >
              {t("pages.reviews.findings.discard_edits", "Discard edits")}
            </Button>
            <Button
              type="button"
              variant="outline"
              disabled={!dirty || !editorEnabled}
              onClick={() => void saveDraft().catch(() => undefined)}
            >
              <IconCheck />
              {busyAction === `save:${finding.id}`
                ? t("pages.reviews.findings.saving", "Saving…")
                : t("pages.reviews.findings.save", "Save finding")}
            </Button>
          </div>

          <div className="border-border bg-muted/20 rounded-md border p-3">
            <div className="flex items-center gap-2">
              <IconSparkles className="text-muted-foreground size-4" />
              <h5 className="text-sm font-medium">
                {t("pages.reviews.rephrase.title", "Rephrase with AI")}
              </h5>
            </div>
            <Textarea
              value={instruction}
              disabled={!editorEnabled}
              maxLength={64 << 10}
              className="mt-2 min-h-16"
              placeholder={t(
                "pages.reviews.rephrase.placeholder",
                "For example: make this concise and constructive",
              )}
              onChange={(event) => setInstruction(event.target.value)}
            />
            <div className="mt-2 flex justify-end">
              <Button
                type="button"
                variant="secondary"
                size="sm"
                disabled={!editorEnabled || dirty || instruction.trim() === ""}
                onClick={() => void previewRephrase()}
              >
                <IconSparkles />
                {busyAction === `rephrase:${finding.id}`
                  ? t("pages.reviews.rephrase.generating", "Generating…")
                  : t("pages.reviews.rephrase.preview", "Preview rephrase")}
              </Button>
            </div>
            {suggestion ? (
              <div className="border-border bg-background mt-3 rounded-md border p-3">
                <p className="text-muted-foreground text-xs font-medium">
                  {t("pages.reviews.rephrase.preview_title", "Preview")}
                </p>
                <p className="mt-1 text-sm font-medium">{suggestion.title}</p>
                <p className="mt-2 text-sm whitespace-pre-wrap">
                  {suggestion.message}
                </p>
                <div className="mt-3 flex justify-end gap-2">
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    disabled={locked}
                    onClick={() => setSuggestion(undefined)}
                  >
                    {t("pages.reviews.rephrase.dismiss", "Dismiss")}
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    disabled={locked || dirty}
                    onClick={() => void applySuggestion()}
                  >
                    {t(
                      "pages.reviews.rephrase.apply",
                      "Apply and save suggestion",
                    )}
                  </Button>
                </div>
              </div>
            ) : null}
          </div>
        </div>
      )}
    </article>
  )
}

function ReviewConversation({
  detail,
  focusRequested,
  editable,
  pending,
  locked,
  onSend,
}: {
  detail: ReviewCaseDetail
  focusRequested: boolean
  editable: boolean
  pending: boolean
  locked: boolean
  onSend: (content: string, findingID?: string) => Promise<void>
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [content, setContent] = useState("")
  const [findingID, setFindingID] = useState("")
  const [error, setError] = useState<string>()
  const [attentionResponse, setAttentionResponse] = useState("")
  const [attentionError, setAttentionError] = useState<string>()
  const [attentionPending, setAttentionPending] = useState(false)
  const sectionRef = useRef<HTMLElement>(null)
  const headingRef = useRef<HTMLHeadingElement>(null)
  const chatInputRef = useRef<HTMLTextAreaElement>(null)
  const attentionInputRef = useRef<HTMLTextAreaElement>(null)
  const recoveryButtonRef = useRef<HTMLButtonElement>(null)
  const focusedTargetsRef = useRef(new Set<string>())
  const activeFindings = detail.findings.filter(
    (finding) => finding.state === "active",
  )
  const attentionEnabled = detail.case.status === "submitted"
  const attentionQuery = useQuery({
    queryKey: ["reviews", "attention", detail.case.id],
    queryFn: ({ signal }) => getReviewAttention(detail.case.id, signal),
    enabled: attentionEnabled,
    retry: false,
    refetchInterval: (query) =>
      attentionProjectionPollInterval(query.state.data),
  })
  const attention = attentionQuery.data
  const actionableTurn = findActionableAttentionTurn(attention)
  const showAttention =
    attentionEnabled &&
    (focusRequested ||
      attentionQuery.isPending ||
      Boolean(attentionQuery.error) ||
      attentionProjectionIsVisible(attention))

  useEffect(() => {
    if (!focusRequested) {
      return
    }
    const responseToken = actionableTurn?.response_token
    const focusKey = responseToken
      ? `${detail.case.id}:${responseToken}`
      : `${detail.case.id}:chat`
    if (focusedTargetsRef.current.has(focusKey)) {
      return
    }
    const target =
      actionableTurn?.status === "waiting" && attention?.can_respond
        ? attentionInputRef.current
        : actionableTurn?.status === "recovery_required" &&
            attention?.can_respond
          ? recoveryButtonRef.current
          : editable && !locked
            ? chatInputRef.current
            : headingRef.current
    if (!target || !sectionRef.current) {
      return
    }
    const frame = window.requestAnimationFrame(() => {
      if (!target.isConnected || !sectionRef.current) {
        return
      }
      sectionRef.current?.scrollIntoView({ block: "start" })
      target.focus({ preventScroll: true })
      focusedTargetsRef.current.add(focusKey)
    })
    return () => window.cancelAnimationFrame(frame)
  }, [
    actionableTurn,
    attention?.can_respond,
    detail.case.id,
    editable,
    focusRequested,
    locked,
  ])

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    const normalized = content.trim()
    if (!normalized) {
      return
    }
    setError(undefined)
    try {
      await onSend(normalized, findingID || undefined)
      setContent("")
    } catch (chatError) {
      setError(reviewErrorMessage(chatError))
    }
  }

  const respondToAttention = async (response: string) => {
    if (
      !attention?.can_respond ||
      !actionableTurn?.response_token ||
      attentionPending
    ) {
      return
    }
    const targetIndex = attention.turns.lastIndexOf(actionableTurn)
    setAttentionPending(true)
    setAttentionError(undefined)
    try {
      const next = await respondToReviewAttention(
        detail.case.id,
        attention.case_version,
        actionableTurn.response_token,
        response,
      )
      queryClient.setQueryData(["reviews", "attention", detail.case.id], next)
      setAttentionResponse("")
    } catch (attentionResponseError) {
      setAttentionError(reviewAttentionErrorMessage(attentionResponseError, t))
      const refreshed = await attentionQuery.refetch()
      if (
        !refreshed.isError &&
        targetIndex >= 0 &&
        attentionProjectionContainsResponse(
          refreshed.data,
          targetIndex,
          trimGoSpace(response),
          actionableTurn.status,
        )
      ) {
        setAttentionError(undefined)
        setAttentionResponse("")
      }
    } finally {
      setAttentionPending(false)
    }
  }

  const submitAttention = (event: FormEvent) => {
    event.preventDefault()
    const normalized = trimGoSpace(attentionResponse)
    if (
      normalized !== "" &&
      utf8ByteLength(normalized) <= REVIEW_ATTENTION_RESPONSE_MAXIMUM_BYTES
    ) {
      void respondToAttention(normalized)
    }
  }

  return (
    <section
      ref={sectionRef}
      aria-labelledby="review-conversation-heading"
      className="border-border scroll-mt-3 rounded-lg border p-3"
    >
      <div className="flex items-center gap-2">
        <IconMessage className="text-muted-foreground size-4" />
        <h3
          ref={headingRef}
          id="review-conversation-heading"
          tabIndex={-1}
          className="text-sm font-medium outline-none"
        >
          {t("pages.reviews.chat.title", "Review conversation")}
        </h3>
        <Badge variant="outline">{detail.messages.length}</Badge>
      </div>
      {showAttention ? (
        <AttentionConversation
          projection={attention}
          loading={attentionQuery.isPending}
          loadError={attentionQuery.error}
          actionError={attentionError}
          response={attentionResponse}
          pending={attentionPending}
          maximumResponseBytes={REVIEW_ATTENTION_RESPONSE_MAXIMUM_BYTES}
          attentionInputRef={attentionInputRef}
          recoveryButtonRef={recoveryButtonRef}
          onResponseChange={setAttentionResponse}
          onSubmit={submitAttention}
          onRetryLoad={() => void attentionQuery.refetch()}
          onRetryContinuation={() => {
            if (actionableTurn?.response) {
              void respondToAttention(actionableTurn.response)
            }
          }}
        />
      ) : null}
      <div
        aria-live="polite"
        className="mt-3 grid max-h-80 gap-2 overflow-auto"
      >
        {detail.messages.length === 0 ? (
          <p className="text-muted-foreground py-4 text-center text-sm">
            {t(
              "pages.reviews.chat.empty",
              "Ask about the review or request clarification.",
            )}
          </p>
        ) : (
          detail.messages.map((message) => (
            <div
              key={message.id}
              className={cn(
                "max-w-[90%] rounded-lg px-3 py-2 text-sm",
                message.role === "user"
                  ? "bg-primary text-primary-foreground ml-auto"
                  : "bg-muted mr-auto",
              )}
            >
              <div className="mb-1 flex items-center gap-2 text-[11px] opacity-70">
                <span>
                  {message.role === "user"
                    ? t("pages.reviews.chat.you", "You")
                    : t("pages.reviews.chat.assistant", "Assistant")}
                </span>
                <span>·</span>
                <span>{message.kind}</span>
              </div>
              <p className="whitespace-pre-wrap">{message.content}</p>
            </div>
          ))
        )}
      </div>
      <form
        className="mt-3 grid gap-2"
        onSubmit={(event) => void submit(event)}
      >
        <div className="flex flex-col gap-2 sm:flex-row">
          <Label
            htmlFor={`review-chat-scope-${detail.case.id}`}
            className="sr-only"
          >
            {t("pages.reviews.chat.scope", "Finding context")}
          </Label>
          <select
            id={`review-chat-scope-${detail.case.id}`}
            value={findingID}
            disabled={!editable || locked}
            onChange={(event) => setFindingID(event.target.value)}
            className="border-input bg-background focus-visible:border-ring focus-visible:ring-ring/25 h-9 min-w-0 rounded-lg border px-3 text-sm outline-none focus-visible:ring-2 disabled:opacity-50 sm:max-w-xs"
          >
            <option value="">
              {t("pages.reviews.chat.whole_review", "Whole review")}
            </option>
            {activeFindings.map((finding) => (
              <option key={finding.id} value={finding.id}>
                {finding.title}
              </option>
            ))}
          </select>
          <div className="flex min-w-0 flex-1 gap-2">
            <Label
              htmlFor={`review-chat-${detail.case.id}`}
              className="sr-only"
            >
              {t("pages.reviews.chat.message", "Message")}
            </Label>
            <Textarea
              ref={chatInputRef}
              id={`review-chat-${detail.case.id}`}
              value={content}
              maxLength={64 << 10}
              disabled={!editable || locked}
              className="min-h-9 flex-1"
              placeholder={
                editable
                  ? t(
                      "pages.reviews.chat.placeholder",
                      "Ask about this review…",
                    )
                  : t(
                      "pages.reviews.chat.locked",
                      "Conversation is locked for this case.",
                    )
              }
              onChange={(event) => setContent(event.target.value)}
            />
            <Button
              type="submit"
              size="icon"
              disabled={!editable || locked || content.trim() === ""}
              aria-label={t("pages.reviews.chat.send", "Send message")}
            >
              <IconSend />
            </Button>
          </div>
        </div>
        {error ? (
          <p role="alert" className="text-destructive text-sm">
            {error}
          </p>
        ) : null}
        {pending ? (
          <p className="text-muted-foreground text-xs">
            {t("pages.reviews.chat.waiting", "Waiting for the assistant…")}
          </p>
        ) : null}
      </form>
    </section>
  )
}

function OptionalFindingField({
  label,
  value,
  disabled,
  onChange,
}: {
  label: string
  value?: string
  disabled: boolean
  onChange: (value?: string) => void
}) {
  return (
    <Field label={label}>
      <Textarea
        value={value ?? ""}
        disabled={disabled}
        maxLength={64 << 10}
        onChange={(event) => onChange(event.target.value || undefined)}
      />
    </Field>
  )
}

function Field({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <label className="grid min-w-0 gap-1.5 text-xs font-medium">
      <span className="text-muted-foreground">{label}</span>
      {children}
    </label>
  )
}

function StringList({
  title,
  items,
  empty,
}: {
  title: string
  items: string[]
  empty: string
}) {
  return (
    <div>
      <h4 className="text-muted-foreground text-xs font-medium">{title}</h4>
      {items.length > 0 ? (
        <ul className="mt-1 list-disc space-y-1 pl-4 text-sm">
          {items.map((item, index) => (
            <li key={`${index}-${item}`}>{item}</li>
          ))}
        </ul>
      ) : (
        <p className="text-muted-foreground mt-1 text-sm">{empty}</p>
      )}
    </div>
  )
}

function StatusNotice({
  title,
  body,
  destructive,
}: {
  title: string
  body: string
  destructive?: boolean
}) {
  return (
    <div
      className={cn(
        "rounded-lg px-3 py-2 text-sm",
        destructive
          ? "bg-destructive/10 text-destructive"
          : "border-border bg-muted/40 border",
      )}
    >
      <p className="font-medium">{title}</p>
      <p className={cn("mt-1", !destructive && "text-muted-foreground")}>
        {body}
      </p>
    </div>
  )
}

function ReviewStatusBadge({ status }: { status: ReviewCaseStatus }) {
  const { t } = useTranslation()
  const variant =
    status === "submission_unknown" || status === "stale"
      ? "destructive"
      : status === "submitted"
        ? "default"
        : status === "submitting"
          ? "secondary"
          : "outline"
  return (
    <Badge variant={variant}>
      {status === "submitting" ? (
        <span
          aria-hidden="true"
          className="size-1.5 animate-pulse rounded-full bg-current"
        />
      ) : null}
      {reviewStatusLabel(status, t)}
    </Badge>
  )
}

function SeverityBadge({ severity }: { severity: ReviewSeverity }) {
  const { t } = useTranslation()
  return (
    <Badge
      variant={
        severity === "critical" || severity === "high"
          ? "destructive"
          : severity === "medium"
            ? "secondary"
            : "outline"
      }
    >
      {severityLabel(severity, t)}
    </Badge>
  )
}

function ReviewMessageBox({ children }: { children: React.ReactNode }) {
  return (
    <div className="text-muted-foreground flex min-h-40 items-center justify-center px-4 text-center text-sm">
      {children}
    </div>
  )
}

function ReviewError({
  error,
  onRetry,
  compact,
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
        "flex flex-col items-center justify-center gap-2 px-4 text-center",
        compact ? "py-3" : "min-h-40",
      )}
    >
      <p className="text-destructive max-w-full text-sm break-words">
        {reviewErrorMessage(
          error,
          t("pages.reviews.error", "Failed to load reviews."),
        )}
      </p>
      <Button type="button" variant="outline" size="sm" onClick={onRetry}>
        {t("pages.reviews.retry", "Retry")}
      </Button>
    </div>
  )
}

function findingDraft(finding: ReviewFinding): ReviewFindingDraft {
  return {
    severity: finding.severity,
    title: finding.title,
    ...(finding.file ? { file: finding.file } : {}),
    ...(finding.line ? { line: finding.line } : {}),
    message: finding.message,
    ...(finding.evidence ? { evidence: finding.evidence } : {}),
    ...(finding.impact ? { impact: finding.impact } : {}),
    ...(finding.recommendation
      ? { recommendation: finding.recommendation }
      : {}),
    ...(finding.validation ? { validation: finding.validation } : {}),
  }
}

function normalizedDraft(draft: ReviewFindingDraft): ReviewFindingDraft {
  const file = draft.file?.trim()
  const optional = (value: string | undefined) => value?.trim() || undefined
  return {
    severity: draft.severity,
    title: draft.title.trim(),
    ...(file ? { file } : {}),
    ...(file && draft.line ? { line: draft.line } : {}),
    message: draft.message.trim(),
    ...(optional(draft.evidence) ? { evidence: optional(draft.evidence) } : {}),
    ...(optional(draft.impact) ? { impact: optional(draft.impact) } : {}),
    ...(optional(draft.recommendation)
      ? { recommendation: optional(draft.recommendation) }
      : {}),
    ...(optional(draft.validation)
      ? { validation: optional(draft.validation) }
      : {}),
  }
}

function deduplicateCases(cases: ReviewCase[]): ReviewCase[] {
  const seen = new Set<string>()
  return cases.filter((reviewCase) => {
    if (seen.has(reviewCase.id)) {
      return false
    }
    seen.add(reviewCase.id)
    return true
  })
}

function reviewSearchWithoutFocus(
  search: ReviewsRouteSearch,
): ReviewsRouteSearch {
  const next = { ...search }
  delete next.focus
  return next
}

function utf8ByteLength(value: string): number {
  return new TextEncoder().encode(value).byteLength
}

function reviewAttentionErrorMessage(error: unknown, t: Translate): string {
  if (error instanceof ReviewAttentionAPIError && error.status === 409) {
    return t(
      "pages.reviews.attention.conflict",
      "This attention request changed. The latest state was loaded and your reply is preserved.",
    )
  }
  return t(
    "pages.reviews.attention.respond_error",
    "The reply could not be sent. The latest state was loaded and your text is preserved.",
  )
}

function reviewErrorMessage(error: unknown, fallback?: string): string {
  if (error instanceof Error && error.message) {
    return error.message
  }
  return fallback ?? "The review action failed."
}

function formatDate(value: string): string {
  const date = new Date(value)
  return Number.isFinite(date.getTime()) ? date.toLocaleString() : value
}

function shortSHA(value: string): string {
  return value.slice(0, 8)
}

type Translate = (
  key: string,
  fallback: string,
  options?: Record<string, unknown>,
) => string

function reviewStatusLabel(status: ReviewCaseStatus, t: Translate): string {
  const labels: Record<ReviewCaseStatus, string> = {
    open: t("pages.reviews.status.open", "Open"),
    all_dropped: t("pages.reviews.status.all_dropped", "All dropped"),
    submitting: t("pages.reviews.status.submitting", "Submitting"),
    submission_unknown: t(
      "pages.reviews.status.submission_unknown",
      "Outcome unknown",
    ),
    submitted: t("pages.reviews.status.submitted", "Submitted"),
    stale: t("pages.reviews.status.stale", "Stale"),
  }
  return labels[status]
}

function severityLabel(severity: ReviewSeverity, t: Translate): string {
  const labels: Record<ReviewSeverity, string> = {
    critical: t("pages.reviews.severity.critical", "Critical"),
    high: t("pages.reviews.severity.high", "High"),
    medium: t("pages.reviews.severity.medium", "Medium"),
    low: t("pages.reviews.severity.low", "Low"),
  }
  return labels[severity]
}

const reviewCaseStatuses: ReviewCaseStatus[] = [
  "open",
  "all_dropped",
  "submitting",
  "submission_unknown",
  "submitted",
  "stale",
]

const reviewSeverities: ReviewSeverity[] = ["critical", "high", "medium", "low"]
