import { createFileRoute } from "@tanstack/react-router"

import { DevelopmentIntakePage } from "@/components/development-workspaces/development-intake-page"

function NewDevelopmentRoutePage() {
  const navigate = Route.useNavigate()
  const search = Route.useSearch()
  return (
    <DevelopmentIntakePage
      initialIssueURL={search.issue}
      onBack={() => void navigate({ to: "/development" })}
      onCreated={(workspaceID) =>
        void navigate({
          to: "/development/$workspaceID",
          params: { workspaceID },
          search: { tab: "overview" },
          replace: true,
        })
      }
    />
  )
}

export const Route = createFileRoute("/development_/new")({
  validateSearch: normalizeNewDevelopmentSearch,
  component: NewDevelopmentRoutePage,
})

export function normalizeNewDevelopmentSearch(raw: Record<string, unknown>): {
  issue?: string
} {
  if (typeof raw.issue !== "string" || raw.issue.length > 4096) return {}
  const issue = raw.issue.trim()
  try {
    const parsed = new URL(issue)
    if (
      parsed.protocol !== "https:" ||
      parsed.username !== "" ||
      parsed.password !== "" ||
      !/\/issues\/[1-9][0-9]*\/?$/.test(parsed.pathname)
    ) {
      return {}
    }
    return { issue }
  } catch {
    return {}
  }
}
