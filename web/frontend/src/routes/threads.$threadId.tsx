import { Navigate, createFileRoute } from "@tanstack/react-router"

function LegacyThreadRoutePage() {
  const { threadId } = Route.useParams()
  return <Navigate to="/threads/open/$threadId" params={{ threadId }} replace />
}

export const Route = createFileRoute("/threads/$threadId")({
  component: LegacyThreadRoutePage,
})
