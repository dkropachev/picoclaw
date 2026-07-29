import { IconDeviceFloppy, IconLoader2 } from "@tabler/icons-react"
import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import { type MCPConfigResponse, type MCPDiscoverySettings } from "@/api/mcp"
import { Field, SwitchCardField } from "@/components/shared-form"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"

export function MCPSettingsSheet({
  open,
  config,
  saving,
  error,
  onOpenChange,
  onSave,
}: {
  open: boolean
  config: MCPConfigResponse
  saving: boolean
  error: string
  onOpenChange: (open: boolean) => void
  onSave: (settings: {
    enabled: boolean
    discovery: MCPDiscoverySettings
  }) => void
}) {
  const { t } = useTranslation()
  const [enabled, setEnabled] = useState(config.enabled)
  const [discovery, setDiscovery] = useState(config.discovery)
  const [validationError, setValidationError] = useState("")

  useEffect(() => {
    if (!open) return
    setEnabled(config.enabled)
    setDiscovery(config.discovery)
    setValidationError("")
  }, [config, open])

  const updateDiscovery = <K extends keyof MCPDiscoverySettings>(
    key: K,
    value: MCPDiscoverySettings[K],
  ) => {
    setDiscovery((current) => ({ ...current, [key]: value }))
    setValidationError("")
  }

  const handleSave = () => {
    if (
      discovery.enabled &&
      (!Number.isInteger(discovery.ttl) ||
        discovery.ttl < 1 ||
        !Number.isInteger(discovery.max_search_results) ||
        discovery.max_search_results < 1)
    ) {
      setValidationError(t("pages.agent.mcp.settings.positive_numbers"))
      return
    }
    if (discovery.enabled && !discovery.use_bm25 && !discovery.use_regex) {
      setValidationError(t("pages.agent.mcp.settings.search_method_required"))
      return
    }
    onSave({ enabled, discovery })
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="flex w-full flex-col gap-0 p-0 data-[side=right]:w-full data-[side=right]:sm:max-w-[500px]">
        <SheetHeader className="border-border/70 border-b px-6 py-5">
          <SheetTitle className="text-base">
            {t("pages.agent.mcp.settings.title")}
          </SheetTitle>
          <SheetDescription className="text-xs">
            {t("pages.agent.mcp.settings.description")}
          </SheetDescription>
        </SheetHeader>

        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-6 py-5">
          <SwitchCardField
            label={t("pages.agent.mcp.settings.enabled")}
            hint={t("pages.agent.mcp.settings.enabled_hint")}
            checked={enabled}
            onCheckedChange={setEnabled}
          />
          <SwitchCardField
            label={t("pages.agent.mcp.settings.discovery_enabled")}
            hint={t("pages.agent.mcp.settings.discovery_enabled_hint")}
            checked={discovery.enabled}
            onCheckedChange={(checked) => updateDiscovery("enabled", checked)}
            disabled={!enabled}
          />

          {enabled && discovery.enabled && (
            <>
              <Field
                label={t("pages.agent.mcp.settings.ttl")}
                hint={t("pages.agent.mcp.settings.ttl_hint")}
              >
                <Input
                  type="number"
                  min={1}
                  aria-label={t("pages.agent.mcp.settings.ttl")}
                  value={discovery.ttl}
                  onChange={(event) =>
                    updateDiscovery("ttl", Number(event.target.value))
                  }
                />
              </Field>
              <Field
                label={t("pages.agent.mcp.settings.max_results")}
                hint={t("pages.agent.mcp.settings.max_results_hint")}
              >
                <Input
                  type="number"
                  min={1}
                  aria-label={t("pages.agent.mcp.settings.max_results")}
                  value={discovery.max_search_results}
                  onChange={(event) =>
                    updateDiscovery(
                      "max_search_results",
                      Number(event.target.value),
                    )
                  }
                />
              </Field>
              <SwitchCardField
                label={t("pages.agent.mcp.settings.use_bm25")}
                hint={t("pages.agent.mcp.settings.use_bm25_hint")}
                checked={discovery.use_bm25}
                onCheckedChange={(checked) =>
                  updateDiscovery("use_bm25", checked)
                }
              />
              <SwitchCardField
                label={t("pages.agent.mcp.settings.use_regex")}
                hint={t("pages.agent.mcp.settings.use_regex_hint")}
                checked={discovery.use_regex}
                onCheckedChange={(checked) =>
                  updateDiscovery("use_regex", checked)
                }
              />
            </>
          )}

          {(validationError || error) && (
            <p className="text-destructive bg-destructive/10 rounded-lg px-3 py-2 text-sm">
              {validationError || error}
            </p>
          )}
        </div>

        <SheetFooter className="border-border/70 flex-col-reverse items-stretch border-t px-6 py-4 sm:flex-row sm:items-center sm:justify-end">
          <Button
            variant="ghost"
            onClick={() => onOpenChange(false)}
            disabled={saving}
          >
            {t("common.cancel")}
          </Button>
          <Button onClick={handleSave} disabled={saving}>
            {saving ? (
              <IconLoader2 className="size-4 animate-spin" />
            ) : (
              <IconDeviceFloppy className="size-4" />
            )}
            {t("common.save")}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
