import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewRunsPage } from "@/components/repository-reviews/repository-review-runs-page"

function RepositoryReviewsRoutePage() {
  return <RepositoryReviewRunsPage />
}

export const Route = createFileRoute("/repository-reviews")({
  component: RepositoryReviewsRoutePage,
})
