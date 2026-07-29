import { useTranslation } from "react-i18next"

import type { EventView } from "@/api/events"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"

export function ReplayEventDialog({
  target,
  pending,
  error,
  onOpenChange,
  onConfirm,
}: {
  target: EventView | null
  pending: boolean
  error?: string
  onOpenChange: (open: boolean) => void
  onConfirm: () => void
}) {
  const { t } = useTranslation()

  return (
    <AlertDialog
      open={target != null}
      onOpenChange={(open) => {
        if (!pending) {
          onOpenChange(open)
        }
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {t("pages.events.replay.title", "Replay this event?")}
          </AlertDialogTitle>
          <AlertDialogDescription className="break-words">
            {t(
              "pages.events.replay.description",
              "Replaying {{id}} creates a new event and may repeat workflows, messages, or other external effects. It does not reset the source event.",
              { id: target?.id ?? "" },
            )}
          </AlertDialogDescription>
        </AlertDialogHeader>

        {error ? (
          <p role="alert" className="text-destructive text-sm break-words">
            {error}
          </p>
        ) : null}

        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>
            {t("common.cancel", "Cancel")}
          </AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={pending}
            onClick={(event) => {
              event.preventDefault()
              onConfirm()
            }}
          >
            {pending
              ? t("pages.events.replay.submitting", "Replaying…")
              : t("pages.events.replay.confirm", "Create replay")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
