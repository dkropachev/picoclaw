import { IconLoader2, IconPlus, IconTrash } from "@tabler/icons-react"
import { useEffect, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"

import { type ModelAlias, addModelAlias, updateModelAlias } from "@/api/models"
import { Field } from "@/components/shared-form"
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

interface OverrideRow {
  accountRef: string
  model: string
}

interface ModelAliasSheetProps {
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
  return Object.entries(alias?.account_overrides ?? {})
    .map(([accountRef, model]) => ({ accountRef, model }))
    .sort((a, b) => a.accountRef.localeCompare(b.accountRef))
}

export function ModelAliasSheet({
  open,
  alias,
  aliasIndex,
  nameLocked = false,
  revision,
  existingNames,
  concreteAccountRefs,
  onClose,
  onSaved,
}: ModelAliasSheetProps) {
  const { t } = useTranslation()
  const [name, setName] = useState("")
  const [model, setModel] = useState("")
  const [overrides, setOverrides] = useState<OverrideRow[]>([])
  const [error, setError] = useState("")
  const [saving, setSaving] = useState(false)
  const isEdit = alias != null && aliasIndex != null
  const lockName = isEdit || nameLocked

  useEffect(() => {
    if (!open) return
    setName(alias?.name ?? "")
    setModel(alias?.model ?? "")
    setOverrides(overrideRows(alias))
    setError("")
  }, [alias, open])

  const availableAccountRefs = useMemo(
    () =>
      concreteAccountRefs.filter(
        (accountRef) =>
          !overrides.some((override) => override.accountRef === accountRef),
      ),
    [concreteAccountRefs, overrides],
  )

  const addOverride = () => {
    const accountRef = availableAccountRefs[0]
    if (!accountRef) return
    setOverrides((current) => [...current, { accountRef, model: "" }])
  }

  const save = async () => {
    const trimmedName = name.trim()
    const trimmedModel = model.trim()
    if (!trimmedName || !trimmedModel) {
      setError(
        t(
          "models.alias.errorRequired",
          "Alias name and base model are required.",
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
        (override) => !override.accountRef.trim() || !override.model.trim(),
      )
    ) {
      setError(
        t(
          "models.alias.errorOverrideRequired",
          "Every account override needs an exact model.",
        ),
      )
      return
    }

    const accountOverrides = Object.fromEntries(
      overrides.map((override) => [
        override.accountRef.trim(),
        override.model.trim(),
      ]),
    )
    const payload: ModelAlias = {
      name: trimmedName,
      model: trimmedModel,
      ...(Object.keys(accountOverrides).length > 0
        ? { account_overrides: accountOverrides }
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
    <Sheet open={open} onOpenChange={(nextOpen) => !nextOpen && onClose()}>
      <SheetContent side="right" className="sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>
            {isEdit
              ? t("models.alias.editTitle", "Edit model alias")
              : nameLocked
                ? t("models.alias.configureTitle", "Configure model alias")
                : t("models.alias.addTitle", "Add model alias")}
          </SheetTitle>
          <SheetDescription>
            {t(
              "models.alias.description",
              "Chats and workflows use this alias. Each concrete account can map it to a different exact model.",
            )}
          </SheetDescription>
        </SheetHeader>

        <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-4 pb-4">
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
              placeholder={t("models.alias.namePlaceholder", "coding")}
            />
          </Field>

          <Field
            label={t("models.alias.baseModel", "Base model")}
            hint={t(
              "models.alias.baseModelHint",
              "Exact upstream model used unless the selected account has an override.",
            )}
            required
          >
            <Input
              value={model}
              onChange={(event) => setModel(event.target.value)}
              placeholder={t("models.alias.baseModelPlaceholder", "gpt-5.4")}
            />
          </Field>

          <section className="space-y-3">
            <div className="flex items-start justify-between gap-3">
              <div>
                <h3 className="text-sm font-medium">
                  {t("models.alias.overrides", "Account overrides")}
                </h3>
                <p className="text-muted-foreground mt-0.5 text-xs">
                  {t(
                    "models.alias.overridesHint",
                    "Overrides are allowed only for concrete accounts, never account routers.",
                  )}
                </p>
              </div>
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={addOverride}
                disabled={availableAccountRefs.length === 0}
              >
                <IconPlus className="size-4" />
                {t("models.alias.addOverride", "Add override")}
              </Button>
            </div>

            {overrides.length === 0 ? (
              <p className="border-border text-muted-foreground rounded-lg border border-dashed px-3 py-4 text-xs">
                {t(
                  "models.alias.noOverrides",
                  "All accounts currently use the base model.",
                )}
              </p>
            ) : (
              <div className="space-y-2">
                {overrides.map((override, index) => (
                  <div
                    key={`${override.accountRef}-${index}`}
                    className="border-border grid gap-2 rounded-lg border p-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]"
                  >
                    <Select
                      value={override.accountRef}
                      onValueChange={(accountRef) =>
                        setOverrides((current) =>
                          current.map((row, rowIndex) =>
                            rowIndex === index ? { ...row, accountRef } : row,
                          ),
                        )
                      }
                    >
                      <SelectTrigger
                        aria-label={t(
                          "models.alias.overrideAccount",
                          "Override account",
                        )}
                      >
                        <SelectValue />
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
                    <Input
                      value={override.model}
                      onChange={(event) =>
                        setOverrides((current) =>
                          current.map((row, rowIndex) =>
                            rowIndex === index
                              ? { ...row, model: event.target.value }
                              : row,
                          ),
                        )
                      }
                      aria-label={t(
                        "models.alias.overrideModel",
                        "Exact override model",
                      )}
                      placeholder={t(
                        "models.alias.overrideModelPlaceholder",
                        "claude-sonnet-4.5",
                      )}
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
                ))}
              </div>
            )}
          </section>
        </div>

        <SheetFooter className="border-border border-t">
          <Button type="button" variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button type="button" onClick={() => void save()} disabled={saving}>
            {saving && <IconLoader2 className="size-4 animate-spin" />}
            {t("common.save")}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
