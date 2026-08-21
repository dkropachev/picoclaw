import {
  IconChevronDown,
  IconLoader2,
  IconPlus,
  IconTrash,
} from "@tabler/icons-react"
import { useEffect, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type ModelAlias,
  addModelAlias,
  fetchUpstreamModels,
  updateModelAlias,
} from "@/api/models"
import { Field } from "@/components/shared-form"
import { Button } from "@/components/ui/button"
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
} from "@/components/ui/select"

const DISABLED_MODEL_VALUE = "__picoclaw_alias_disabled__"

interface OverrideRow {
  accountRef: string
  model: string
  disabled: boolean
}

interface ModelAvailability {
  id: string
  accountRefs: string[]
}

interface ModelAliasDialogProps {
  open: boolean
  alias: ModelAlias | null
  aliasIndex: number | null
  nameLocked?: boolean
  revision: string
  existingNames: string[]
  concreteAccountRefs: string[]
  onClose: () => void
  onSaved: () => void | Promise<void>
}

function overrideRows(alias: ModelAlias | null): OverrideRow[] {
  const rows = Object.entries(alias?.account_overrides ?? {}).map(
    ([accountRef, model]) => ({ accountRef, model, disabled: false }),
  )
  for (const accountRef of alias?.disabled_accounts ?? []) {
    rows.push({ accountRef, model: "", disabled: true })
  }
  return rows.sort((a, b) => a.accountRef.localeCompare(b.accountRef))
}

interface ModelSelectProps {
  value: string
  options: ModelAvailability[]
  allAccountRefs: string[]
  placeholder: string
  ariaLabel: string
  disabled?: boolean
  allowDisabled?: boolean
  disabledLabel: string
  onValueChange: (value: string) => void
}

function ModelSelect({
  value,
  options,
  allAccountRefs,
  placeholder,
  ariaLabel,
  disabled = false,
  allowDisabled = false,
  disabledLabel,
  onValueChange,
}: ModelSelectProps) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const displayValue = value === DISABLED_MODEL_VALUE ? disabledLabel : value

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="outline"
          role="combobox"
          aria-label={ariaLabel}
          aria-expanded={open}
          disabled={disabled}
          className="w-full min-w-0 justify-between font-normal"
        >
          <span
            className={
              displayValue ? "truncate" : "text-muted-foreground truncate"
            }
          >
            {displayValue || placeholder}
          </span>
          <IconChevronDown className="ml-2 size-4 shrink-0 opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent
        align="start"
        className="w-[--radix-popover-trigger-width] max-w-[min(42rem,90vw)] p-0"
      >
        <Command>
          <CommandInput
            placeholder={t("models.alias.searchModels", "Search models...")}
            onKeyDown={(event) => {
              if (event.key === "Escape") {
                event.preventDefault()
                setOpen(false)
              }
            }}
          />
          <CommandList>
            <CommandEmpty>
              {t("models.alias.noModelsFound", "No models found.")}
            </CommandEmpty>
            <CommandGroup>
              {allowDisabled && (
                <CommandItem
                  value={DISABLED_MODEL_VALUE}
                  keywords={[disabledLabel]}
                  onSelect={() => {
                    onValueChange(DISABLED_MODEL_VALUE)
                    setOpen(false)
                  }}
                >
                  <span className="text-destructive">{disabledLabel}</span>
                </CommandItem>
              )}
              {options.map((option) => {
                const available = new Set(option.accountRefs)
                const missing = allAccountRefs.filter(
                  (accountRef) => !available.has(accountRef),
                )
                const availability =
                  allAccountRefs.length === 0
                    ? t(
                        "models.alias.availabilityUnknown",
                        "Availability unknown",
                      )
                    : missing.length === 0
                      ? t(
                          "models.alias.availableAll",
                          "All accounts ({{count}})",
                          {
                            count: allAccountRefs.length,
                          },
                        )
                      : option.accountRefs.length === 0
                        ? t(
                            "models.alias.availableNone",
                            "Not reported by any account",
                          )
                        : t(
                            "models.alias.availableSome",
                            "Available: {{available}} · Missing: {{missing}}",
                            {
                              available: option.accountRefs.join(", "),
                              missing: missing.join(", "),
                            },
                          )
                return (
                  <CommandItem
                    key={option.id}
                    value={option.id}
                    keywords={[option.id]}
                    className="py-2"
                    onSelect={() => {
                      onValueChange(option.id)
                      setOpen(false)
                    }}
                  >
                    <span className="flex min-w-0 flex-col items-start gap-0.5">
                      <span className="max-w-[36rem] truncate font-mono">
                        {option.id}
                      </span>
                      <span className="text-muted-foreground max-w-[36rem] truncate text-[11px]">
                        {availability}
                      </span>
                    </span>
                  </CommandItem>
                )
              })}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}

