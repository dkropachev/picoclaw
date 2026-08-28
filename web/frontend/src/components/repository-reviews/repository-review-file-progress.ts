import type { RepositoryReviewAutomation } from "@/api/repository-reviews"

export interface RepositoryReviewFileProgress {
  resolved: number
  total: number
  percent: number
}

export type RepositoryReviewFileProgressSource = Pick<
  RepositoryReviewAutomation,
  "status" | "progress" | "scope_plan" | "run_ids" | "started_at"
>

export function repositoryReviewFileProgress(
  review: RepositoryReviewFileProgressSource,
): RepositoryReviewFileProgress {
  const progress = review.progress
  const selected = count(review.scope_plan?.counts.selected_files)
  if (progress.scope_frozen === true && selected > 0) {
    const noFileEvidence =
      review.status !== "completed" &&
      count(progress.reviewed_files) === 0 &&
      count(progress.remaining_files) === 0 &&
      count(progress.unsupported_files) === 0
    const resolved =
      review.status === "completed"
        ? selected
        : noFileEvidence
          ? 0
          : clamp(selected - count(progress.remaining_files), 0, selected)
    return fileProgress(resolved, selected, review.status === "completed")
  }

  const resolved =
    count(progress.reviewed_files) + count(progress.unsupported_files)
  const total = Math.max(resolved + count(progress.remaining_files), resolved)
  return fileProgress(
    review.status === "completed" ? total : resolved,
    total,
    review.status === "completed",
  )
}

export function repositoryReviewFileProgressLabel(
  review: RepositoryReviewFileProgressSource,
): string {
  const progress = repositoryReviewFileProgress(review)
  return `${progress.resolved} of ${progress.total} files (${progress.percent}%)`
}

export function repositoryReviewInspectedFilesLabel(
  review: RepositoryReviewFileProgressSource,
): number | string {
  const inspected = count(review.progress.inspected_files)
  if (review.progress.coverage_available !== true) {
    const neverStarted =
      review.status === "idle" &&
      review.run_ids.length === 0 &&
      !review.started_at
    return neverStarted ? 0 : "Unknown"
  }
  if (review.progress.coverage_exact !== true) return `At least ${inspected}`
  return inspected
}

function fileProgress(
  resolved: number,
  total: number,
  completed: boolean,
): RepositoryReviewFileProgress {
  total = count(total)
  resolved = clamp(count(resolved), 0, total)
  const percent = completed
    ? 100
    : total > 0
      ? Math.round((resolved / total) * 100)
      : 0
  return { resolved, total, percent }
}

function count(value: number | undefined): number {
  return Number.isFinite(value) ? Math.max(0, Math.trunc(value ?? 0)) : 0
}

function clamp(value: number, minimum: number, maximum: number): number {
  return Math.min(maximum, Math.max(minimum, value))
}
