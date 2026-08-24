import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewRepositoriesPage } from "@/components/repository-reviews/repository-review-repositories-page"

export const Route = createFileRoute("/repository-reviews_/repositories")({
  component: RepositoryReviewRepositoriesPage,
})
