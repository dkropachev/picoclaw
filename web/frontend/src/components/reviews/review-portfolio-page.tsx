import {
  IconArrowLeft,
  IconArrowRight,
  IconBrandGithub,
  IconCheck,
  IconChevronRight,
  IconCircleCheck,
  IconClock,
  IconCode,
  IconGitPullRequest,
  IconInbox,
  IconMessageCircle,
  IconRefresh,
  IconSearch,
  IconShieldCheck,
} from "@tabler/icons-react"
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"

import { type EventPage, listEvents } from "@/api/events"
import {
  type PRDevelopmentCaseSummary,
  listPRDevelopmentCases,
} from "@/api/pr-development"
import {
  type ReviewProviderSnapshot,
  type ReviewProviderThread,
  type ReviewProviderThreadAction,
  getReviewProviderSnapshot,
  getReviewProviderStatus,
  mutateReviewProviderThread,
} from "@/api/review-provider"
import {
  type ReviewCase,
  type ReviewCaseStatus,
  listReviews,
} from "@/api/reviews"
import { PageHeader } from "@/components/page-header"
import {
  type LiveReviewPullRequestState,
  type ReviewProviderStatusTarget,
  type ReviewRepositorySummary,
  type ReviewWorkItem,
  type ReviewWorkRole,
  buildReviewPortfolio,
  externalPullReviews,
  reviewProviderStatusTargets,
} from "@/components/reviews/review-portfolio-model"
import {
  applyReviewQuerySuggestion,
  filterReviewWorkItems,
  getReviewQuerySuggestions,
  parseReviewQuery,
} from "@/components/reviews/review-query-model"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { cn } from "@/lib/utils"

const PAGE_SIZE = 100
const EVENTS_PAGE_SIZE = 100
const PROVIDER_STATUS_CONCURRENCY = 4

function nextUnseenCursor(
  page: { next_cursor?: string },
  _pages: unknown[],
  _pageParam: unknown,
  pageParams: unknown[],
) {
  const nextCursor = page.next_cursor?.trim()
  if (!nextCursor || pageParams.includes(nextCursor)) return undefined
  return nextCursor
}

function hasRepeatedCursor(data?: {
  pages: { next_cursor?: string }[]
  pageParams: unknown[]
}) {
  const nextCursor = data?.pages.at(-1)?.next_cursor?.trim()
  return Boolean(nextCursor && data?.pageParams.includes(nextCursor))
}

export interface ReviewPortfolioSearch {
  repo?: string
  pr?: number
  filter?: string
  role?: ReviewWorkRole
  review_case?: string
}

export function ReviewPortfolioPage({
  search,
  onSearchChange,
  onOpenReview,
}: {
  search: ReviewPortfolioSearch
  onSearchChange: (search: ReviewPortfolioSearch, replace?: boolean) => void
  onOpenReview: (caseID: string, repository: string, pullNumber: number) => void
}) {
  const queryClient = useQueryClient()
  const [repositorySearch, setRepositorySearch] = useState("")
  const [providerStatusRefresh, setProviderStatusRefresh] = useState(0)
  const reviewQuery = useInfiniteQuery({
    queryKey: ["review-portfolio", "reviews"],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      listReviews({ limit: PAGE_SIZE, cursor: pageParam || undefined }),
    getNextPageParam: nextUnseenCursor,
  })
  const developmentQuery = useInfiniteQuery({
    queryKey: ["review-portfolio", "development"],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      listPRDevelopmentCases({
        limit: PAGE_SIZE,
        cursor: pageParam || undefined,
      }),
    getNextPageParam: nextUnseenCursor,
  })
  const {
    fetchNextPage: fetchNextReviewPage,
    hasNextPage: hasNextReviewPage,
    isFetchNextPageError: isReviewPageError,
    isFetchingNextPage: isFetchingNextReviewPage,
  } = reviewQuery
  const {
    fetchNextPage: fetchNextDevelopmentPage,
    hasNextPage: hasNextDevelopmentPage,
    isFetchNextPageError: isDevelopmentPageError,
    isFetchingNextPage: isFetchingNextDevelopmentPage,
  } = developmentQuery

  useEffect(() => {
    if (hasNextReviewPage && !isFetchingNextReviewPage && !isReviewPageError) {
      void fetchNextReviewPage()
    }
  }, [
    fetchNextReviewPage,
    hasNextReviewPage,
    isFetchingNextReviewPage,
    isReviewPageError,
  ])
  useEffect(() => {
    if (
      hasNextDevelopmentPage &&
      !isFetchingNextDevelopmentPage &&
      !isDevelopmentPageError
    ) {
      void fetchNextDevelopmentPage()
    }
  }, [
    fetchNextDevelopmentPage,
    hasNextDevelopmentPage,
    isDevelopmentPageError,
    isFetchingNextDevelopmentPage,
  ])

  const reviewCases = useMemo(
    () =>
      reviewQuery.data?.pages.flatMap((page) => page.cases) ??
      ([] as ReviewCase[]),
    [reviewQuery.data?.pages],
  )
  const developmentCases = useMemo(
    () =>
      developmentQuery.data?.pages.flatMap((page) => page.cases) ??
      ([] as PRDevelopmentCaseSummary[]),
    [developmentQuery.data?.pages],
  )
  const loading = reviewQuery.isPending || developmentQuery.isPending
  const hasAnyData = reviewQuery.data != null || developmentQuery.data != null
  const portfolioError = reviewQuery.error ?? developmentQuery.error
  const initialError = hasAnyData ? null : portfolioError
  const paginationStalled =
    hasRepeatedCursor(reviewQuery.data) ||
    hasRepeatedCursor(developmentQuery.data)
  const partialError =
    hasAnyData && Boolean(portfolioError || paginationStalled)
  const stillLoadingAll =
    (!isReviewPageError && reviewQuery.hasNextPage) ||
    (!isDevelopmentPageError && developmentQuery.hasNextPage) ||
    reviewQuery.isFetchingNextPage ||
    developmentQuery.isFetchingNextPage
  const statusTargets = useMemo(
    () => reviewProviderStatusTargets(reviewCases),
    [reviewCases],
  )
  const providerStatusEnabled =
    hasAnyData &&
    !stillLoadingAll &&
    !reviewQuery.isFetching &&
    !developmentQuery.isFetching
  const providerStatus = useReviewProviderStatuses(
    statusTargets,
    providerStatusEnabled,
    providerStatusRefresh,
  )
  const repositories = useMemo(
    () =>
      buildReviewPortfolio(
        reviewCases,
        developmentCases,
        providerStatus.liveStates,
      ),
    [developmentCases, providerStatus.liveStates, reviewCases],
  )
  const selectedRepository = repositories.find(
    (repository) =>
      repository.repository.toLowerCase() === search.repo?.toLowerCase(),
  )
  const selectedItem = selectedRepository?.items.find(
    (item) => item.pullNumber === search.pr,
  )
  const refresh = async () => {
    setProviderStatusRefresh((value) => value + 1)
    await queryClient.invalidateQueries({ queryKey: ["review-portfolio"] })
  }

  return (
    <div className="bg-background flex h-full min-h-0 flex-col">
      <PageHeader
        title="Pull request work"
        titleExtra={
          <Badge variant="outline" className="hidden sm:inline-flex">
            Only work tracked by PicoClaw
          </Badge>
        }
      >
        <Button
          type="button"
          variant="outline"
          size="icon"
          aria-label="Refresh pull request work"
          title="Refresh pull request work"
          onClick={() => void refresh()}
          disabled={
            reviewQuery.isFetching ||
            developmentQuery.isFetching ||
            providerStatus.checking
          }
        >
          <IconRefresh
            className={cn(
              "size-4",
              (reviewQuery.isFetching ||
                developmentQuery.isFetching ||
                providerStatus.checking) &&
                "animate-spin",
            )}
          />
        </Button>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-auto">
        <ProviderStatusNotice
          status={providerStatus}
          onRetry={() => setProviderStatusRefresh((value) => value + 1)}
        />
        {search.repo && selectedRepository && partialError ? (
          <div className="border-border bg-muted/30 m-4 mb-0 flex flex-wrap items-center justify-between gap-2 rounded-lg border px-3 py-2 text-xs sm:mx-6 lg:mx-8">
            <span>Some older tracked work could not be loaded.</span>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => void refresh()}
            >
              Retry
            </Button>
          </div>
        ) : null}
        {!search.repo ? (
          <RepositoryOverview
            repositories={repositories}
            loading={loading}
            loadingAll={Boolean(stillLoadingAll)}
            error={initialError}
            partialError={partialError}
            search={repositorySearch}
            onSearchChange={setRepositorySearch}
            onSelect={(repository) =>
              onSearchChange({ repo: repository.repository })
            }
            onRetry={() => void refresh()}
          />
        ) : !selectedRepository && (loading || stillLoadingAll) ? (
          <PortfolioNotice>Loading repository work…</PortfolioNotice>
        ) : !selectedRepository && (portfolioError || paginationStalled) ? (
          <MissingSelection
            title="Repository unavailable"
            body="PicoClaw could not load the repository work needed for this link."
            onBack={() => onSearchChange({}, true)}
          />
        ) : !selectedRepository ? (
          <MissingSelection
            title="Repository not found"
            body="This repository has no pull request work captured by PicoClaw."
            onBack={() => onSearchChange({}, true)}
          />
        ) : search.pr && !selectedItem && (loading || stillLoadingAll) ? (
          <PortfolioNotice>Loading pull request work…</PortfolioNotice>
        ) : search.pr &&
          !selectedItem &&
          (portfolioError || paginationStalled) ? (
          <MissingSelection
            title="Pull request unavailable"
            body="PicoClaw could not load the tracked work needed for this link."
            onBack={() =>
              onSearchChange({ repo: selectedRepository.repository }, true)
            }
          />
        ) : search.pr && !selectedItem ? (
          <MissingSelection
            title="Pull request not found"
            body="This pull request has no work captured by PicoClaw."
            onBack={() =>
              onSearchChange({ repo: selectedRepository.repository }, true)
            }
          />
        ) : selectedItem ? (
          <PullRequestWorkspace
            item={selectedItem}
            requestedRole={search.role}
            requestedCaseID={search.review_case}
            onRoleChange={(role) =>
              onSearchChange(
                {
                  repo: selectedRepository.repository,
                  pr: selectedItem.pullNumber,
                  ...(search.filter ? { filter: search.filter } : {}),
                  role,
                  ...(search.review_case
                    ? { review_case: search.review_case }
                    : {}),
                },
                true,
              )
            }
            onCaseChange={(caseID) =>
              onSearchChange(
                {
                  repo: selectedRepository.repository,
                  pr: selectedItem.pullNumber,
                  ...(search.filter ? { filter: search.filter } : {}),
                  role: "review",
                  review_case: caseID,
                },
                true,
              )
            }
            onBack={() =>
              onSearchChange(
                {
                  repo: selectedRepository.repository,
                  ...(search.filter ? { filter: search.filter } : {}),
                },
                true,
              )
            }
            onLiveStateChange={providerStatus.observe}
            onOpenReview={onOpenReview}
          />
        ) : (
          <RepositoryPullRequests
            repository={selectedRepository}
            query={search.filter ?? ""}
            onQueryChange={(filter) =>
              onSearchChange(
                {
                  repo: selectedRepository.repository,
                  ...(filter ? { filter } : {}),
                },
                true,
              )
            }
            onBack={() => onSearchChange({}, true)}
            onSelect={(item) =>
              onSearchChange({
                repo: selectedRepository.repository,
                pr: item.pullNumber,
                ...(search.filter ? { filter: search.filter } : {}),
              })
            }
          />
        )}
      </div>
    </div>
  )
}

