import {
  IconArrowsShuffle,
  IconCheck,
  IconGitBranch,
  IconLoader2,
  IconRoute,
} from "@tabler/icons-react"
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

type RouterMode = "fallback" | "load_balance"
type RouterStrategy = "blind" | "tokens_spent" | "closest_limit"

interface RouterForm {
  modelName: string
  mode: RouterMode
  primaryAccount: string
  fallbackAccount: string
  lbAccounts: string[]
  strategy: RouterStrategy
  intervalSeconds: string
}

interface ModelRouterSheetProps {
  open: boolean
  model: ModelInfo | null
  models: ModelInfo[]
  onClose: () => void
  onSaved: () => void
}

const EMPTY_FORM: RouterForm = {
  modelName: "",
  mode: "fallback",
  primaryAccount: "",
  fallbackAccount: "",
  lbAccounts: [],
  strategy: "blind",
  intervalSeconds: "60",
}

function isRouterModel(model: ModelInfo): boolean {
  return model.provider === "router" || model.router != null
}

function selectableAccounts(models: ModelInfo[], currentName?: string) {
  return models.filter(
    (model) =>
      !isRouterModel(model) &&
      !model.is_virtual &&
      model.model_name !== currentName,
  )
}

function parseRouterForm(model: ModelInfo | null): RouterForm {
  if (!model?.router?.blocks?.length) {
    return EMPTY_FORM
  }
  const blocks = model.router.blocks
  const entry = blocks.find((block) => block.id === model.router?.entry)
  const fallbackBlock = entry?.fallback
    ? blocks.find((block) => block.id === entry.fallback)
    : undefined
  const fallbackAccount =
    fallbackBlock?.type === "account" ? (fallbackBlock.account ?? "") : ""

  if (entry?.type === "load_balance") {
    return {
      modelName: model.model_name,
      mode: "load_balance",
      primaryAccount: "",
      fallbackAccount,
      lbAccounts: entry.accounts ?? [],
      strategy: entry.strategy ?? "blind",
      intervalSeconds: String(
        entry.refresh_interval_seconds ??
          model.router.refresh_interval_seconds ??
          60,
      ),
    }
  }

  return {
    modelName: model.model_name,
    mode: "fallback",
    primaryAccount: entry?.account ?? "",
    fallbackAccount,
    lbAccounts: [],
    strategy: "blind",
    intervalSeconds: String(model.router.refresh_interval_seconds ?? 60),
  }
}

