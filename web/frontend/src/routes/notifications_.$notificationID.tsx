import { createFileRoute } from "@tanstack/react-router"

import { asDevelopmentAttentionPanel } from "@/components/development-workspaces/development-workspace-navigation"
import { NotificationInboxPage } from "@/components/notifications/notification-inbox-page"
import { normalizeNotificationRouteSearch } from "@/components/notifications/notification-query"

function NotificationDetailRoutePage() {
  const { notificationID } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <NotificationInboxPage
      initialQuery={search.query}
      selectedNotificationID={notificationID}
      onQueryChange={(query) =>
        void navigate({
          to: "/notifications/$notificationID",
          params: { notificationID },
          search: query ? { query } : {},
          replace: true,
        })
      }
      onNotificationChange={(nextID) =>
        void navigate({
          to: nextID ? "/notifications/$notificationID" : "/notifications",
          ...(nextID ? { params: { notificationID: nextID } } : {}),
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

export const Route = createFileRoute("/notifications_/$notificationID")({
  validateSearch: normalizeNotificationRouteSearch,
  component: NotificationDetailRoutePage,
})
