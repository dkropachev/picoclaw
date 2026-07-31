import { IconLoader2 } from "@tabler/icons-react"
import { useState } from "react"
import { useTranslation } from "react-i18next"

import { type ModelInfo, deleteModel } from "@/api/models"
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

interface DeleteModelDialogProps {
  model: ModelInfo | null
  revision: string
  onClose: () => void
  onDeleted: () => void
}

export function DeleteModelDialog({
  model,
  revision,
  onClose,
  onDeleted,
}: DeleteModelDialogProps) {
  const { t } = useTranslation()
  const [deleting, setDeleting] = useState(false)
  const [error, setError] = useState("")
  const isAccountRouter = model?.provider === "router" || Boolean(model?.router)
  const isModelRouter =
    model?.provider === "model-router" || Boolean(model?.model_router)

  const handleConfirm = async () => {
    if (!model) return
    if (model.is_default) {
      onClose()
      return
    }
    setDeleting(true)
    setError("")
    try {
      await deleteModel(model.index, revision)
      onDeleted()
      onClose()
    } catch (deleteError) {
      setError(
        deleteError instanceof Error
          ? deleteError.message
          : t("models.delete.error", "Failed to delete account."),
      )
    } finally {
      setDeleting(false)
    }
  }

  return (
    <AlertDialog
      open={model !== null}
      onOpenChange={(open) => {
        if (!open) {
          setError("")
          onClose()
        }
      }}
    >
      <AlertDialogContent size="sm">
        <AlertDialogHeader>
          <AlertDialogTitle>
            {isAccountRouter
              ? t("models.router.deleteTitle")
              : isModelRouter
                ? t("models.modelRouter.deleteTitle")
                : t("models.delete.title")}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {isAccountRouter
              ? t("models.router.deleteDescription", {
                  name: model?.model_name,
                })
              : isModelRouter
                ? t("models.modelRouter.deleteDescription", {
                    name: model?.model_name,
                  })
                : t("models.delete.description", { name: model?.model_name })}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <p role="alert" className="text-destructive text-sm">
            {error}
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel onClick={onClose} disabled={deleting}>
            {t("common.cancel")}
          </AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            onClick={handleConfirm}
            disabled={deleting}
          >
            {deleting && <IconLoader2 className="size-4 animate-spin" />}
            {t("models.delete.confirm")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