interface ReviewProviderStatusState {
  liveStates: ReadonlyMap<string, LiveReviewPullRequestState>
  checking: boolean
  checked: number
  total: number
  failures: number
  observe: (key: string, state: LiveReviewPullRequestState) => void
}

interface ReviewProviderStatusProgress {
  liveStates: ReadonlyMap<string, LiveReviewPullRequestState>
  liveStateOrder: ReadonlyMap<string, number>
  checking: boolean
  checked: number
  total: number
  failures: number
}

function useReviewProviderStatuses(
  targets: ReviewProviderStatusTarget[],
  enabled: boolean,
  refresh: number,
): ReviewProviderStatusState {
  const observationOrder = useRef(0)
  const [state, setState] = useState<ReviewProviderStatusProgress>({
    liveStates: new Map(),
    liveStateOrder: new Map(),
    checking: false,
    checked: 0,
    total: 0,
    failures: 0,
  })
  const targetKey = useMemo(
    () => targets.map((target) => `${target.key}:${target.caseID}`).join("|"),
    [targets],
  )
  const observationGeneration = `${refresh}:${targetKey}`
  const observationGenerationRef = useRef(observationGeneration)
  useEffect(() => {
    observationGenerationRef.current = observationGeneration
  }, [observationGeneration])
  const [observations, setObservations] = useState<{
    generation: string
    liveStates: ReadonlyMap<string, LiveReviewPullRequestState>
    liveStateOrder: ReadonlyMap<string, number>
  }>({ generation: "", liveStates: new Map(), liveStateOrder: new Map() })
  const observe = useCallback(
    (key: string, liveState: LiveReviewPullRequestState) => {
      setObservations((current) => {
        const generation = observationGenerationRef.current
        const liveStates =
          current.generation === generation
            ? new Map(current.liveStates)
            : new Map<string, LiveReviewPullRequestState>()
        const liveStateOrder =
          current.generation === generation
            ? new Map(current.liveStateOrder)
            : new Map<string, number>()
        const existing = liveStates.get(key)
        const order = ++observationOrder.current
        if (sameLivePullRequestState(existing, liveState)) {
          liveStateOrder.set(key, order)
          return { generation, liveStates, liveStateOrder }
        }
        liveStates.set(key, liveState)
        liveStateOrder.set(key, order)
        return { generation, liveStates, liveStateOrder }
      })
    },
    [],
  )

  useEffect(() => {
    if (!enabled || targets.length === 0) {
      setState({
        liveStates: new Map(),
        liveStateOrder: new Map(),
        checking: false,
        checked: 0,
        total: 0,
        failures: 0,
      })
      return
    }

    const controller = new AbortController()
    let nextIndex = 0
    const liveStates = new Map<string, LiveReviewPullRequestState>()
    const liveStateOrder = new Map<string, number>()
    let checked = 0
    let failures = 0
    setState({
      liveStates: new Map(),
      liveStateOrder: new Map(),
      checking: true,
      checked: 0,
      total: targets.length,
      failures: 0,
    })

    const publishProgress = () => {
      if (controller.signal.aborted) return
      setState({
        liveStates: new Map(liveStates),
        liveStateOrder: new Map(liveStateOrder),
        checking: checked < targets.length,
        checked,
        total: targets.length,
        failures,
      })
    }

    const readNext = async (): Promise<void> => {
      while (!controller.signal.aborted) {
        const target = targets[nextIndex]
        nextIndex += 1
        if (target == null) return
        try {
          const status = await getReviewProviderStatus(
            target.caseID,
            controller.signal,
          )
          if (
            status.repository.toLowerCase() !==
              target.repository.toLowerCase() ||
            status.pull_number !== target.pullNumber
          ) {
            failures += 1
            continue
          }
          if (
            status.availability === "unavailable" ||
            status.availability === "incompatible"
          ) {
            failures += 1
            continue
          }
          const pullRequest = status.pull_request
          if (pullRequest == null) {
            failures += 1
            continue
          }
          liveStates.set(target.key, {
            state: pullRequest.state,
            merged: pullRequest.merged,
            title: pullRequest.title,
            url: pullRequest.url,
            ...(pullRequest.author == null
              ? {}
              : { author: pullRequest.author }),
            ...(pullRequest.updated_at == null
              ? {}
              : { updatedAt: pullRequest.updated_at }),
          })
          liveStateOrder.set(target.key, ++observationOrder.current)
        } catch {
          if (!controller.signal.aborted) failures += 1
        } finally {
          if (!controller.signal.aborted) {
            checked += 1
            publishProgress()
          }
        }
      }
    }

    void Promise.all(
      Array.from(
        { length: Math.min(PROVIDER_STATUS_CONCURRENCY, targets.length) },
        () => readNext(),
      ),
    ).then(() => {
      if (controller.signal.aborted) return
      publishProgress()
    })

    return () => controller.abort()
  }, [enabled, observe, refresh, targetKey, targets])

  const liveStates = new Map(state.liveStates)
  if (observations.generation === observationGeneration) {
    for (const [key, liveState] of observations.liveStates) {
      const statusState = liveStates.get(key)
      if (
        livePullRequestStateIsNewer(
          liveState,
          observations.liveStateOrder.get(key) ?? 0,
          statusState,
          state.liveStateOrder.get(key) ?? 0,
        )
      ) {
        liveStates.set(key, liveState)
      }
    }
  }
  return {
    liveStates,
    checking: state.checking,
    checked: state.checked,
    total: state.total,
    failures: state.failures,
    observe,
  }
}

