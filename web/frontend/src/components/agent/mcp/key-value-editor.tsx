import { IconPlus, IconTrash } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import { KeyInput } from "@/components/shared-form"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

import { type MCPKeyValueRow, newKeyValueRow } from "./mcp-server-form"

export function KeyValueEditor({
  rows,
  secretValues = true,
  onChange,
}: {
  rows: MCPKeyValueRow[]
  secretValues?: boolean
  onChange: (rows: MCPKeyValueRow[]) => void
}) {
  const { t } = useTranslation()

  const updateRow = (id: string, field: "key" | "value", value: string) => {
    onChange(
      rows.map((row) => (row.id === id ? { ...row, [field]: value } : row)),
    )
  }

  return (
    <div className="space-y-2">
      {rows.map((row) => (
        <div
          key={row.id}
          className="grid min-w-0 grid-cols-[minmax(0,1fr)_minmax(0,1.25fr)_auto] items-center gap-2"
        >
          <Input
            value={row.key}
            onChange={(event) => updateRow(row.id, "key", event.target.value)}
            placeholder={t("pages.agent.mcp.form.key_placeholder")}
            aria-label={t("pages.agent.mcp.form.key")}
            className="min-w-0 font-mono text-xs"
          />
          {secretValues ? (
            <KeyInput
              value={row.value}
              onChange={(value) => updateRow(row.id, "value", value)}
              ariaLabel={t("pages.agent.mcp.form.value")}
              placeholder={t("pages.agent.mcp.form.preserved_value")}
              className="min-w-0 font-mono text-xs"
            />
          ) : (
            <Input
              value={row.value}
              onChange={(event) =>
                updateRow(row.id, "value", event.target.value)
              }
              placeholder={t("pages.agent.mcp.form.value_placeholder")}
              aria-label={t("pages.agent.mcp.form.value")}
              className="min-w-0 font-mono text-xs"
            />
          )}
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            className="text-muted-foreground hover:text-destructive"
            onClick={() => onChange(rows.filter((item) => item.id !== row.id))}
            aria-label={t("pages.agent.mcp.form.remove_pair", {
              key: row.key || t("pages.agent.mcp.form.unnamed_pair"),
            })}
          >
            <IconTrash className="size-4" />
          </Button>
        </div>
      ))}
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => onChange([...rows, newKeyValueRow()])}
      >
        <IconPlus className="size-4" />
        {t("pages.agent.mcp.form.add_pair")}
      </Button>
    </div>
  )
}
