import { IconLoader2, IconUpload, IconWorld } from "@tabler/icons-react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { type ChangeEvent, type DragEvent, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { importSkill } from "@/api/skills"
import { CollectionDetailShell } from "@/components/collection"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

const maximumImportFileSize = 1 << 20

export function SkillImportPage({
  onBack,
  onImported,
  onOpenMarketplace,
}: {
  onBack: () => void
  onImported: (id: string) => void
  onOpenMarketplace: () => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const inputRef = useRef<HTMLInputElement | null>(null)
  const dragDepthRef = useRef(0)
  const [dragActive, setDragActive] = useState(false)
  const mutation = useMutation({
    mutationFn: importSkill,
    onSuccess: async (skill) => {
      toast.success(t("pages.agent.skills.import_success"))
      await queryClient.invalidateQueries({ queryKey: ["skills"] })
      if (skill.id) onImported(skill.id)
      else onBack()
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : t("pages.agent.skills.import_error"),
      )
    },
  })

  const validateFile = (file: File): string | null => {
    const name = file.name.toLowerCase()
    const markdown =
      name.endsWith(".md") ||
      file.type === "text/markdown" ||
      file.type === "text/plain" ||
      file.type === ""
    const archive =
      name.endsWith(".zip") ||
      file.type === "application/zip" ||
      file.type === "application/x-zip-compressed"
    if (!markdown && !archive) {
      return t("pages.agent.skills.import_invalid_type")
    }
    if (file.size > maximumImportFileSize) {
      return t("pages.agent.skills.import_invalid_size")
    }
    return null
  }

  const importFile = (file: File) => {
    const validationError = validateFile(file)
    if (validationError) {
      toast.error(validationError)
      return
    }
    mutation.mutate(file)
  }

  const onFileChange = (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    if (file) importFile(file)
    event.target.value = ""
  }

  const onDragEnter = (event: DragEvent<HTMLElement>) => {
    event.preventDefault()
    dragDepthRef.current += 1
    setDragActive(true)
  }

  const onDragLeave = (event: DragEvent<HTMLElement>) => {
    event.preventDefault()
    dragDepthRef.current = Math.max(0, dragDepthRef.current - 1)
    if (dragDepthRef.current === 0) setDragActive(false)
  }

  const onDrop = (event: DragEvent<HTMLElement>) => {
    event.preventDefault()
    dragDepthRef.current = 0
    setDragActive(false)
    const file = event.dataTransfer.files?.[0]
    if (file) importFile(file)
  }

  return (
    <CollectionDetailShell
      title="Import skill"
      onBack={onBack}
      actions={
        <Button
          type="button"
          variant="outline"
          size="sm"
          aria-label="Browse marketplace"
          title="Browse marketplace"
          onClick={onOpenMarketplace}
        >
          <IconWorld />
          <span className="hidden sm:inline">Browse marketplace</span>
        </Button>
      }
    >
      <input
        ref={inputRef}
        type="file"
        accept=".md,.zip,text/markdown,text/plain,application/zip,application/x-zip-compressed"
        className="hidden"
        onChange={onFileChange}
      />
      <section className="mx-auto max-w-2xl space-y-5">
        <div>
          <h2 className="text-lg font-semibold">
            {t("pages.agent.skills.dropzone_title")}
          </h2>
          <p className="text-muted-foreground mt-1 text-sm">
            {t("pages.agent.skills.dropzone_description")}
          </p>
        </div>
        <button
          type="button"
          disabled={mutation.isPending}
          className={cn(
            "flex min-h-72 w-full cursor-pointer appearance-none flex-col items-center justify-center gap-3 rounded-xl border-2 border-dashed px-6 py-10 text-center transition-all disabled:cursor-not-allowed disabled:opacity-50",
            dragActive
              ? "border-primary bg-primary/10"
              : "border-border/60 bg-muted/30 hover:bg-muted/50 hover:border-primary/50",
          )}
          onClick={() => inputRef.current?.click()}
          onDragEnter={onDragEnter}
          onDragLeave={onDragLeave}
          onDragOver={(event) => event.preventDefault()}
          onDrop={onDrop}
        >
          <span className="bg-background text-muted-foreground ring-border/50 rounded-full p-3 shadow-sm ring-1">
            {mutation.isPending ? (
              <IconLoader2 className="size-6 animate-spin" />
            ) : (
              <IconUpload className="size-6" />
            )}
          </span>
          <span className="font-semibold">
            {dragActive
              ? t("pages.agent.skills.dropzone_active")
              : t("pages.agent.skills.dropzone_label")}
          </span>
          <span className="text-muted-foreground max-w-md text-sm">
            {t("pages.agent.skills.import_constraints")}
          </span>
          <span className="bg-primary text-primary-foreground mt-2 inline-flex h-9 items-center rounded-md px-4 text-sm font-medium">
            {mutation.isPending ? "Importing…" : t("pages.agent.skills.import")}
          </span>
        </button>
      </section>
    </CollectionDetailShell>
  )
}