function buildRouterConfig(form: RouterForm): ModelRouterConfig {
  const interval = Number(form.intervalSeconds) || 60
  if (form.mode === "load_balance") {
    return {
      enabled: true,
      entry: "pool",
      refresh_interval_seconds: interval,
      blocks: [
        {
          id: "pool",
          type: "load_balance",
          accounts: form.lbAccounts,
          strategy: form.strategy,
          refresh_interval_seconds: interval,
          fallback: form.fallbackAccount ? "fallback" : undefined,
        },
        ...(form.fallbackAccount
          ? [
              {
                id: "fallback",
                type: "account" as const,
                account: form.fallbackAccount,
              },
            ]
          : []),
      ],
    }
  }

  return {
    enabled: true,
    entry: "primary",
    refresh_interval_seconds: interval,
    blocks: [
      {
        id: "primary",
        type: "account",
        account: form.primaryAccount,
        fallback: form.fallbackAccount ? "fallback" : undefined,
      },
      ...(form.fallbackAccount
        ? [
            {
              id: "fallback",
              type: "account" as const,
              account: form.fallbackAccount,
            },
          ]
        : []),
    ],
  }
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
  const accountOptions = useMemo(
    () => selectableAccounts(models, model?.model_name),
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
  const isEdit = model != null

  useEffect(() => {
    if (!open) return
    setForm(model ? parseRouterForm(model) : EMPTY_FORM)
    setSetAsDefault(model?.is_default === true)
    setSaving(false)
    setError("")
  }, [model, open])

  const update = <K extends keyof RouterForm>(key: K, value: RouterForm[K]) => {
    setForm((current) => ({ ...current, [key]: value }))
    if (error) setError("")
  }

  const toggleLoadBalanceAccount = (account: string) => {
    setForm((current) => {
      const exists = current.lbAccounts.includes(account)
      return {
        ...current,
        lbAccounts: exists
          ? current.lbAccounts.filter((item) => item !== account)
          : [...current.lbAccounts, account],
      }
    })
    if (error) setError("")
  }

  const validate = () => {
    const modelName = form.modelName.trim()
    if (!modelName) return t("models.router.errorNameRequired")
    if (existingNames.has(modelName)) return t("models.router.errorDuplicate")
    if (form.mode === "fallback" && !form.primaryAccount) {
      return t("models.router.errorPrimaryRequired")
    }
    if (
      form.mode === "fallback" &&
      form.fallbackAccount &&
      form.fallbackAccount === form.primaryAccount
    ) {
      return t("models.router.errorDistinctFallback")
    }
    if (form.mode === "load_balance" && form.lbAccounts.length === 0) {
      return t("models.router.errorPoolRequired")
    }
    if (
      form.mode === "load_balance" &&
      form.fallbackAccount &&
      form.lbAccounts.includes(form.fallbackAccount)
    ) {
      return t("models.router.errorDistinctFallback")
    }
    const interval = Number(form.intervalSeconds)
    if (!Number.isFinite(interval) || interval < 1) {
      return t("models.router.errorInterval")
    }
    return ""
  }

  const save = async () => {
    const validationError = validate()
    if (validationError) {
      setError(validationError)
      return
    }
    setSaving(true)
    setError("")
    try {
      const modelName = form.modelName.trim()
      const payload = {
        model_name: modelName,
        provider: "router",
        model: modelName,
        router: buildRouterConfig(form),
      }
      if (model) {
        await updateModel(model.index, payload)
      } else {
        await addModel(payload)
      }
      if (setAsDefault && !model?.is_default) {
        await setDefaultModel(modelName)
      }
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        isEdit ? t("models.router.saveSuccess") : t("models.router.addSuccess"),
        modelName,
        gateway?.restartRequired === true,
      )
      onSaved()
      onClose()
    } catch (err) {
      setError(
        err instanceof Error ? err.message : t("models.router.saveError"),
      )
    } finally {
      setSaving(false)
    }
  }

  return (
    <Sheet open={open} onOpenChange={(nextOpen) => !nextOpen && onClose()}>
      <SheetContent
        side="right"
        className="flex flex-col gap-0 p-0 data-[side=right]:!w-full data-[side=right]:sm:!w-[560px] data-[side=right]:sm:!max-w-[560px]"
      >
        <SheetHeader className="border-b-muted border-b px-6 py-5">
          <SheetTitle className="text-base">
            {isEdit
              ? t("models.router.editTitle", { name: model?.model_name })
              : t("models.router.title")}
          </SheetTitle>
          <SheetDescription className="text-xs">
            {t("models.router.description")}
          </SheetDescription>
        </SheetHeader>

        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="space-y-5 px-6 py-5">
            <Field
              label={t("models.add.modelName")}
              hint={t("models.router.nameHint")}
            >
              <Input
                value={form.modelName}
                onChange={(event) => update("modelName", event.target.value)}
                placeholder="router-main"
                disabled={isEdit}
                aria-label={t("models.add.modelName")}
              />
            </Field>

            <div className="grid grid-cols-2 gap-2">
              <Button
                type="button"
                variant={form.mode === "fallback" ? "default" : "outline"}
                onClick={() => update("mode", "fallback")}
                className="justify-start"
              >
                <IconGitBranch className="size-4" />
                {t("models.router.modeFallback")}
              </Button>
              <Button
                type="button"
                variant={form.mode === "load_balance" ? "default" : "outline"}
                onClick={() => update("mode", "load_balance")}
                className="justify-start"
              >
                <IconArrowsShuffle className="size-4" />
                {t("models.router.modeLoadBalance")}
              </Button>
            </div>

            {form.mode === "fallback" ? (
              <Field
                label={t("models.router.primaryAccount")}
                hint={t("models.router.primaryHint")}
                required
              >
                <AccountSelect
                  value={form.primaryAccount}
                  accounts={accountOptions}
                  placeholder={t("models.router.selectAccount")}
                  ariaLabel={t("models.router.primaryAccount")}
                  onChange={(value) => update("primaryAccount", value)}
                />
              </Field>
            ) : (
              <>
                <Field
                  label={t("models.router.poolAccounts")}
                  hint={t("models.router.poolHint")}
                  required
                >
                  <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                    {accountOptions.length === 0 && (
                      <p className="text-muted-foreground col-span-full text-sm">
                        {t("models.router.noAccounts")}
                      </p>
                    )}
                    {accountOptions.map((account) => {
                      const selected = form.lbAccounts.includes(
                        account.model_name,
                      )
                      return (
                        <button
                          key={account.index}
                          type="button"
                          onClick={() =>
                            toggleLoadBalanceAccount(account.model_name)
                          }
                          className={[
                            "border-border bg-background hover:bg-muted flex min-h-9 items-center justify-between rounded-lg border px-3 py-2 text-left text-sm",
                            selected ? "border-primary text-primary" : "",
                          ].join(" ")}
                        >
                          <span className="truncate">{account.model_name}</span>
                          {selected && <IconCheck className="size-4" />}
                        </button>
                      )
                    })}
                  </div>
                </Field>

                <Field
                  label={t("models.router.strategy")}
                  hint={t("models.router.strategyHint")}
                >
                  <Select
                    value={form.strategy}
                    onValueChange={(value) =>
                      update("strategy", value as RouterStrategy)
                    }
                  >
                    <SelectTrigger
                      className="w-full"
                      aria-label={t("models.router.strategy")}
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="blind">
                        {t("models.router.strategyBlind")}
                      </SelectItem>
                      <SelectItem value="tokens_spent">
                        {t("models.router.strategyTokens")}
                      </SelectItem>
                      <SelectItem value="closest_limit">
                        {t("models.router.strategyLimit")}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
              </>
            )}

            <Field
              label={t("models.router.fallbackAccount")}
              hint={t("models.router.fallbackHint")}
            >
              <AccountSelect
                value={form.fallbackAccount}
                accounts={accountOptions}
                placeholder={t("models.router.noFallback")}
                allowEmpty
                ariaLabel={t("models.router.fallbackAccount")}
                onChange={(value) => update("fallbackAccount", value)}
              />
            </Field>

            <Field
              label={t("models.router.interval")}
              hint={t("models.router.intervalHint")}
            >
              <Input
                type="number"
                min={1}
                value={form.intervalSeconds}
                onChange={(event) =>
                  update("intervalSeconds", event.target.value)
                }
                aria-label={t("models.router.interval")}
              />
            </Field>

            <SwitchCardField
              label={t("models.defaultOnSave.label")}
              hint={t("models.defaultOnSave.description")}
              checked={setAsDefault}
              onCheckedChange={setSetAsDefault}
            />

            {error && <p className="text-destructive text-sm">{error}</p>}
          </div>
        </div>

        <SheetFooter className="border-t-muted border-t px-6 py-4">
          <div className="flex w-full flex-col gap-3">
            <ConfigChangeNotice />
            <div className="flex justify-end gap-2">
              <Button variant="outline" onClick={onClose} disabled={saving}>
                {t("common.cancel")}
              </Button>
              <Button onClick={() => void save()} disabled={saving}>
                {saving ? (
                  <IconLoader2 className="size-4 animate-spin" />
                ) : (
                  <IconRoute className="size-4" />
                )}
                {isEdit ? t("common.save") : t("models.router.create")}
              </Button>
            </div>
          </div>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}

function AccountSelect({
  value,
  accounts,
  placeholder,
  allowEmpty,
  ariaLabel,
  onChange,
}: {
  value: string
  accounts: ModelInfo[]
  placeholder: string
  allowEmpty?: boolean
  ariaLabel: string
  onChange: (value: string) => void
}) {
  const { t } = useTranslation()
  return (
    <Select
      value={value || (allowEmpty ? "__none" : undefined)}
      onValueChange={(nextValue) =>
        onChange(nextValue === "__none" ? "" : nextValue)
      }
    >
      <SelectTrigger className="w-full" aria-label={ariaLabel}>
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent>
        {allowEmpty && (
          <SelectItem value="__none">
            {t("models.router.noFallback")}
          </SelectItem>
        )}
        {accounts.map((account) => (
          <SelectItem key={account.index} value={account.model_name}>
            {account.model_name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
