import { IconLoader2 } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import type { MCPServer } from "@/api/mcp"
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

export function DeleteMCPServerDialog({
  server,
  deleting,
  onOpenChange,
  onConfirm,
}: {
  server: MCPServer | null
  deleting: boolean
  onOpenChange: (open: boolean) => void
  onConfirm: () => void
}) {
  const { t } = useTranslation()

  return (
    <AlertDialog open={server !== null} onOpenChange={onOpenChange}>
      <AlertDialogContent size="sm">
        <AlertDialogHeader>
          <AlertDialogTitle>
            {t("pages.agent.mcp.delete.title")}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {t("pages.agent.mcp.delete.description", {
              name: server?.name ?? "",
            })}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={deleting}>
            {t("common.cancel")}
          </AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={deleting}
            onClick={onConfirm}
          >
            {deleting && <IconLoader2 className="size-4 animate-spin" />}
            {t("pages.agent.mcp.delete.confirm")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