export function ModelAliasDialog({
  open,
  alias,
  aliasIndex,
  nameLocked = false,
  revision,
  existingNames,
  concreteAccountRefs,
  onClose,
  onSaved,
}: ModelAliasDialogProps) {
  const { t } = useTranslation()
  const [name, setName] = useState("")
  const [model, setModel] = useState("")
  const [overrides, setOverrides] = useState<OverrideRow[]>([])
  const [availability, setAvailability] = useState<ModelAvailability[]>([])
  const [availabilityIssues, setAvailabilityIssues] = useState<string[]>([])
  const [loadingAvailability, setLoadingAvailability] = useState(false)
  const [error, setError] = useState("")
  const [saving, setSaving] = useState(false)
  const isEdit = alias != null && aliasIndex != null
  const lockName = isEdit || nameLocked
  const hasConcreteAccounts = concreteAccountRefs.length > 0

  useEffect(() => {
    if (!open) return
    setName(alias?.name ?? "")
    setModel(alias?.model ?? "")
    setOverrides(overrideRows(alias))
    setError("")
  }, [alias, open])

  useEffect(() => {
    if (!open) return
    let cancelled = false
    setLoadingAvailability(true)
    setAvailabilityIssues([])

    void Promise.all(
      concreteAccountRefs.map(async (accountRef) => {
        try {
          const response = await fetchUpstreamModels({
            account_ref: accountRef,
          })
          return {
            accountRef,
            models: response.models
              .map((item) => item.id.trim())
              .filter(Boolean),
            issue: response.issues?.map((item) => item.error).join("; ") ?? "",
          }
        } catch (fetchError) {
          return {
            accountRef,
            models: [] as string[],
            issue:
              fetchError instanceof Error
                ? fetchError.message
                : "Failed to load models",
          }
        }
      }),
    ).then((results) => {
      if (cancelled) return
      const accountsByModel = new Map<string, Set<string>>()
      for (const result of results) {
        for (const modelID of result.models) {
          const accounts = accountsByModel.get(modelID) ?? new Set<string>()
          accounts.add(result.accountRef)
          accountsByModel.set(modelID, accounts)
        }
      }
      const configuredModels = [
        alias?.model ?? "",
        ...Object.values(alias?.account_overrides ?? {}),
      ]
      for (const configuredModel of configuredModels) {
        const modelID = configuredModel.trim()
        if (modelID && !accountsByModel.has(modelID)) {
          accountsByModel.set(modelID, new Set())
        }
      }
      setAvailability(
        [...accountsByModel.entries()]
          .map(([id, accountRefs]) => ({
            id,
            accountRefs: [...accountRefs].sort((a, b) => a.localeCompare(b)),
          }))
          .sort((a, b) => a.id.localeCompare(b.id)),
      )
      setAvailabilityIssues(
        results
          .filter((result) => result.issue)
          .map((result) => `${result.accountRef}: ${result.issue}`),
      )
      setLoadingAvailability(false)
    })

    return () => {
      cancelled = true
    }
  }, [alias, concreteAccountRefs, open])

  const availableAccountRefs = useMemo(
    () =>
      concreteAccountRefs.filter(
        (accountRef) =>
          !overrides.some((override) => override.accountRef === accountRef),
      ),
    [concreteAccountRefs, overrides],
  )

  const modelOptions = useMemo(() => {
    if (!model || availability.some((item) => item.id === model)) {
      return availability
    }
    return [...availability, { id: model, accountRefs: [] }].sort((a, b) =>
      a.id.localeCompare(b.id),
    )
  }, [availability, model])

  const addOverride = () => {
    const accountRef = availableAccountRefs[0]
    if (!accountRef) return
    setOverrides((current) => [
      ...current,
      { accountRef, model: "", disabled: false },
    ])
  }

  const save = async () => {
    const trimmedName = name.trim()
    const trimmedModel = model.trim()
    if (!trimmedName || !trimmedModel) {
      setError(
        t(
          "models.alias.errorRequired",
          "Alias name and default model are required.",
        ),
      )
      return
    }
    if (
      !isEdit &&
      existingNames.some((existingName) => existingName.trim() === trimmedName)
    ) {
      setError(
        t("models.alias.errorDuplicate", "This model alias already exists."),
      )
      return
    }
    if (
      overrides.some(
        (override) =>
          !override.accountRef.trim() ||
          (!override.disabled && !override.model.trim()),
      )
    ) {
      setError(
        t(
          "models.alias.errorOverrideRequired",
          "Every account override needs a model or must be disabled.",
        ),
      )
      return
    }

    const accountOverrides = Object.fromEntries(
      overrides
        .filter((override) => !override.disabled)
        .map((override) => [override.accountRef.trim(), override.model.trim()]),
    )
    const disabledAccounts = overrides
      .filter((override) => override.disabled)
      .map((override) => override.accountRef.trim())
    const payload: ModelAlias = {
      name: trimmedName,
      model: trimmedModel,
      ...(Object.keys(accountOverrides).length > 0
        ? { account_overrides: accountOverrides }
        : {}),
      ...(disabledAccounts.length > 0
        ? { disabled_accounts: disabledAccounts }
        : {}),
    }

    setSaving(true)
    setError("")
    try {
      if (isEdit) {
        await updateModelAlias(aliasIndex, revision, payload)
      } else {
        await addModelAlias(payload)
      }
      await onSaved()
      onClose()
    } catch (saveError) {
      setError(
        saveError instanceof Error
          ? saveError.message
          : t("models.alias.saveError", "Failed to save model alias."),
      )
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(nextOpen) => !nextOpen && onClose()}>
      <DialogContent className="flex max-h-[calc(100vh-2rem)] flex-col gap-0 p-0 sm:max-w-3xl">
        <DialogHeader className="border-border border-b px-6 py-5 pr-14">
          <DialogTitle>
            {isEdit
              ? t("models.alias.editTitle", "Edit model alias")
              : nameLocked
                ? t("models.alias.configureTitle", "Configure model alias")
                : t("models.alias.addTitle", "Add model alias")}
          </DialogTitle>
          <DialogDescription>
            {t(
              "models.alias.description",
              "Chats and workflows use this alias. Each account can use the default model, an override, or disable the alias.",
            )}
          </DialogDescription>
        </DialogHeader>

        <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
          {error && (
            <p
              role="alert"
              className="bg-destructive/10 text-destructive rounded-lg px-3 py-2 text-sm"
            >
              {error}
            </p>
          )}

          <Field
            label={t("models.alias.name", "Alias")}
            hint={
              lockName
                ? t(
                    "models.alias.nameImmutable",
                    "This stable role name cannot be changed.",
                  )
                : t(
                    "models.alias.nameHint",
                    "Stable name referenced by chats, workflows, and agents.",
                  )
            }
            required
          >
            <Input
              value={name}
              onChange={(event) => setName(event.target.value)}
              disabled={lockName}
              placeholder={t("models.alias.namePlaceholder", "code")}
            />
          </Field>

          <Field
            label={t("models.alias.baseModel", "Default model")}
            hint={t(
              "models.alias.baseModelHint",
              "Used for accounts without an override. Each option shows where the model is available.",
            )}
            required
          >
            <ModelSelect
              value={model}
              options={modelOptions}
              allAccountRefs={concreteAccountRefs}
              disabled={loadingAvailability || !hasConcreteAccounts}
              placeholder={
                loadingAvailability
                  ? t("models.alias.loadingModels", "Loading models...")
                  : t("models.alias.selectModel", "Select a model")
              }
              ariaLabel={t("models.alias.baseModel", "Default model")}
              disabledLabel={t(
                "models.alias.disableForAccount",
                "Disabled for this account",
              )}
              onValueChange={setModel}
            />
          </Field>

          {!loadingAvailability && !hasConcreteAccounts && (
            <p
              role="status"
              className="border-border bg-muted text-muted-foreground rounded-lg border px-3 py-2 text-xs"
            >
              {t(
                "models.alias.noEnabledAccounts",
                "No enabled accounts are available. Add or restore one on the Accounts page before choosing models or overrides.",
              )}
            </p>
          )}

          {availabilityIssues.length > 0 && (
            <div className="bg-muted text-muted-foreground rounded-lg px-3 py-2 text-xs">
              <p className="text-foreground font-medium">
                {t(
                  "models.alias.partialAvailability",
                  "Some accounts did not return a model list",
                )}
              </p>
              {availabilityIssues.map((issue) => (
                <p key={issue} className="mt-1 break-words">
                  {issue}
                </p>
              ))}
            </div>
          )}

          <section className="space-y-3">
            <div className="flex items-start justify-between gap-3">
              <div>
                <h3 className="text-sm font-medium">
                  {t("models.alias.overrides", "Account overrides")}
                </h3>
                <p className="text-muted-foreground mt-0.5 text-xs">
                  {t(
                    "models.alias.overridesHint",
                    "Choose another model or disable this alias for a concrete account.",
                  )}
                </p>
              </div>
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={addOverride}
                disabled={
                  loadingAvailability || availableAccountRefs.length === 0
                }
              >
                <IconPlus className="size-4" />
                {t("models.alias.addOverride", "Add override")}
              </Button>
            </div>

            {overrides.length === 0 ? (
              <p className="border-border text-muted-foreground rounded-lg border border-dashed px-3 py-4 text-xs">
                {t(
                  "models.alias.noOverrides",
                  "Every account currently uses the default model.",
                )}
              </p>
            ) : (
              <div className="space-y-2">
                {overrides.map((override, index) => {
                  const selectedValue = override.disabled
                    ? DISABLED_MODEL_VALUE
                    : override.model
                  const rowOptions =
                    override.model &&
                    !availability.some((item) => item.id === override.model)
                      ? [
                          ...availability,
                          { id: override.model, accountRefs: [] },
                        ].sort((a, b) => a.id.localeCompare(b.id))
                      : availability
                  return (
                    <div
                      key={`${override.accountRef}-${index}`}
                      className="border-border grid gap-2 rounded-lg border p-3 sm:grid-cols-[minmax(0,0.75fr)_minmax(0,1.25fr)_auto]"
                    >
                      <Select
                        value={override.accountRef}
                        onValueChange={(accountRef) =>
                          setOverrides((current) =>
                            current.map((row, rowIndex) =>
                              rowIndex === index
                                ? {
                                    ...row,
                                    accountRef,
                                    model: row.disabled ? row.model : "",
                                  }
                                : row,
                            ),
                          )
                        }
                      >
                        <SelectTrigger
                          className="w-full min-w-0"
                          aria-label={t(
                            "models.alias.overrideAccount",
                            "Override account",
                          )}
                        >
                          <span className="truncate">
                            {override.accountRef}
                          </span>
                        </SelectTrigger>
                        <SelectContent>
                          {concreteAccountRefs
                            .filter(
                              (accountRef) =>
                                accountRef === override.accountRef ||
                                !overrides.some(
                                  (row) => row.accountRef === accountRef,
                                ),
                            )
                            .map((accountRef) => (
                              <SelectItem key={accountRef} value={accountRef}>
                                {accountRef}
                              </SelectItem>
                            ))}
                        </SelectContent>
                      </Select>
                      <ModelSelect
                        value={selectedValue}
                        options={rowOptions}
                        allAccountRefs={[override.accountRef]}
                        placeholder={t(
                          "models.alias.selectOverride",
                          "Select model or disable",
                        )}
                        ariaLabel={t(
                          "models.alias.overrideModel",
                          "Override model",
                        )}
                        allowDisabled
                        disabledLabel={t(
                          "models.alias.disableForAccount",
                          "Disabled for this account",
                        )}
                        onValueChange={(nextValue) =>
                          setOverrides((current) =>
                            current.map((row, rowIndex) =>
                              rowIndex === index
                                ? {
                                    ...row,
                                    disabled:
                                      nextValue === DISABLED_MODEL_VALUE,
                                    model:
                                      nextValue === DISABLED_MODEL_VALUE
                                        ? ""
                                        : nextValue,
                                  }
                                : row,
                            ),
                          )
                        }
                      />
                      <Button
                        type="button"
                        size="icon"
                        variant="ghost"
                        onClick={() =>
                          setOverrides((current) =>
                            current.filter(
                              (_row, rowIndex) => rowIndex !== index,
                            ),
                          )
                        }
                        aria-label={t(
                          "models.alias.removeOverride",
                          "Remove override",
                        )}
                        title={t(
                          "models.alias.removeOverride",
                          "Remove override",
                        )}
                      >
                        <IconTrash className="size-4" />
                      </Button>
                    </div>
                  )
                })}
              </div>
            )}
          </section>
        </div>

        <DialogFooter className="border-border border-t px-6 py-4">
          <Button type="button" variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button type="button" onClick={() => void save()} disabled={saving}>
            {saving && <IconLoader2 className="size-4 animate-spin" />}
            {t("common.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
