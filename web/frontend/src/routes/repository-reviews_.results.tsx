import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewsPage } from "@/components/repository-reviews/repository-reviews-page"

function RepositoryReviewResultsRoutePage() {
  const navigate = Route.useNavigate()
  return (
    <RepositoryReviewsPage
      onOpenThread={(threadID) =>
        void navigate({
          to: "/threads/open/$threadId",
          params: { threadId: threadID },
        })
      }
    />
  )
}

export const Route = createFileRoute("/repository-reviews_/results")({
  component: RepositoryReviewResultsRoutePage,
})