function livePullRequestStateIsNewer(
  candidate: LiveReviewPullRequestState,
  candidateOrder: number,
  current: LiveReviewPullRequestState | undefined,
  currentOrder: number,
) {
  if (current == null) return true
  const candidateTime = Date.parse(candidate.updatedAt ?? "")
  const currentTime = Date.parse(current.updatedAt ?? "")
  if (
    Number.isFinite(candidateTime) &&
    Number.isFinite(currentTime) &&
    candidateTime !== currentTime
  ) {
    return candidateTime > currentTime
  }
  return candidateOrder >= currentOrder
}

function sameLivePullRequestState(
  left: LiveReviewPullRequestState | undefined,
  right: LiveReviewPullRequestState,
): boolean {
  return (
    left?.state === right.state &&
    left.merged === right.merged &&
    left.title === right.title &&
    left.url === right.url &&
    left.author === right.author &&
    left.updatedAt === right.updatedAt
  )
}

function ProviderStatusNotice({
  status,
  onRetry,
}: {
  status: ReviewProviderStatusState
  onRetry: () => void
}) {
  if (status.total === 0 || (!status.checking && status.failures === 0)) {
    return null
  }
  return (
    <div className="border-border bg-muted/30 m-4 mb-0 flex flex-wrap items-center justify-between gap-2 rounded-lg border px-3 py-2 text-xs sm:mx-6 lg:mx-8">
      <span>
        {status.checking
          ? `Checking live provider state: ${status.checked} of ${status.total}.`
          : `Live provider state could not be verified for ${status.failures} of ${status.total} tracked pull requests; captured workflow state remains visible.`}
      </span>
      {!status.checking && status.failures > 0 ? (
        <Button type="button" variant="outline" size="sm" onClick={onRetry}>
          Retry live status
        </Button>
      ) : null}
    </div>
  )
}

function RepositoryOverview({
  repositories,
  loading,
  loadingAll,
  error,
  partialError,
  search,
  onSearchChange,
  onSelect,
  onRetry,
}: {
  repositories: ReviewRepositorySummary[]
  loading: boolean
  loadingAll: boolean
  error: unknown
  partialError: boolean
  search: string
  onSearchChange: (value: string) => void
  onSelect: (repository: ReviewRepositorySummary) => void
  onRetry: () => void
}) {
  const normalizedSearch = search.trim().toLowerCase()
  const visibleRepositories = repositories.filter((repository) =>
    repository.repository.toLowerCase().includes(normalizedSearch),
  )
  const totals = repositories.reduce(
    (result, repository) => ({
      pending: result.pending + repository.pending,
      needsAction: result.needsAction + repository.needsAction,
      closed: result.closed + repository.closed,
      liveClosed: result.liveClosed + repository.liveClosed,
      capturedClosed: result.capturedClosed + repository.capturedClosed,
      complete: result.complete + repository.complete,
    }),
    {
      pending: 0,
      needsAction: 0,
      closed: 0,
      liveClosed: 0,
      capturedClosed: 0,
      complete: 0,
    },
  )

  return (
    <div className="mx-auto w-full max-w-7xl p-4 sm:p-6 lg:p-8">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <p className="text-muted-foreground text-xs font-semibold tracking-wider uppercase">
            Repository overview
          </p>
          <h1 className="mt-1 text-2xl font-semibold tracking-tight">
            Pull request work
          </h1>
          <p className="text-muted-foreground mt-1 max-w-2xl text-sm">
            Review and development work PicoClaw has handled or is expected to
            handle, grouped by repository.
          </p>
        </div>
      </div>

      <div className="mt-6 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <SummaryCard
          label="PRs with pending work"
          value={totals.pending}
          icon={IconClock}
          tone="amber"
        />
        <SummaryCard
          label="Needs action"
          value={totals.needsAction}
          icon={IconInbox}
          tone="rose"
        />
        <SummaryCard
          label="Finished review work"
          value={totals.complete}
          icon={IconCircleCheck}
          tone="emerald"
        />
        <SummaryCard
          label="Closed PRs"
          value={totals.closed}
          icon={IconCheck}
          tone="emerald"
        />
      </div>

      <p className="text-muted-foreground mt-3 text-xs">
        Closed state is provider-verified: {totals.liveClosed} live and{" "}
        {totals.capturedClosed} from development captures. Counts stay
        provisional while the live provider check above is running.
      </p>

      <div className="mt-6 flex items-center gap-3">
        <div className="relative max-w-md flex-1">
          <IconSearch className="text-muted-foreground pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2" />
          <Input
            value={search}
            onChange={(event) => onSearchChange(event.target.value)}
            placeholder="Find a repository…"
            aria-label="Find a repository"
            className="pl-9"
          />
        </div>
        {loadingAll ? (
          <Badge variant="secondary">Loading complete history…</Badge>
        ) : null}
      </div>

      {partialError ? (
        <div className="border-border bg-muted/30 mt-3 flex flex-wrap items-center justify-between gap-2 rounded-lg border px-3 py-2 text-xs">
          <span>Some older tracked work could not be loaded.</span>
          <Button type="button" variant="outline" size="sm" onClick={onRetry}>
            Retry
          </Button>
        </div>
      ) : null}

      <section aria-label="Repositories" className="mt-4">
        {loading ? (
          <PortfolioNotice>Loading pull request work…</PortfolioNotice>
        ) : error ? (
          <PortfolioNotice>
            <p>Pull request work could not be loaded.</p>
            <Button type="button" variant="outline" size="sm" onClick={onRetry}>
              Try again
            </Button>
          </PortfolioNotice>
        ) : visibleRepositories.length === 0 ? (
          <PortfolioNotice>
            {repositories.length === 0
              ? "No pull request work has been captured yet."
              : "No repository matches your search."}
          </PortfolioNotice>
        ) : (
          <div className="grid gap-3 lg:grid-cols-2">
            {visibleRepositories.map((repository) => (
              <button
                key={repository.repository}
                type="button"
                onClick={() => onSelect(repository)}
                className="border-border bg-card hover:border-foreground/30 hover:bg-accent/30 group rounded-xl border p-4 text-left shadow-sm transition-colors"
              >
                <div className="flex items-start justify-between gap-4">
                  <div className="flex min-w-0 items-center gap-3">
                    <div className="bg-muted flex size-10 shrink-0 items-center justify-center rounded-lg">
                      <IconBrandGithub className="size-5" />
                    </div>
                    <div className="min-w-0">
                      <h2 className="truncate font-semibold">
                        {repository.repository}
                      </h2>
                      <p className="text-muted-foreground mt-0.5 text-xs">
                        {repository.items.length} tracked pull request
                        {repository.items.length === 1 ? "" : "s"}
                      </p>
                    </div>
                  </div>
                  <IconChevronRight className="text-muted-foreground group-hover:text-foreground mt-2 size-5 transition-colors" />
                </div>

                <div className="mt-4 grid grid-cols-2 gap-2 sm:grid-cols-4">
                  <Metric label="Pending" value={repository.pending} />
                  <Metric
                    label="Needs action"
                    value={repository.needsAction}
                    urgent={repository.needsAction > 0}
                  />
                  <Metric label="Finished" value={repository.complete} />
                  <Metric label="Closed" value={repository.closed} />
                </div>
                <p className="text-muted-foreground mt-2 text-[11px]">
                  {repository.liveClosed} live closed ·{" "}
                  {repository.capturedClosed} captured closed
                </p>
                <div className="border-border mt-4 flex flex-wrap gap-2 border-t pt-3">
                  <RoleBadge workRole="review" count={repository.reviewing} />
                  <RoleBadge workRole="develop" count={repository.developing} />
                </div>
              </button>
            ))}
          </div>
        )}
      </section>
    </div>
  )
}

