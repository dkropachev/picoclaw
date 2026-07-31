import { useState } from "react"
import { useTranslation } from "react-i18next"

import { type ModelAlias, deleteModelAlias } from "@/api/models"
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

interface DeleteModelAliasDialogProps {
  alias: ModelAlias | null
  aliasIndex: number | null
  revision: string
  onClose: () => void
  onDeleted: () => void | Promise<void>
}

export function DeleteModelAliasDialog({
  alias,
  aliasIndex,
  revision,
  onClose,
  onDeleted,
}: DeleteModelAliasDialogProps) {
  const { t } = useTranslation()
  const [error, setError] = useState("")

  const remove = async () => {
    if (aliasIndex == null) return
    setError("")
    try {
      await deleteModelAlias(aliasIndex, revision)
      await onDeleted()
      onClose()
    } catch (deleteError) {
      setError(
        deleteError instanceof Error
          ? deleteError.message
          : t("models.alias.deleteError", "Failed to delete model alias."),
      )
    }
  }

  return (
    <AlertDialog
      open={alias != null}
      onOpenChange={(open) => {
        if (!open) {
          setError("")
          onClose()
        }
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {t("models.alias.deleteTitle", "Delete model alias?")}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {t(
              "models.alias.deleteDescription",
              '"{{name}}" will no longer be available to chats or workflows.',
              { name: alias?.name ?? "" },
            )}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <p role="alert" className="text-destructive text-sm">
            {error}
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            onClick={(event) => {
              event.preventDefault()
              void remove()
            }}
          >
            {t("common.delete")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
