import { IconGitBranch, IconLoader2 } from "@tabler/icons-react"
import { useEffect, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type ModelInfo,
  type ModelRouterConfig,
  addModel,
  setDefaultModel,
  updateModel,
} from "@/api/models"
import { ConfigChangeNotice } from "@/components/config-change-notice"
import { Field, SwitchCardField } from "@/components/shared-form"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

interface ModelRouterSheetProps {
  open: boolean
  model: ModelInfo | null
  models: ModelInfo[]
  onClose: () => void
  onSaved: () => void
}

interface RouterForm {
  modelName: string
  defaultTarget: string
  mediaTarget: string
  codeTarget: string
  containsText: string
  containsTarget: string
  regexText: string
  regexTarget: string
}

const EMPTY_FORM: RouterForm = {
  modelName: "",
  defaultTarget: "",
  mediaTarget: "",
  codeTarget: "",
  containsText: "",
  containsTarget: "",
  regexText: "",
  regexTarget: "",
}

function isAccountRouterModel(model: ModelInfo): boolean {
  return model.provider === "router" || model.router != null
}

function isModelRouterModel(model: ModelInfo): boolean {
  return model.provider === "model-router" || model.model_router != null
}

function selectableTargets(models: ModelInfo[], currentName?: string) {
  return models.filter(
    (model) =>
      model.model_name !== currentName &&
      !isModelRouterModel(model) &&
      !model.model_name.startsWith("credential:") &&
      (model.is_virtual !== true || isAccountRouterModel(model)),
  )
}

function blockTarget(config: ModelRouterConfig | undefined, id: string) {
  return config?.blocks?.find((block) => block.id === id)?.model ?? ""
}

function parseRouterForm(model: ModelInfo | null): RouterForm {
  if (!model?.model_router) return EMPTY_FORM
  const entry = model.model_router.blocks?.find(
    (block) => block.id === model.model_router?.entry,
  )
  const next = { ...EMPTY_FORM, modelName: model.model_name }
  next.defaultTarget = blockTarget(model.model_router, entry?.fallback ?? "")
  for (const rule of entry?.rules ?? []) {
    if (rule.match === "has_media") {
      next.mediaTarget = blockTarget(model.model_router, rule.target)
    } else if (rule.match === "has_code") {
      next.codeTarget = blockTarget(model.model_router, rule.target)
    } else if (rule.match === "contains") {
      next.containsText = rule.value ?? ""
      next.containsTarget = blockTarget(model.model_router, rule.target)
    } else if (rule.match === "regex") {
      next.regexText = rule.value ?? ""
      next.regexTarget = blockTarget(model.model_router, rule.target)
    }
  }
  return next
}

function targetBlockID(prefix: string, target: string) {
  return `${prefix}-${
    target
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-|-$/g, "") || "target"
  }`
}

function buildRouterConfig(form: RouterForm): ModelRouterConfig {
  const rules: NonNullable<
    NonNullable<ModelRouterConfig["blocks"]>[number]["rules"]
  > = []
  const blocks: NonNullable<ModelRouterConfig["blocks"]> = []
  const addTarget = (prefix: string, target: string) => {
    const id = targetBlockID(prefix, target)
    if (!blocks.some((block) => block.id === id)) {
      blocks.push({ id, type: "model", model: target })
    }
    return id
  }
  if (form.mediaTarget) {
    rules.push({
      match: "has_media",
      target: addTarget("media", form.mediaTarget),
    })
  }
  if (form.codeTarget) {
    rules.push({
      match: "has_code",
      target: addTarget("code", form.codeTarget),
    })
  }
  if (form.containsText.trim() && form.containsTarget) {
    rules.push({
      match: "contains",
      value: form.containsText.trim(),
      target: addTarget("contains", form.containsTarget),
    })
  }
  if (form.regexText.trim() && form.regexTarget) {
    rules.push({
      match: "regex",
      value: form.regexText.trim(),
      target: addTarget("regex", form.regexTarget),
    })
  }
  const fallback = addTarget("default", form.defaultTarget)
  return {
    name: form.modelName.trim(),
    enabled: true,
    entry: "entry",
    blocks: [{ id: "entry", type: "rules", rules, fallback }, ...blocks],
  }
}