function RepositoryPullRequests({
  repository,
  query,
  onQueryChange,
  onBack,
  onSelect,
}: {
  repository: ReviewRepositorySummary
  query: string
  onQueryChange: (value: string) => void
  onBack: () => void
  onSelect: (item: ReviewWorkItem) => void
}) {
  const [draft, setDraft] = useState(query)
  const [suggestionsOpen, setSuggestionsOpen] = useState(false)
  const [activeSuggestion, setActiveSuggestion] = useState(-1)
  const inputRef = useRef<HTMLInputElement>(null)
  useEffect(() => setDraft(query), [query])
  const parsedQuery = useMemo(() => parseReviewQuery(draft), [draft])
  const filteredItems = useMemo(
    () => filterReviewWorkItems(repository.items, query),
    [query, repository.items],
  )
  const suggestions = useMemo(
    () => getReviewQuerySuggestions(draft, repository.items).slice(0, 8),
    [draft, repository.items],
  )
  useEffect(() => setActiveSuggestion(-1), [draft])

  const applySuggestion = (index: number) => {
    const suggestion = suggestions[index]
    if (!suggestion) return
    setDraft(applyReviewQuerySuggestion(draft, suggestion))
    setSuggestionsOpen(true)
    requestAnimationFrame(() => inputRef.current?.focus())
  }

  return (
    <div className="mx-auto w-full max-w-7xl p-4 sm:p-6 lg:p-8">
      <Button type="button" variant="ghost" size="sm" onClick={onBack}>
        <IconArrowLeft />
        All repositories
      </Button>
      <div className="mt-4 flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <p className="text-muted-foreground text-xs font-semibold tracking-wider uppercase">
            Repository
          </p>
          <h1 className="mt-1 text-2xl font-semibold tracking-tight">
            {repository.repository}
          </h1>
          <p className="text-muted-foreground mt-1 text-sm">
            {repository.pending} pending · {repository.needsAction} need action
            · {repository.complete} finished · {repository.closed} closed (
            {repository.liveClosed} live, {repository.capturedClosed} captured)
          </p>
        </div>
        <div className="flex gap-2">
          <RoleBadge workRole="review" count={repository.reviewing} />
          <RoleBadge workRole="develop" count={repository.developing} />
        </div>
      </div>

      <form
        className="mt-6"
        onSubmit={(event) => {
          event.preventDefault()
          if (!parsedQuery.valid) return
          onQueryChange(draft.trim())
        }}
      >
        <label htmlFor="pr-work-query" className="text-sm font-medium">
          Filter pull requests
        </label>
        <div className="relative mt-2">
          <IconSearch className="text-muted-foreground pointer-events-none absolute top-3 left-3 size-4" />
          <Input
            ref={inputRef}
            id="pr-work-query"
            value={draft}
            onChange={(event) => {
              setDraft(event.target.value)
              setSuggestionsOpen(true)
            }}
            onFocus={() => setSuggestionsOpen(true)}
            onBlur={() => setSuggestionsOpen(false)}
            onKeyDown={(event) => {
              if (event.key === "ArrowDown" && suggestions.length > 0) {
                event.preventDefault()
                setSuggestionsOpen(true)
                setActiveSuggestion((current) =>
                  Math.min(current + 1, suggestions.length - 1),
                )
              } else if (event.key === "ArrowUp" && suggestions.length > 0) {
                event.preventDefault()
                setSuggestionsOpen(true)
                setActiveSuggestion((current) => Math.max(current - 1, 0))
              } else if (
                event.key === "Enter" &&
                suggestionsOpen &&
                activeSuggestion >= 0
              ) {
                event.preventDefault()
                applySuggestion(activeSuggestion)
              } else if (event.key === "Escape") {
                setSuggestionsOpen(false)
              }
            }}
            placeholder={"Try: status = pending AND role = review"}
            className="h-10 pr-24 pl-9 font-mono text-sm"
            autoComplete="off"
            maxLength={2048}
            role="combobox"
            aria-autocomplete="list"
            aria-expanded={suggestionsOpen && suggestions.length > 0}
            aria-controls="pr-work-query-suggestions"
            aria-activedescendant={
              suggestionsOpen && activeSuggestion >= 0
                ? `pr-work-query-suggestion-${activeSuggestion}`
                : undefined
            }
          />
          <Button
            type="submit"
            size="sm"
            className="absolute top-1 right-1"
            disabled={!parsedQuery.valid}
          >
            Search
          </Button>
          {suggestionsOpen && suggestions.length > 0 ? (
            <div
              id="pr-work-query-suggestions"
              role="listbox"
              aria-label="Filter suggestions"
              className="border-border bg-popover absolute top-11 right-0 left-0 z-20 max-h-64 overflow-auto rounded-lg border p-1 shadow-lg"
            >
              {suggestions.map((suggestion, index) => (
                <button
                  type="button"
                  role="option"
                  aria-label={suggestion.label}
                  aria-selected={index === activeSuggestion}
                  id={`pr-work-query-suggestion-${index}`}
                  key={`${suggestion.kind}:${suggestion.label}:${suggestion.replaceStart}`}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => applySuggestion(index)}
                  onMouseEnter={() => setActiveSuggestion(index)}
                  className={cn(
                    "hover:bg-accent flex w-full items-center justify-between gap-3 rounded-md px-3 py-2 text-left text-sm",
                    index === activeSuggestion && "bg-accent",
                  )}
                >
                  <span className="font-mono">{suggestion.label}</span>
                  {suggestion.detail ? (
                    <span className="text-muted-foreground truncate text-xs">
                      {suggestion.detail}
                    </span>
                  ) : null}
                </button>
              ))}
            </div>
          ) : null}
        </div>
        {!parsedQuery.valid ? (
          <p className="text-destructive mt-2 text-sm" role="alert">
            {parsedQuery.errors[0]?.message}
          </p>
        ) : (
          <p className="text-muted-foreground mt-2 text-xs">
            Fields: status, role, attention, author, reviewer, number, updated,
            text. Operators: =, !=, ~. Join clauses with AND. Author and
            reviewer identities are available where development feedback
            captured them.
          </p>
        )}
      </form>

      <section
        aria-label="Pull requests"
        className="mt-5 overflow-hidden rounded-xl border"
      >
        <div className="bg-muted/40 text-muted-foreground hidden grid-cols-[minmax(0,1fr)_7rem_8rem_7rem_2rem] gap-3 border-b px-4 py-2 text-xs font-medium lg:grid">
          <span>Pull request</span>
          <span>Role</span>
          <span>Status</span>
          <span>Updated</span>
          <span />
        </div>
        {filteredItems.length === 0 ? (
          <PortfolioNotice>No pull requests match this filter.</PortfolioNotice>
        ) : (
          filteredItems.map((item) => (
            <button
              type="button"
              key={item.key}
              onClick={() => onSelect(item)}
              className="border-border hover:bg-accent/30 grid w-full gap-3 border-b p-4 text-left last:border-b-0 lg:grid-cols-[minmax(0,1fr)_7rem_8rem_7rem_2rem] lg:items-center"
            >
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <IconGitPullRequest className="text-muted-foreground size-4 shrink-0" />
                  <span className="font-medium">#{item.pullNumber}</span>
                  {item.needsAction ? (
                    <Badge variant="destructive" className="text-[10px]">
                      Needs action
                    </Badge>
                  ) : null}
                </div>
                <p className="mt-1 truncate text-sm">{item.title}</p>
                <p className="text-muted-foreground mt-1 text-xs lg:hidden">
                  {formatRelativeDate(item.updatedAt)}
                </p>
              </div>
              <div className="flex flex-wrap gap-1">
                {item.roles.map((role) => (
                  <RoleBadge key={role} workRole={role} />
                ))}
              </div>
              <StatusBadge
                status={item.status}
                closureSource={item.closureSource}
              />
              <span className="text-muted-foreground hidden text-xs lg:block">
                {formatRelativeDate(item.updatedAt)}
              </span>
              <IconChevronRight className="text-muted-foreground hidden size-4 lg:block" />
            </button>
          ))
        )}
      </section>
    </div>
  )
}

