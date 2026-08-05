import {
  IconArrowLeft,
  IconExternalLink,
  IconGitBranch,
  IconGitPullRequest,
  IconRefresh,
} from "@tabler/icons-react"
import {
  useInfiniteQuery,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"
import {
  type FormEvent,
  type UIEvent,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"
import { useTranslation } from "react-i18next"

import {
  type PRDevelopmentCase,
  type PRDevelopmentCasePage,
  type PRDevelopmentCaseSummary,
  type PRDevelopmentReviewState,
  getPRDevelopmentCase,
  listPRDevelopmentCases,
} from "@/api/pr-development"
import { PageHeader } from "@/components/page-header"
import { ReviewWorkbenchTabs } from "@/components/reviews/review-workbench-tabs"
import type { ReviewsRouteSearch } from "@/components/reviews/reviews-page"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { cn } from "@/lib/utils"

const DEVELOPMENT_PAGE_SIZE = 40
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
              "Read-only captured feedback",
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
              onSelect={(caseID) =>
                onSearchChange({ ...search, case: caseID }, false)
              }
              onRetry={() => void casesQuery.refetch()}
              onLoadMore={() => void casesQuery.fetchNextPage()}
            />
            <DevelopmentDetailPanel
              caseID={search.case}
              hiddenOnMobile={!search.case}
              onBack={() => {
                const next = { ...search }
                delete next.case
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
  onSelect: (caseID: string) => void
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
                  aria-current={selected ? "true" : undefined}
                  onClick={() => onSelect(developmentCase.id)}
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

function DevelopmentDetailPanel({
  caseID,
  hiddenOnMobile,
  onBack,
}: {
  caseID?: string
  hiddenOnMobile: boolean
  onBack: () => void
}) {
  const { t } = useTranslation()
  const detailQuery = useQuery({
    queryKey: ["pr-development", "detail", caseID],
    queryFn: () => getPRDevelopmentCase(caseID ?? ""),
    enabled: Boolean(caseID),
  })
  const developmentCase = detailQuery.data?.case

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
        ) : detailQuery.error ? (
          <DevelopmentError
            error={detailQuery.error}
            onRetry={() => void detailQuery.refetch()}
          />
        ) : developmentCase ? (
          <DevelopmentCaseDetail developmentCase={developmentCase} />
        ) : null}
      </div>
    </section>
  )
}

function DevelopmentCaseDetail({
  developmentCase,
}: {
  developmentCase: PRDevelopmentCase
}) {
  const { t } = useTranslation()
  const forked =
    developmentCase.head_repository.toLowerCase() !==
    developmentCase.base_repository.toLowerCase()

  return (
    <div className="mx-auto flex w-full max-w-4xl flex-col gap-4">
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
