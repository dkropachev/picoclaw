import { IconEye } from "@tabler/icons-react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import { getEventPayload } from "@/api/events"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"

import { eventErrorMessage, formatEventBytes } from "./event-format"
import { EventPanel, EventScrollRegion } from "./event-ui"

export function EventPayloadPanel({
  eventID,
  payloadBytes,
}: {
  eventID: string
  payloadBytes: number
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [requestedEventID, setRequestedEventID] = useState<string | null>(null)
  const payloadRequested = requestedEventID === eventID

  const payloadQuery = useQuery({
    queryKey: ["events", "payload", eventID],
    queryFn: ({ signal }) => getEventPayload(eventID, signal),
    enabled: payloadRequested,
    gcTime: 0,
    staleTime: 0,
    retry: false,
    refetchOnWindowFocus: false,
  })

  useEffect(() => {
    setRequestedEventID(null)

    return () => {
      void queryClient.cancelQueries({
        queryKey: ["events", "payload", eventID],
        exact: true,
      })
      queryClient.removeQueries({
        queryKey: ["events", "payload", eventID],
        exact: true,
      })
    }
  }, [eventID, queryClient])

  return (
    <EventPanel
      title={t("pages.events.payload.title", "Payload")}
      titleExtra={
        <Badge variant="outline" className="font-mono">
          {formatEventBytes(payloadBytes)}
        </Badge>
      }
    >
      {!payloadRequested ? (
        <div className="grid gap-3">
          <p className="text-muted-foreground text-sm">
            {t(
              "pages.events.payload.description",
              "Payload content is hidden until you explicitly request it.",
            )}
          </p>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="w-fit"
            onClick={() => setRequestedEventID(eventID)}
          >
            <IconEye className="size-4" />
            {t("pages.events.payload.load", "Load payload")}
          </Button>
        </div>
      ) : payloadQuery.isPending || payloadQuery.isFetching ? (
        <div
          role="status"
          className="text-muted-foreground flex min-h-20 items-center justify-center text-sm"
        >
          {t("pages.events.payload.loading", "Loading payload…")}
        </div>
      ) : payloadQuery.error ? (
        <div role="alert" className="grid gap-2">
          <p className="text-destructive text-sm break-words">
            {eventErrorMessage(
              payloadQuery.error,
              t(
                "pages.events.payload.error",
                "Failed to load the event payload.",
              ),
            )}
          </p>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="w-fit"
            onClick={() => void payloadQuery.refetch()}
          >
            {t("pages.events.payload.retry", "Retry payload")}
          </Button>
        </div>
      ) : (
        <EventScrollRegion
          label={t("pages.events.payload.region", "Raw event payload")}
          className="bg-muted/50 max-h-96 overflow-auto rounded-md p-3 font-mono text-xs"
        >
          <pre className="m-0 min-w-max whitespace-pre">
            {payloadQuery.data}
          </pre>
        </EventScrollRegion>
      )}
    </EventPanel>
  )
}