function PullRequestWorkspace({
  item,
  requestedRole,
  requestedCaseID,
  onRoleChange,
  onCaseChange,
  onBack,
  onLiveStateChange,
  onOpenReview,
}: {
  item: ReviewWorkItem
  requestedRole?: ReviewWorkRole
  requestedCaseID?: string
  onRoleChange: (role: ReviewWorkRole) => void
  onCaseChange: (caseID: string) => void
  onBack: () => void
  onLiveStateChange: (key: string, state: LiveReviewPullRequestState) => void
  onOpenReview: (caseID: string, repository: string, pullNumber: number) => void
}) {
  const [activeRole, setActiveRole] = useState<"review" | "develop">(
    requestedRole && item.roles.includes(requestedRole)
      ? requestedRole
      : item.roles.includes("review")
        ? "review"
        : "develop",
  )
  const hasReviewRole = item.roles.includes("review")
  useEffect(() => {
    const nextRole =
      requestedRole && item.roles.includes(requestedRole)
        ? requestedRole
        : hasReviewRole
          ? "review"
          : "develop"
    setActiveRole(nextRole)
    if (requestedRole && requestedRole !== nextRole) onRoleChange(nextRole)
  }, [hasReviewRole, item.key, item.roles, onRoleChange, requestedRole])
  const preferredReviewCase =
    item.reviewCases.find(isEditableReviewCase) ?? item.reviewCases[0]
  const selectedReviewCase =
    item.reviewCases.find((reviewCase) => reviewCase.id === requestedCaseID) ??
    preferredReviewCase
  const providerQueryKey = [
    "review-portfolio",
    "provider",
    selectedReviewCase?.id,
  ] as const
  const providerQuery = useQuery({
    queryKey: providerQueryKey,
    queryFn: ({ signal }) =>
      getReviewProviderSnapshot(selectedReviewCase!.id, signal),
    enabled: activeRole === "review" && selectedReviewCase != null,
    retry: false,
  })
  const livePullRequest = providerQuery.data?.pull_request
  const livePullRequestUpdatedAt = providerQuery.dataUpdatedAt
  useEffect(() => {
    if (livePullRequest == null) return
    onLiveStateChange(item.key, {
      state: livePullRequest.state,
      merged: livePullRequest.merged,
      title: livePullRequest.title,
      url: livePullRequest.url,
      ...(livePullRequest.author == null
        ? {}
        : { author: livePullRequest.author }),
      ...(livePullRequest.updated_at == null
        ? {}
        : { updatedAt: livePullRequest.updated_at }),
    })
  }, [item.key, livePullRequest, livePullRequestUpdatedAt, onLiveStateChange])
  const retainedFallbackActive =
    activeRole === "review" &&
    (providerQuery.isError ||
      providerQuery.data?.availability === "unavailable" ||
      providerQuery.data?.availability === "incompatible")
  const eventQuery = useInfiniteQuery({
    queryKey: ["review-portfolio", "external-reviews"],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      listEvents({
        source: "github",
        type: "pull_request_review.submitted",
        limit: EVENTS_PAGE_SIZE,
        cursor: pageParam || undefined,
      }),
    getNextPageParam: (page: EventPage) => page.next_cursor || undefined,
    enabled: retainedFallbackActive,
  })
  const {
    fetchNextPage: fetchNextEventPage,
    hasNextPage: hasNextEventPage,
    isFetchingNextPage: isFetchingNextEventPage,
  } = eventQuery
  const reviews = useMemo(
    () =>
      retainedFallbackActive
        ? externalPullReviews(
            eventQuery.data?.pages.flatMap((page) => page.events) ?? [],
            item.repository,
            item.pullNumber,
            item.developmentCases,
          )
        : [],
    [
      eventQuery.data?.pages,
      item.developmentCases,
      item.pullNumber,
      item.repository,
      retainedFallbackActive,
    ],
  )

  return (
    <div className="mx-auto w-full max-w-7xl p-4 sm:p-6 lg:p-8">
      <Button type="button" variant="ghost" size="sm" onClick={onBack}>
        <IconArrowLeft />
        {item.repository}
      </Button>
      <div className="mt-4 flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <StatusBadge
              status={item.status}
              closureSource={item.closureSource}
            />
            {item.needsAction ? (
              <Badge variant="destructive">Needs action</Badge>
            ) : null}
          </div>
          <h1 className="mt-3 text-2xl font-semibold tracking-tight">
            <span className="text-muted-foreground">#{item.pullNumber}</span>{" "}
            {item.title}
          </h1>
          <p className="text-muted-foreground mt-2 text-sm">
            {item.repository} · Updated {formatDate(item.updatedAt)}
          </p>
        </div>
        {item.pullURL ? (
          <Button type="button" variant="outline" asChild>
            <a href={item.pullURL} target="_blank" rel="noreferrer">
              <IconBrandGithub />
              Open pull request
            </a>
          </Button>
        ) : null}
      </div>

      {item.roles.length > 1 ? (
        <div className="border-border mt-6 flex gap-1 border-b">
          <RoleTab
            workRole="review"
            selected={activeRole === "review"}
            onSelect={() => {
              setActiveRole("review")
              onRoleChange("review")
            }}
          />
          <RoleTab
            workRole="develop"
            selected={activeRole === "develop"}
            onSelect={() => {
              setActiveRole("develop")
              onRoleChange("develop")
            }}
          />
        </div>
      ) : null}

      <div className="mt-6">
        {activeRole === "review" ? (
          <ReviewRoleWorkspace
            item={item}
            selectedCase={selectedReviewCase}
            externalReviews={reviews}
            historyLoading={
              retainedFallbackActive &&
              (eventQuery.isPending || isFetchingNextEventPage)
            }
            historyError={eventQuery.error}
            hasOlderHistory={Boolean(hasNextEventPage)}
            onLoadOlderHistory={() => void fetchNextEventPage()}
            onRetryHistory={() => void eventQuery.refetch()}
            retainedFallbackActive={retainedFallbackActive}
            providerSnapshot={providerQuery.data}
            providerLoading={providerQuery.isPending}
            providerRefreshing={providerQuery.isFetching}
            providerError={providerQuery.error}
            providerQueryKey={providerQueryKey}
            onRetryProvider={() => void providerQuery.refetch()}
            onCaseChange={onCaseChange}
            onOpenReview={(caseID) =>
              onOpenReview(caseID, item.repository, item.pullNumber)
            }
          />
        ) : (
          <DevelopmentPlaceholder item={item} />
        )}
      </div>
    </div>
  )
}

