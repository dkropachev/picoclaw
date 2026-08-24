import { createFileRoute } from "@tanstack/react-router"

import { asDevelopmentAttentionPanel } from "@/components/development-workspaces/development-workspace-navigation"
import { NotificationInboxPage } from "@/components/notifications/notification-inbox-page"
import { normalizeNotificationRouteSearch } from "@/components/notifications/notification-query"

function NotificationsRoutePage() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <NotificationInboxPage
      initialQuery={search.query}
      onQueryChange={(query) =>
        void navigate({
          to: "/notifications",
          search: query ? { query } : {},
          replace: true,
        })
      }
      onNotificationChange={(notificationID) =>
        void navigate({
          to: notificationID
            ? "/notifications/$notificationID"
            : "/notifications",
          ...(notificationID ? { params: { notificationID } } : {}),
          search,
        })
      }
      onOpenWorkspace={(workspaceID, target) =>
        void navigate({
          to: "/development/$workspaceID",
          params: { workspaceID },
          search: {
            tab: "overview",
            panel: asDevelopmentAttentionPanel(target.panel),
            ...(target.entity_id ? { entity: target.entity_id } : {}),
          },
        })
      }
    />
  )
}

export const Route = createFileRoute("/notifications")({
  validateSearch: normalizeNotificationRouteSearch,
  component: NotificationsRoutePage,
})
