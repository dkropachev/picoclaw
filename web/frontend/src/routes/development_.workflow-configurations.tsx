import { createFileRoute } from "@tanstack/react-router"

import type { PRLifecycleDecisionPoint } from "@/api/pr-lifecycle-flow"
import { PRLifecycleWorkflowConfigurationsPage } from "@/components/pr-workspaces/pr-lifecycle-workflow-configurations-page"

function normalizeSearch(raw: Record<string, unknown>) {
  const config =
    typeof raw.config === "string" && raw.config ? raw.config : undefined
  const flow: "review" | "implementation" =
    raw.flow === "implementation" ? "implementation" : "review"
  const gate =
    typeof raw.gate === "string" && /^pr\.[a-z0-9.-]+$/u.test(raw.gate)
      ? (raw.gate as PRLifecycleDecisionPoint)
      : undefined
  return { ...(config ? { config } : {}), flow, ...(gate ? { gate } : {}) }
}

function DevelopmentWorkflowConfigurationsRoutePage() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <PRLifecycleWorkflowConfigurationsPage
      page={search.config ? "config" : "configs"}
      initialConfigID={search.config}
      initialDecisionPoint={search.gate}
      activeFlowID={search.flow}
      onBack={() =>
        void navigate(
          search.config
            ? {
                to: "/development/workflow-configurations",
                search: { flow: search.flow },
              }
            : { to: "/development" },
        )
      }
      onConfigChange={(config) =>
        void navigate({
          to: "/development/workflow-configurations",
          search: config
            ? { config, flow: search.flow }
            : { flow: search.flow },
        })
      }
      onDecisionPointChange={(gate) =>
        void navigate({
          to: "/development/workflow-configurations",
          search: {
            ...(search.config ? { config: search.config } : {}),
            flow: search.flow,
            ...(gate ? { gate } : {}),
          },
        })
      }
      onFlowChange={(flow) =>
        void navigate({
          to: "/development/workflow-configurations",
          search: { ...(search.config ? { config: search.config } : {}), flow },
        })
      }
    />
  )
}

export const Route = createFileRoute("/development_/workflow-configurations")({
  validateSearch: normalizeSearch,
  component: DevelopmentWorkflowConfigurationsRoutePage,
})