function ReviewRoleWorkspace({
  item,
  selectedCase,
  externalReviews: reviews,
  historyLoading,
  historyError,
  hasOlderHistory,
  onLoadOlderHistory,
  onRetryHistory,
  retainedFallbackActive,
  providerSnapshot,
  providerLoading,
  providerRefreshing,
  providerError,
  providerQueryKey,
  onRetryProvider,
  onCaseChange,
  onOpenReview,
}: {
  item: ReviewWorkItem
  selectedCase?: ReviewCase
  externalReviews: ReturnType<typeof externalPullReviews>
  historyLoading: boolean
  historyError: unknown
  hasOlderHistory: boolean
  onLoadOlderHistory: () => void
  onRetryHistory: () => void
  retainedFallbackActive: boolean
  providerSnapshot?: ReviewProviderSnapshot
  providerLoading: boolean
  providerRefreshing: boolean
  providerError: unknown
  providerQueryKey: readonly unknown[]
  onRetryProvider: () => void
  onCaseChange: (caseID: string) => void
  onOpenReview: (caseID: string) => void
}) {
  const activity = useMemo(
    () =>
      [
        ...item.reviewCases.map((reviewCase) => ({
          id: `picoclaw:${reviewCase.id}`,
          author: "PicoClaw",
          state: reviewCase.status,
          submittedAt: reviewCase.submitted_at ?? reviewCase.updated_at,
          url: reviewCase.pull_url,
          source: "PicoClaw case lifecycle" as const,
        })),
        ...(retainedFallbackActive
          ? reviews.map((review) => ({
              ...review,
              source: `GitHub · ${review.connector}`,
            }))
          : []),
      ].sort((left, right) =>
        right.submittedAt.localeCompare(left.submittedAt),
      ),
    [item.reviewCases, retainedFallbackActive, reviews],
  )
  return (
    <div className="grid gap-5">
      <ProviderReviewSnapshot
        key={selectedCase?.id ?? "no-review-case"}
        caseID={selectedCase?.id}
        snapshot={providerSnapshot}
        loading={providerLoading}
        refreshing={providerRefreshing}
        error={providerError}
        queryKey={providerQueryKey}
        onRetry={onRetryProvider}
      />
      <div className="grid gap-5 xl:grid-cols-[minmax(0,1fr)_21rem]">
        <section className="border-border bg-card overflow-hidden rounded-xl border shadow-sm">
          <div className="border-border flex items-center justify-between gap-3 border-b p-4">
            <div>
              <h2 className="font-semibold">PicoClaw review</h2>
              <p className="text-muted-foreground mt-0.5 text-xs">
                Findings prepared by this tool for the current review case.
              </p>
            </div>
            <Badge variant="outline">
              {selectedCase
                ? `${selectedCase.active_findings}/${selectedCase.total_findings} active`
                : "No case"}
            </Badge>
          </div>
          {selectedCase ? (
            <div className="p-4">
              {item.reviewCases.length > 1 ? (
                <fieldset
                  aria-describedby={`review-case-help-${item.key}`}
                  className="mb-4"
                >
                  <legend className="text-sm font-medium">Review cases</legend>
                  <p
                    id={`review-case-help-${item.key}`}
                    className="text-muted-foreground mt-1 text-xs"
                  >
                    Choose the captured case to view or edit. Editable cases are
                    selected first unless a case is saved in this page URL.
                  </p>
                  <div className="mt-3 grid gap-2">
                    {item.reviewCases.map((reviewCase, index) => (
                      <label
                        key={reviewCase.id}
                        className="block cursor-pointer"
                      >
                        <input
                          type="radio"
                          name={`review-case-${item.key}`}
                          value={reviewCase.id}
                          checked={reviewCase.id === selectedCase.id}
                          onChange={() => onCaseChange(reviewCase.id)}
                          className="peer sr-only"
                        />
                        <span className="border-border bg-background peer-checked:border-primary peer-checked:bg-primary/5 peer-focus-visible:ring-ring flex min-w-0 items-start justify-between gap-3 rounded-lg border p-3 transition-colors peer-focus-visible:ring-2 peer-focus-visible:ring-offset-2">
                          <span className="min-w-0">
                            <span className="block text-xs font-medium">
                              {index === 0
                                ? "Latest case"
                                : `Earlier case ${index + 1}`}
                            </span>
                            <span className="mt-1 block truncate text-sm">
                              {reviewCaseTitle(reviewCase)}
                            </span>
                            <span className="text-muted-foreground mt-1 block text-xs">
                              Updated {formatDate(reviewCase.updated_at)} ·{" "}
                              {reviewCase.active_findings}/
                              {reviewCase.total_findings} active
                            </span>
                          </span>
                          <ReviewCaseStatusBadge status={reviewCase.status} />
                        </span>
                      </label>
                    ))}
                  </div>
                </fieldset>
              ) : null}
              <p className="text-sm whitespace-pre-wrap">
                {selectedCase.summary}
              </p>
              {selectedCase.tests.length > 0 ? (
                <div className="bg-muted/40 mt-4 rounded-lg p-3">
                  <p className="text-xs font-medium">Tests considered</p>
                  <ul className="text-muted-foreground mt-2 grid gap-1 text-xs">
                    {selectedCase.tests.map((test) => (
                      <li key={test}>{test}</li>
                    ))}
                  </ul>
                </div>
              ) : null}
              <Button
                type="button"
                className="mt-4"
                onClick={() => onOpenReview(selectedCase.id)}
              >
                <IconShieldCheck />
                {isEditableReviewCase(selectedCase)
                  ? "Open review editor"
                  : "View review case"}
                <IconArrowRight />
              </Button>
              <p className="text-muted-foreground mt-3 text-xs">
                The editor lets you write or rephrase prepared comments,
                drop/restore draft findings, and submit the review.
                Provider-side thread controls, when the connector safely exposes
                them, are kept in the live snapshot above.
              </p>
            </div>
          ) : (
            <PortfolioNotice>No review case is available.</PortfolioNotice>
          )}
        </section>

        <section className="border-border bg-card overflow-hidden rounded-xl border shadow-sm">
          <div className="border-border border-b p-4">
            <h2 className="font-semibold">Observed review activity</h2>
            <p className="text-muted-foreground mt-0.5 text-xs">
              {retainedFallbackActive
                ? "Fallback: retained provider observations plus PicoClaw case lifecycle. This may not be complete provider history."
                : "Local PicoClaw case lifecycle. Live provider reviews and threads are shown separately above."}
            </p>
          </div>
          <div className="max-h-[36rem] overflow-auto p-3">
            {historyLoading && activity.length === 0 ? (
              <PortfolioNotice>Loading review activity…</PortfolioNotice>
            ) : activity.length === 0 ? (
              <PortfolioNotice>
                No provider review activity was captured.
              </PortfolioNotice>
            ) : (
              <ol className="grid gap-2">
                {activity.map((review) => (
                  <li
                    key={review.id}
                    className="border-border rounded-lg border p-3"
                  >
                    <div className="flex items-start justify-between gap-2">
                      <div className="min-w-0">
                        <p className="truncate text-sm font-medium">
                          @{review.author}
                        </p>
                        <p className="text-muted-foreground mt-1 text-xs">
                          {formatDate(review.submittedAt)}
                        </p>
                        <p className="text-muted-foreground mt-0.5 text-[10px]">
                          {review.source}
                        </p>
                      </div>
                      <ReviewActivityBadge state={review.state} />
                    </div>
                    {review.url ? (
                      <a
                        href={review.url}
                        target="_blank"
                        rel="noreferrer"
                        className="text-muted-foreground hover:text-foreground mt-2 inline-flex items-center gap-1 text-xs underline underline-offset-2"
                      >
                        View on GitHub <IconArrowRight className="size-3" />
                      </a>
                    ) : null}
                  </li>
                ))}
              </ol>
            )}
            {retainedFallbackActive && historyError ? (
              <p role="alert" className="text-destructive mt-3 text-xs">
                Retained review events could not be loaded.
              </p>
            ) : null}
            {retainedFallbackActive && (hasOlderHistory || historyError) ? (
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="mt-3 w-full"
                disabled={historyLoading}
                onClick={historyError ? onRetryHistory : onLoadOlderHistory}
              >
                {historyLoading
                  ? "Loading…"
                  : historyError
                    ? "Retry retained activity"
                    : "Load older observed activity"}
              </Button>
            ) : null}
          </div>
        </section>
      </div>
    </div>
  )
}

function ProviderReviewSnapshot({
  caseID,
  snapshot,
  loading,
  refreshing,
  error,
  queryKey,
  onRetry,
}: {
  caseID?: string
  snapshot?: ReviewProviderSnapshot
  loading: boolean
  refreshing: boolean
  error: unknown
  queryKey: readonly unknown[]
  onRetry: () => void
}) {
  const queryClient = useQueryClient()
  const [actionError, setActionError] = useState<string>()
  const mutation = useMutation({
    mutationFn: ({
      caseID: mutationCaseID,
      token,
      action,
    }: {
      caseID: string
      queryKey: readonly unknown[]
      token: string
      action: ReviewProviderThreadAction
    }) => mutateReviewProviderThread(mutationCaseID, token, action),
    retry: false,
    onMutate: () => setActionError(undefined),
    onSuccess: (updated, variables) => {
      queryClient.setQueryData(variables.queryKey, updated)
      void queryClient.invalidateQueries({ queryKey: variables.queryKey })
    },
    onError: (_error, variables) => {
      setActionError(
        "The thread may have changed on the provider. Refresh the live snapshot before trying again.",
      )
      void queryClient.invalidateQueries({ queryKey: variables.queryKey })
    },
  })

  if (caseID == null) return null

  return (
    <section
      aria-labelledby={`live-provider-review-${caseID}`}
      className="border-border bg-card overflow-hidden rounded-xl border shadow-sm"
    >
      <div className="border-border flex flex-wrap items-start justify-between gap-3 border-b p-4">
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <h2 id={`live-provider-review-${caseID}`} className="font-semibold">
              Live provider review
            </h2>
            {snapshot ? (
              <ProviderAvailabilityBadge availability={snapshot.availability} />
            ) : null}
          </div>
          <p className="text-muted-foreground mt-1 text-xs">
            Current pull request, reviews, and discussion threads fetched for
            the selected PicoClaw review case.
          </p>
        </div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={refreshing || mutation.isPending}
          onClick={onRetry}
        >
          <IconRefresh className={cn("size-4", refreshing && "animate-spin")} />
          {refreshing ? "Refreshing…" : "Refresh live data"}
        </Button>
      </div>

      {loading ? (
        <PortfolioNotice>Loading current provider review…</PortfolioNotice>
      ) : error ? (
        <ProviderSnapshotNotice
          title="Live provider review unavailable"
          body="The provider request failed. Retained activity below is available as a clearly labeled fallback."
          onRetry={onRetry}
        />
      ) : snapshot == null ? (
        <PortfolioNotice>No provider snapshot is available.</PortfolioNotice>
      ) : snapshot.availability === "unavailable" ? (
        <ProviderSnapshotNotice
          title="Connector unavailable"
          body="This review connector cannot currently read provider state. Retained activity below is the fallback."
          onRetry={onRetry}
        />
      ) : snapshot.availability === "incompatible" ? (
        <ProviderSnapshotNotice
          title="Connector response incompatible"
          body="The connector returned data that cannot be safely projected. Upgrade or reconfigure the connector; retained activity below is the fallback."
          onRetry={onRetry}
        />
      ) : (
        <div className="grid gap-4 p-4">
          {snapshot.pull_request ? (
            <LivePullRequestStatus pullRequest={snapshot.pull_request} />
          ) : (
            <p role="status" className="text-muted-foreground text-sm">
              Current pull request metadata was not returned.
            </p>
          )}
          {snapshot.availability === "partial" ||
          !snapshot.review_history_complete ||
          !snapshot.threads_complete ? (
            <ProviderPartialNotice snapshot={snapshot} />
          ) : null}

          <div className="grid gap-4 lg:grid-cols-2">
            <div>
              <h3 className="text-sm font-semibold">Provider reviews</h3>
              <p className="text-muted-foreground mt-1 text-xs">
                All reviews returned by {snapshot.connector} for this live read.
              </p>
              {snapshot.reviews.length === 0 ? (
                <p className="text-muted-foreground mt-3 text-sm">
                  No provider reviews were returned.
                </p>
              ) : (
                <ol className="mt-3 grid gap-2">
                  {snapshot.reviews.map((review) => (
                    <li
                      key={review.id}
                      className="border-border min-w-0 rounded-lg border p-3"
                    >
                      <div className="flex flex-wrap items-start justify-between gap-2">
                        <div className="min-w-0">
                          <p className="truncate text-sm font-medium">
                            {review.author
                              ? `@${review.author}`
                              : "Unknown reviewer"}
                          </p>
                          <p className="text-muted-foreground mt-1 text-xs">
                            {review.submitted_at
                              ? formatDate(review.submitted_at)
                              : "Submission time unavailable"}
                          </p>
                        </div>
                        <ReviewActivityBadge state={review.state} />
                      </div>
                      {review.body ? (
                        <p className="mt-3 text-sm whitespace-pre-wrap">
                          {review.body}
                        </p>
                      ) : null}
                      {review.url ? (
                        <a
                          href={review.url}
                          target="_blank"
                          rel="noreferrer"
                          className="text-muted-foreground hover:text-foreground mt-3 inline-flex items-center gap-1 text-xs underline underline-offset-2"
                        >
                          View review <IconArrowRight className="size-3" />
                        </a>
                      ) : null}
                    </li>
                  ))}
                </ol>
              )}
            </div>

            <div>
              <h3 className="text-sm font-semibold">Review threads</h3>
              <p className="text-muted-foreground mt-1 text-xs">
                Current comment and resolution state returned by the connector.
              </p>
              {snapshot.threads.length === 0 ? (
                <p className="text-muted-foreground mt-3 text-sm">
                  No review threads were returned.
                </p>
              ) : (
                <ol className="mt-3 grid gap-3">
                  {snapshot.threads.map((thread, index) => (
                    <ProviderReviewThread
                      key={thread.token ?? `provider-thread-${index}`}
                      thread={thread}
                      index={index}
                      threadResolutionAvailable={
                        snapshot.capabilities.thread_resolution
                      }
                      pending={mutation.isPending}
                      onAction={(token, action) =>
                        mutation.mutate({
                          caseID,
                          queryKey,
                          token,
                          action,
                        })
                      }
                    />
                  ))}
                </ol>
              )}
              {actionError ? (
                <p role="alert" className="text-destructive mt-3 text-xs">
                  {actionError}
                </p>
              ) : null}
            </div>
          </div>
        </div>
      )}
    </section>
  )
}

