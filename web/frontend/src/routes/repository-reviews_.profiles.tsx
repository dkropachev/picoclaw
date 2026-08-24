import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewProfilesPage } from "@/components/repository-reviews/repository-review-profiles-page"

export const Route = createFileRoute("/repository-reviews_/profiles")({
  component: RepositoryReviewProfilesPage,
})