function TargetSelect({
  value,
  onValueChange,
  placeholder,
  targetOptions,
}: {
  value: string
  onValueChange: (value: string) => void
  placeholder: string
  targetOptions: ModelInfo[]
}) {
  return (
    <Select value={value} onValueChange={onValueChange}>
      <SelectTrigger>
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent>
        {targetOptions.map((target) => (
          <SelectItem key={target.model_name} value={target.model_name}>
            {target.model_name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

export function ModelRouterSheet({
  open,
  model,
  models,
  onClose,
  onSaved,
}: ModelRouterSheetProps) {
  const { t } = useTranslation()
  const [form, setForm] = useState<RouterForm>(EMPTY_FORM)
  const [setAsDefault, setSetAsDefault] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState("")
  const isEdit = model != null
  const targetOptions = useMemo(
    () => selectableTargets(models, model?.model_name),
    [model?.model_name, models],
  )
  const existingNames = useMemo(
    () =>
      new Set(
        models
          .filter((item) => item.index !== model?.index)
          .map((item) => item.model_name),
      ),
    [model?.index, models],
  )

  useEffect(() => {
    if (!open) return
    setForm(parseRouterForm(model))
    setSetAsDefault(model?.is_default === true)
    setSaving(false)
    setError("")
  }, [model, open])

  const update = <K extends keyof RouterForm>(key: K, value: RouterForm[K]) => {
    setForm((current) => ({ ...current, [key]: value }))
    if (error) setError("")
  }

  const validate = () => {
    const modelName = form.modelName.trim()
    if (!modelName) return t("models.modelRouter.errorNameRequired")
    if (existingNames.has(modelName))
      return t("models.modelRouter.errorDuplicate")
    if (!form.defaultTarget) return t("models.modelRouter.errorDefaultTarget")
    if (form.containsText.trim() && !form.containsTarget) {
      return t("models.modelRouter.errorContainsTarget")
    }
    if (form.regexText.trim() && !form.regexTarget) {
      return t("models.modelRouter.errorRegexTarget")
    }
    try {
      if (form.regexText.trim()) new RegExp(form.regexText.trim())
    } catch {
      return t("models.modelRouter.errorRegexInvalid")
    }
    return ""
  }

  const save = async () => {
    const validation = validate()
    if (validation) {
      setError(validation)
      return
    }
    setSaving(true)
    setError("")
    const router = buildRouterConfig(form)
    const payload = {
      model_name: form.modelName.trim(),
      provider: "model-router",
      model: form.modelName.trim(),
      enabled: true,
      model_router: router,
    }
    try {
      if (isEdit && model != null) {
        await updateModel(model.index, payload)
      } else {
        await addModel(payload)
      }
      if (setAsDefault) await setDefaultModel(form.modelName.trim())
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        t("models.modelRouter.saveSuccess"),
        form.modelName.trim(),
        gateway?.restartRequired === true,
      )
      onSaved()
      onClose()
    } catch (e) {
      setError(
        e instanceof Error ? e.message : t("models.modelRouter.saveError"),
      )
    } finally {
      setSaving(false)
    }
  }

  return (
    <Sheet open={open} onOpenChange={(next) => !next && onClose()}>
      <SheetContent className="flex w-full flex-col gap-0 overflow-hidden sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle className="flex items-center gap-2">
            <IconGitBranch className="size-4" />
            {isEdit
              ? t("models.modelRouter.editTitle", { name: model?.model_name })
              : t("models.modelRouter.title")}
          </SheetTitle>
          <SheetDescription>
            {t("models.modelRouter.description")}
          </SheetDescription>
        </SheetHeader>
        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-4 py-4">
          <ConfigChangeNotice kind="restart" title={t("models.restartHint")} />
          {error && (
            <div className="bg-destructive/10 text-destructive rounded px-3 py-2 text-sm">
              {error}
            </div>
          )}
          <Field label={t("models.modelRouter.routerName")}>
            <Input
              value={form.modelName}
              onChange={(event) => update("modelName", event.target.value)}
              placeholder={t("models.modelRouter.routerNamePlaceholder")}
              disabled={isEdit}
            />
          </Field>
          <Field label={t("models.modelRouter.defaultTarget")}>
            <TargetSelect
              value={form.defaultTarget}
              onValueChange={(value) => update("defaultTarget", value)}
              placeholder={t("models.modelRouter.selectTarget")}
              targetOptions={targetOptions}
            />
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("models.modelRouter.mediaTarget")}>
              <TargetSelect
                value={form.mediaTarget}
                onValueChange={(value) => update("mediaTarget", value)}
                placeholder={t("models.modelRouter.noRule")}
                targetOptions={targetOptions}
              />
            </Field>
            <Field label={t("models.modelRouter.codeTarget")}>
              <TargetSelect
                value={form.codeTarget}
                onValueChange={(value) => update("codeTarget", value)}
                placeholder={t("models.modelRouter.noRule")}
                targetOptions={targetOptions}
              />
            </Field>
          </div>
          <div className="grid gap-4 sm:grid-cols-[1fr_1fr]">
            <Field label={t("models.modelRouter.containsText")}>
              <Input
                value={form.containsText}
                onChange={(event) => update("containsText", event.target.value)}
                placeholder={t("models.modelRouter.containsPlaceholder")}
              />
            </Field>
            <Field label={t("models.modelRouter.containsTarget")}>
              <TargetSelect
                value={form.containsTarget}
                onValueChange={(value) => update("containsTarget", value)}
                placeholder={t("models.modelRouter.selectTarget")}
                targetOptions={targetOptions}
              />
            </Field>
          </div>
          <div className="grid gap-4 sm:grid-cols-[1fr_1fr]">
            <Field label={t("models.modelRouter.regexText")}>
              <Input
                value={form.regexText}
                onChange={(event) => update("regexText", event.target.value)}
                placeholder={t("models.modelRouter.regexPlaceholder")}
              />
            </Field>
            <Field label={t("models.modelRouter.regexTarget")}>
              <TargetSelect
                value={form.regexTarget}
                onValueChange={(value) => update("regexTarget", value)}
                placeholder={t("models.modelRouter.selectTarget")}
                targetOptions={targetOptions}
              />
            </Field>
          </div>
          <SwitchCardField
            label={t("models.defaultOnSave.label")}
            hint={t("models.defaultOnSave.description")}
            checked={setAsDefault}
            onCheckedChange={setSetAsDefault}
            ariaLabel={t("models.defaultOnSave.label")}
          />
        </div>
        <SheetFooter>
          <Button variant="outline" onClick={onClose} disabled={saving}>
            {t("common.cancel", "Cancel")}
          </Button>
          <Button onClick={() => void save()} disabled={saving}>
            {saving && <IconLoader2 className="size-4 animate-spin" />}
            {isEdit ? t("common.save", "Save") : t("models.modelRouter.create")}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