function LivePullRequestStatus({
  pullRequest,
}: {
  pullRequest: NonNullable<ReviewProviderSnapshot["pull_request"]>
}) {
  const state = pullRequest.merged
    ? "Merged"
    : pullRequest.state === "closed"
      ? "Closed"
      : pullRequest.draft
        ? "Open draft"
        : "Open"
  return (
    <div className="border-border bg-muted/30 flex flex-wrap items-center justify-between gap-3 rounded-lg border p-3">
      <div className="min-w-0">
        <p className="text-xs font-medium">Current pull request state</p>
        <p className="mt-1 truncate text-sm">
          #{pullRequest.number} {pullRequest.title}
          {pullRequest.author ? ` · @${pullRequest.author}` : ""}
        </p>
        {pullRequest.updated_at ? (
          <p className="text-muted-foreground mt-1 text-xs">
            Provider updated {formatDate(pullRequest.updated_at)}
          </p>
        ) : null}
      </div>
      <Badge
        variant={pullRequest.state === "closed" ? "secondary" : "outline"}
        className="shrink-0"
      >
        <IconGitPullRequest className="size-3" />
        {state}
      </Badge>
    </div>
  )
}

function ProviderReviewThread({
  thread,
  index,
  threadResolutionAvailable,
  pending,
  onAction,
}: {
  thread: ReviewProviderThread
  index: number
  threadResolutionAvailable: boolean
  pending: boolean
  onAction: (token: string, action: ReviewProviderThreadAction) => void
}) {
  const firstComment = thread.comments[0]
  const location = firstComment?.path
    ? `${firstComment.path}${firstComment.line ? `:${firstComment.line}` : ""}`
    : `Thread ${index + 1}`
  const canAct =
    threadResolutionAvailable && thread.can_resolve && thread.token != null
  return (
    <li className="border-border overflow-hidden rounded-lg border">
      <div className="bg-muted/30 flex flex-wrap items-center justify-between gap-2 px-3 py-2">
        <p className="min-w-0 truncate text-xs font-medium">{location}</p>
        <div className="flex flex-wrap items-center gap-1">
          <Badge variant={thread.is_resolved ? "secondary" : "outline"}>
            {thread.is_resolved ? "Resolved" : "Open"}
          </Badge>
          {thread.is_outdated ? (
            <Badge variant="outline">Outdated</Badge>
          ) : null}
          {thread.is_collapsed ? (
            <Badge variant="outline">Collapsed</Badge>
          ) : null}
        </div>
      </div>
      <ol className="divide-border divide-y">
        {thread.comments.map((comment, commentIndex) => (
          <li key={commentIndex} className="p-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <p className="text-xs font-medium">
                {comment.author ? `@${comment.author}` : "Unknown author"}
              </p>
              <p className="text-muted-foreground text-[11px]">
                {comment.created_at
                  ? formatDate(comment.created_at)
                  : "Time unavailable"}
              </p>
            </div>
            {comment.body ? (
              <p className="mt-2 text-sm whitespace-pre-wrap">{comment.body}</p>
            ) : null}
            {comment.url ? (
              <a
                href={comment.url}
                target="_blank"
                rel="noreferrer"
                className="text-muted-foreground hover:text-foreground mt-2 inline-flex text-xs underline underline-offset-2"
              >
                View comment
              </a>
            ) : null}
          </li>
        ))}
      </ol>
      {thread.total_count > thread.comments.length ? (
        <p className="text-muted-foreground border-border border-t px-3 py-2 text-xs">
          {thread.total_count - thread.comments.length} additional comment(s)
          were not returned.
        </p>
      ) : null}
      {canAct ? (
        <div className="border-border border-t p-2 text-right">
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={pending}
            onClick={() =>
              onAction(
                thread.token!,
                thread.is_resolved ? "unresolve" : "resolve",
              )
            }
          >
            {pending
              ? "Updating…"
              : thread.is_resolved
                ? "Reopen thread"
                : "Resolve thread"}
          </Button>
        </div>
      ) : null}
    </li>
  )
}

function ProviderAvailabilityBadge({
  availability,
}: {
  availability: ReviewProviderSnapshot["availability"]
}) {
  const labels: Record<ReviewProviderSnapshot["availability"], string> = {
    available: "Live",
    partial: "Live · incomplete",
    unavailable: "Unavailable",
    incompatible: "Incompatible",
  }
  return <Badge variant="outline">{labels[availability]}</Badge>
}

function ProviderPartialNotice({
  snapshot,
}: {
  snapshot: ReviewProviderSnapshot
}) {
  return (
    <div
      role="status"
      className="border-border rounded-lg border bg-amber-500/5 p-3 text-xs"
    >
      <p className="font-medium">Live provider data is incomplete</p>
      <p className="text-muted-foreground mt-1 leading-5">
        Showing the current data the connector returned. Review history or
        thread identity may require a connector upgrade; unavailable thread
        tokens never produce action controls.
      </p>
      {snapshot.limitations.length > 0 ? (
        <ul className="text-muted-foreground mt-2 list-disc space-y-1 pl-4">
          {snapshot.limitations.map((limitation) => (
            <li key={limitation}>{providerLimitationLabel(limitation)}</li>
          ))}
        </ul>
      ) : null}
    </div>
  )
}

function ProviderSnapshotNotice({
  title,
  body,
  onRetry,
}: {
  title: string
  body: string
  onRetry: () => void
}) {
  return (
    <div className="p-4">
      <div
        role="status"
        className="border-border bg-muted/30 rounded-lg border p-4"
      >
        <p className="text-sm font-medium">{title}</p>
        <p className="text-muted-foreground mt-1 text-xs leading-5">{body}</p>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="mt-3"
          onClick={onRetry}
        >
          Retry live provider
        </Button>
      </div>
    </div>
  )
}

function providerLimitationLabel(value: string): string {
  const labels: Record<string, string> = {
    review_history_pagination_stalled:
      "Review history pagination stalled; upgrade the GitHub connector to v1.0.5 or newer.",
    thread_identity_unavailable:
      "Thread identities are unavailable; upgrade the GitHub connector to v1.1.0 or newer for thread actions.",
  }
  return labels[value] ?? value.replaceAll("_", " ")
}

function DevelopmentPlaceholder({ item }: { item: ReviewWorkItem }) {
  const latest = item.developmentCases[0]
  return (
    <section className="border-border bg-card relative overflow-hidden rounded-xl border p-6 shadow-sm">
      <div className="bg-primary/5 absolute -top-16 -right-16 size-52 rounded-full blur-3xl" />
      <div className="relative max-w-2xl">
        <div className="bg-muted flex size-12 items-center justify-center rounded-xl">
          <IconCode className="size-6" />
        </div>
        <Badge variant="secondary" className="mt-5">
          Coming soon
        </Badge>
        <h2 className="mt-3 text-xl font-semibold">Development workspace</h2>
        <p className="text-muted-foreground mt-2 text-sm leading-6">
          PicoClaw knows this is a pull request it is responsible for coding.
          The full development experience is still being built; this placeholder
          preserves the role and the captured review context without presenting
          unfinished controls.
        </p>
        {latest ? (
          <div className="border-border bg-muted/30 mt-5 rounded-lg border p-4">
            <div className="flex flex-wrap items-center gap-2">
              <ReviewActivityBadge state={latest.current_review_state} />
              {latest.attention_required ? (
                <Badge variant="destructive">Needs action</Badge>
              ) : null}
            </div>
            <p className="mt-3 text-sm">
              Latest feedback from <strong>@{latest.review_author}</strong>
            </p>
            <p className="text-muted-foreground mt-1 text-xs">
              Captured {formatDate(latest.captured_at)} · {latest.head_ref}
            </p>
          </div>
        ) : null}
      </div>
    </section>
  )
}

function SummaryCard({
  label,
  value,
  icon: Icon,
  tone,
}: {
  label: string
  value: number
  icon: typeof IconClock
  tone: "amber" | "rose" | "emerald"
}) {
  const tones = {
    amber: "bg-amber-500/10 text-amber-700 dark:text-amber-300",
    rose: "bg-rose-500/10 text-rose-700 dark:text-rose-300",
    emerald: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-300",
  }
  return (
    <div className="border-border bg-card flex items-center gap-3 rounded-xl border p-4 shadow-sm">
      <div
        className={cn(
          "flex size-10 items-center justify-center rounded-lg",
          tones[tone],
        )}
      >
        <Icon className="size-5" />
      </div>
      <div>
        <p className="text-2xl font-semibold tabular-nums">{value}</p>
        <p className="text-muted-foreground text-xs">{label}</p>
      </div>
    </div>
  )
}

function Metric({
  label,
  value,
  urgent = false,
}: {
  label: string
  value: number
  urgent?: boolean
}) {
  return (
    <div
      className={cn(
        "bg-muted/50 rounded-lg px-3 py-2",
        urgent && "bg-rose-500/10",
      )}
    >
      <p
        className={cn(
          "text-lg font-semibold tabular-nums",
          urgent && "text-rose-700 dark:text-rose-300",
        )}
      >
        {value}
      </p>
      <p className="text-muted-foreground text-[11px]">{label}</p>
    </div>
  )
}

function RoleBadge({
  workRole,
  count,
}: {
  workRole: "review" | "develop"
  count?: number
}) {
  return (
    <Badge
      variant="outline"
      className={cn(
        "gap-1",
        workRole === "review"
          ? "border-blue-500/30 bg-blue-500/5 text-blue-700 dark:text-blue-300"
          : "border-violet-500/30 bg-violet-500/5 text-violet-700 dark:text-violet-300",
      )}
    >
      {workRole === "review" ? (
        <IconShieldCheck className="size-3" />
      ) : (
        <IconCode className="size-3" />
      )}
      {workRole === "review" ? "Reviewing" : "Developing"}
      {count === undefined ? null : (
        <span className="tabular-nums">{count}</span>
      )}
    </Badge>
  )
}

function StatusBadge({
  status,
  closureSource,
}: {
  status: "pending" | "complete" | "closed"
  closureSource?: ReviewWorkItem["closureSource"]
}) {
  return status === "pending" ? (
    <Badge
      variant="outline"
      className="w-fit border-amber-500/30 bg-amber-500/5 text-amber-700 dark:text-amber-300"
    >
      <IconClock className="size-3" />
      Pending
    </Badge>
  ) : status === "closed" ? (
    <Badge
      variant="outline"
      className="w-fit border-emerald-500/30 bg-emerald-500/5 text-emerald-700 dark:text-emerald-300"
    >
      <IconCheck className="size-3" />
      {closureSource === "live" ? "Closed" : "Captured closed"}
    </Badge>
  ) : (
    <Badge
      variant="outline"
      className="w-fit border-emerald-500/30 bg-emerald-500/5 text-emerald-700 dark:text-emerald-300"
    >
      <IconCheck className="size-3" />
      Review finished
    </Badge>
  )
}

function RoleTab({
  workRole,
  selected,
  onSelect,
}: {
  workRole: "review" | "develop"
  selected: boolean
  onSelect: () => void
}) {
  return (
    <button
      type="button"
      aria-current={selected ? "page" : undefined}
      onClick={onSelect}
      className={cn(
        "border-primary flex items-center gap-2 border-b-2 px-4 py-3 text-sm font-medium",
        !selected && "text-muted-foreground border-transparent",
      )}
    >
      {workRole === "review" ? (
        <IconShieldCheck className="size-4" />
      ) : (
        <IconCode className="size-4" />
      )}
      {workRole === "review" ? "Reviewing" : "Developing"}
    </button>
  )
}

function ReviewActivityBadge({ state }: { state: string }) {
  const normalized = state.toLowerCase()
  const positive = normalized === "approved"
  const negative = normalized === "changes_requested"
  return (
    <Badge
      variant={negative ? "destructive" : "outline"}
      className={cn(
        positive &&
          "border-emerald-500/30 bg-emerald-500/5 text-emerald-700 dark:text-emerald-300",
      )}
    >
      {positive ? (
        <IconCheck className="size-3" />
      ) : (
        <IconMessageCircle className="size-3" />
      )}
      {normalized.replaceAll("_", " ")}
    </Badge>
  )
}

function ReviewCaseStatusBadge({ status }: { status: ReviewCaseStatus }) {
  const destructive = status === "stale" || status === "submission_unknown"
  return (
    <Badge
      variant={destructive ? "destructive" : "outline"}
      className="w-fit shrink-0"
    >
      {reviewCaseStatusLabel(status)}
    </Badge>
  )
}

function isEditableReviewCase(reviewCase: ReviewCase): boolean {
  return reviewCase.status === "open" || reviewCase.status === "all_dropped"
}

function reviewCaseStatusLabel(status: ReviewCaseStatus): string {
  const labels: Record<ReviewCaseStatus, string> = {
    open: "Open",
    all_dropped: "All findings dropped",
    submitting: "Submitting",
    submission_unknown: "Outcome unknown",
    submitted: "Submitted",
    stale: "Stale",
  }
  return labels[status]
}

function reviewCaseTitle(reviewCase: ReviewCase): string {
  return (
    reviewCase.summary.split(/\r?\n/, 1)[0]?.trim() || "Untitled review case"
  )
}

function MissingSelection({
  title,
  body,
  onBack,
}: {
  title: string
  body: string
  onBack: () => void
}) {
  return (
    <div className="flex min-h-full items-center justify-center p-6">
      <div className="max-w-md text-center">
        <IconGitPullRequest className="text-muted-foreground mx-auto size-10" />
        <h1 className="mt-4 text-lg font-semibold">{title}</h1>
        <p className="text-muted-foreground mt-2 text-sm">{body}</p>
        <Button
          type="button"
          variant="outline"
          className="mt-5"
          onClick={onBack}
        >
          <IconArrowLeft />
          Back to repositories
        </Button>
      </div>
    </div>
  )
}

function PortfolioNotice({ children }: { children: React.ReactNode }) {
  return (
    <div className="text-muted-foreground flex min-h-28 flex-col items-center justify-center gap-3 p-5 text-center text-sm">
      {children}
    </div>
  )
}

function formatDate(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

function formatRelativeDate(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  const days = Math.floor((Date.now() - date.getTime()) / 86_400_000)
  if (days <= 0) return "Today"
  if (days === 1) return "Yesterday"
  if (days < 30) return `${days}d ago`
  return date.toLocaleDateString()
}
