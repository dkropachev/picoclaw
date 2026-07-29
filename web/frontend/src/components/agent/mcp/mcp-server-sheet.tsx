import {
  IconAlertTriangle,
  IconCheck,
  IconDeviceFloppy,
  IconLoader2,
  IconLogin2,
  IconPlugConnected,
} from "@tabler/icons-react"
import { type FormEvent, useEffect, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type MCPProbeResponse,
  type MCPServer,
  addMCPServer,
  deleteMCPServerCredential,
  setMCPServerCredential,
  testMCPServer,
  updateMCPServer,
} from "@/api/mcp"
import {
  AdvancedSection,
  Field,
  KeyInput,
  SwitchCardField,
} from "@/components/shared-form"
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
import { Textarea } from "@/components/ui/textarea"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

import { KeyValueEditor } from "./key-value-editor"
import {
  type MCPServerDraft,
  type MCPServerFieldErrors,
  draftFromMCPServer,
  emptyMCPServerDraft,
  hasMCPServerOriginChanged,
  serverInputFromDraft,
  validateMCPServerDraft,
} from "./mcp-server-form"

export function MCPServerSheet({
  open,
  server,
  existingNames,
  onOpenChange,
  onSaved,
  onProbe,
  onOAuthLogin,
}: {
  open: boolean
  server: MCPServer | null
  existingNames: string[]
  onOpenChange: (open: boolean) => void
  onSaved: (name: string, previousName?: string) => Promise<void> | void
  onProbe: (name: string, result: MCPProbeResponse) => void
  onOAuthLogin: (
    name: string,
    preparedPopup?: Window | null,
  ) => Promise<boolean>
}) {
  const { t } = useTranslation()
  const [draft, setDraft] = useState<MCPServerDraft>(emptyMCPServerDraft)
  const [errors, setErrors] = useState<MCPServerFieldErrors>({})
  const [serverError, setServerError] = useState("")
  const [probeResult, setProbeResult] = useState<MCPProbeResponse | null>(null)
  const [savingMode, setSavingMode] = useState<"save" | "test" | "oauth" | "">(
    "",
  )
  const [persistedName, setPersistedName] = useState("")
  const [credentialAuthType, setCredentialAuthType] = useState("")

  useEffect(() => {
    if (!open) return
    setDraft(server ? draftFromMCPServer(server) : emptyMCPServerDraft())
    setErrors({})
    setServerError("")
    setProbeResult(null)
    setSavingMode("")
    setPersistedName(server?.name ?? "")
    const authType = server?.auth.type.toLocaleLowerCase() ?? ""
    setCredentialAuthType(
      server?.auth.configured === true &&
        (authType === "bearer" || authType === "oauth")
        ? authType
        : "",
    )
  }, [open, server])

  const isBusy = savingMode !== ""
  const credentialConfigured = credentialAuthType === "bearer"
  const remoteOriginChanged =
    server !== null &&
    server.type !== "stdio" &&
    draft.type !== "stdio" &&
    hasMCPServerOriginChanged(server.url, draft.url)
  const isEditing = persistedName !== ""
  const title = isEditing
    ? t("pages.agent.mcp.form.edit_title")
    : t("pages.agent.mcp.form.add_title")
  const description = isEditing
    ? t("pages.agent.mcp.form.edit_description")
    : t("pages.agent.mcp.form.add_description")

  const translatedErrors = useMemo(
    () =>
      Object.fromEntries(
        Object.entries(errors).map(([field, code]) => [
          field,
          t(`pages.agent.mcp.validation.${code}`),
        ]),
      ) as Partial<Record<keyof MCPServerFieldErrors, string>>,
    [errors, t],
  )

  const setField = <K extends keyof MCPServerDraft>(
    key: K,
    value: MCPServerDraft[K],
  ) => {
    setDraft((current) => ({ ...current, [key]: value }))
    setErrors((current) => ({ ...current, [key]: undefined }))
    setServerError("")
    setProbeResult(null)
  }

  const save = async (
    mode: "save" | "test" | "oauth",
    preparedPopup?: Window | null,
  ) => {
    const validation = validateMCPServerDraft(
      draft,
      existingNames,
      server,
      persistedName,
    )
    setErrors(validation)
    if (Object.keys(validation).length > 0) {
      preparedPopup?.close()
      return
    }

    setSavingMode(mode)
    setServerError("")
    setProbeResult(null)
    const input = serverInputFromDraft(draft)
    const previousPersistedName = persistedName
    let serverPersisted = false

    try {
      if (persistedName) {
        await updateMCPServer(persistedName, input)
      } else {
        await addMCPServer(input)
      }
      serverPersisted = true
      setPersistedName(input.name)

      if (draft.type !== "stdio") {
        if (draft.authMode === "bearer" && draft.token.trim()) {
          await setMCPServerCredential(input.name, draft.token.trim())
          setCredentialAuthType("bearer")
        } else if (
          draft.authMode !== credentialAuthType &&
          credentialAuthType !== ""
        ) {
          await deleteMCPServerCredential(input.name)
          setCredentialAuthType("")
        }
      } else if (credentialAuthType !== "") {
        await deleteMCPServerCredential(input.name)
        setCredentialAuthType("")
      }

      serverPersisted = false
      await onSaved(input.name, previousPersistedName || undefined)

      if (mode === "oauth") {
        const started = await onOAuthLogin(input.name, preparedPopup)
        if (started) {
          onOpenChange(false)
        } else {
          setServerError(
            t("pages.agent.mcp.oauth.start_failed", {
              name: input.name,
            }),
          )
        }
        return
      }

      if (mode === "save") {
        const gateway = await refreshGatewayState({ force: true })
        showSaveSuccessOrRestartToast(
          t,
          t("pages.agent.mcp.toast.saved", { name: input.name }),
          input.name,
          gateway?.restartRequired === true,
        )
        onOpenChange(false)
        return
      }

      const result = await testMCPServer(input, input.name)
      setProbeResult(result)
      onProbe(input.name, result)
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        result.ok
          ? t("pages.agent.mcp.toast.saved_and_connected", {
              name: input.name,
              count: result.tool_count,
            })
          : t("pages.agent.mcp.toast.saved", { name: input.name }),
        input.name,
        gateway?.restartRequired === true,
      )
      if (result.ok) {
        onOpenChange(false)
      }
    } catch (error) {
      preparedPopup?.close()
      if (serverPersisted) {
        try {
          await onSaved(input.name, previousPersistedName || undefined)
        } catch {
          // Preserve the mutation error below; the page can still be refreshed.
        }
      }
      setServerError(
        error instanceof Error
          ? error.message
          : t("pages.agent.mcp.toast.save_failed"),
      )
    } finally {
      setSavingMode("")
    }
  }

  const handleSubmit = (event: FormEvent) => {
    event.preventDefault()
    if (draft.authMode === "oauth" && draft.type !== "stdio") {
      const popup = window.open("", "_blank")
      void save("oauth", popup)
      return
    }
    void save("test")
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="flex w-full flex-col gap-0 p-0 data-[side=right]:w-full data-[side=right]:sm:max-w-[560px]">
        <SheetHeader className="border-border/70 border-b px-6 py-5">
          <SheetTitle className="text-base">{title}</SheetTitle>
          <SheetDescription className="text-xs">{description}</SheetDescription>
        </SheetHeader>

        <form className="flex min-h-0 flex-1 flex-col" onSubmit={handleSubmit}>
          <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
            <Field
              label={t("pages.agent.mcp.form.name")}
              hint={t("pages.agent.mcp.form.name_hint")}
              error={translatedErrors.name}
              required
            >
              <Input
                value={draft.name}
                onChange={(event) => setField("name", event.target.value)}
                placeholder={t("pages.agent.mcp.form.name_placeholder")}
                aria-label={t("pages.agent.mcp.form.name")}
                aria-required="true"
                aria-invalid={Boolean(errors.name)}
              />
            </Field>

            <Field
              label={t("pages.agent.mcp.form.transport")}
              hint={t("pages.agent.mcp.form.transport_hint")}
              required
            >
              <Select
                value={draft.type}
                onValueChange={(value) =>
                  setField("type", value as MCPServerDraft["type"])
                }
              >
                <SelectTrigger
                  className="w-full"
                  aria-label={t("pages.agent.mcp.form.transport")}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="http">
                    {t("pages.agent.mcp.transport.http")}
                  </SelectItem>
                  <SelectItem value="sse">
                    {t("pages.agent.mcp.transport.sse")}
                  </SelectItem>
                  <SelectItem value="stdio">
                    {t("pages.agent.mcp.transport.stdio")}
                  </SelectItem>
                </SelectContent>
              </Select>
            </Field>

            {draft.type === "stdio" ? (
              <>
                <Field
                  label={t("pages.agent.mcp.form.command")}
                  hint={t("pages.agent.mcp.form.command_hint")}
                  error={translatedErrors.command}
                  required
                >
                  <Input
                    value={draft.command}
                    onChange={(event) =>
                      setField("command", event.target.value)
                    }
                    placeholder="npx"
                    className="font-mono text-sm"
                    aria-label={t("pages.agent.mcp.form.command")}
                    aria-required="true"
                    aria-invalid={Boolean(errors.command)}
                  />
                </Field>
                <Field
                  label={t("pages.agent.mcp.form.arguments")}
                  hint={t("pages.agent.mcp.form.arguments_hint")}
                >
                  <Textarea
                    value={draft.argsText}
                    onChange={(event) =>
                      setField("argsText", event.target.value)
                    }
                    placeholder={"-y\n@modelcontextprotocol/server-filesystem"}
                    className="min-h-24 font-mono text-xs"
                    aria-label={t("pages.agent.mcp.form.arguments")}
                  />
                </Field>
              </>
            ) : (
              <>
                <Field
                  label={t("pages.agent.mcp.form.url")}
                  hint={t("pages.agent.mcp.form.url_hint")}
                  error={translatedErrors.url}
                  required
                >
                  <Input
                    value={draft.url}
                    onChange={(event) => setField("url", event.target.value)}
                    placeholder="https://example.com/mcp"
                    className="font-mono text-sm"
                    aria-label={t("pages.agent.mcp.form.url")}
                    aria-required="true"
                    aria-invalid={Boolean(errors.url)}
                  />
                </Field>
                {remoteOriginChanged && (
                  <div className="flex gap-2 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-3 text-xs text-amber-800 dark:text-amber-200">
                    <IconAlertTriangle className="mt-0.5 size-4 shrink-0" />
                    <p>{t("pages.agent.mcp.form.url_change_warning")}</p>
                  </div>
                )}
                <Field
                  label={t("pages.agent.mcp.form.authentication")}
                  hint={t("pages.agent.mcp.form.authentication_hint")}
                >
                  <Select
                    value={draft.authMode}
                    onValueChange={(value) =>
                      setField("authMode", value as MCPServerDraft["authMode"])
                    }
                  >
                    <SelectTrigger
                      className="w-full"
                      aria-label={t("pages.agent.mcp.form.authentication")}
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="none">
                        {t("pages.agent.mcp.auth.none")}
                      </SelectItem>
                      <SelectItem value="oauth">
                        {t("pages.agent.mcp.auth.oauth")}
                      </SelectItem>
                      <SelectItem value="bearer">
                        {t("pages.agent.mcp.auth.bearer")}
                      </SelectItem>
                      <SelectItem value="custom">
                        {t("pages.agent.mcp.auth.custom")}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                </Field>

                {draft.authMode === "bearer" && (
                  <Field
                    label={
                      credentialConfigured
                        ? t("pages.agent.mcp.form.replace_token")
                        : t("pages.agent.mcp.form.token")
                    }
                    hint={
                      credentialConfigured
                        ? t("pages.agent.mcp.form.token_preserved")
                        : t("pages.agent.mcp.form.token_hint")
                    }
                    error={translatedErrors.token}
                    required={!credentialConfigured}
                  >
                    <KeyInput
                      value={draft.token}
                      onChange={(value) => setField("token", value)}
                      ariaLabel={t("pages.agent.mcp.form.token")}
                      ariaRequired={!credentialConfigured}
                      placeholder={
                        credentialConfigured
                          ? t("pages.agent.mcp.form.preserved_value")
                          : t("pages.agent.mcp.form.token_placeholder")
                      }
                    />
                  </Field>
                )}

                {draft.authMode === "oauth" && (
                  <div className="border-border bg-muted/40 rounded-lg border px-3 py-3 text-xs">
                    <p className="font-medium">
                      {t("pages.agent.mcp.form.oauth_title")}
                    </p>
                    <p className="text-muted-foreground mt-1">
                      {credentialAuthType === "oauth" && !remoteOriginChanged
                        ? t("pages.agent.mcp.form.oauth_configured_hint")
                        : t("pages.agent.mcp.form.oauth_hint")}
                    </p>
                  </div>
                )}

                {draft.authMode === "custom" && (
                  <Field
                    label={t("pages.agent.mcp.form.headers")}
                    hint={t("pages.agent.mcp.form.headers_hint")}
                    error={translatedErrors.headers}
                  >
                    <KeyValueEditor
                      rows={draft.headerRows}
                      onChange={(rows) => setField("headerRows", rows)}
                    />
                  </Field>
                )}
              </>
            )}

            <AdvancedSection>
              <SwitchCardField
                label={t("pages.agent.mcp.form.enabled")}
                hint={t("pages.agent.mcp.form.enabled_hint")}
                checked={draft.enabled}
                onCheckedChange={(checked) => setField("enabled", checked)}
              />

              <Field
                label={t("pages.agent.mcp.form.discovery_mode")}
                hint={t("pages.agent.mcp.form.discovery_mode_hint")}
              >
                <Select
                  value={draft.discoveryMode}
                  onValueChange={(value) =>
                    setField(
                      "discoveryMode",
                      value as MCPServerDraft["discoveryMode"],
                    )
                  }
                >
                  <SelectTrigger
                    className="w-full"
                    aria-label={t("pages.agent.mcp.form.discovery_mode")}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="inherit">
                      {t("pages.agent.mcp.discovery.inherit")}
                    </SelectItem>
                    <SelectItem value="deferred">
                      {t("pages.agent.mcp.discovery.deferred")}
                    </SelectItem>
                    <SelectItem value="eager">
                      {t("pages.agent.mcp.discovery.eager")}
                    </SelectItem>
                  </SelectContent>
                </Select>
              </Field>

              {draft.type === "stdio" && (
                <>
                  <Field
                    label={t("pages.agent.mcp.form.env_file")}
                    hint={t("pages.agent.mcp.form.env_file_hint")}
                  >
                    <Input
                      value={draft.envFile}
                      onChange={(event) =>
                        setField("envFile", event.target.value)
                      }
                      placeholder="/absolute/path/.env"
                      className="font-mono text-xs"
                      aria-label={t("pages.agent.mcp.form.env_file")}
                    />
                  </Field>
                  <Field
                    label={t("pages.agent.mcp.form.environment")}
                    hint={t("pages.agent.mcp.form.environment_hint")}
                    error={translatedErrors.env}
                  >
                    <KeyValueEditor
                      rows={draft.envRows}
                      onChange={(rows) => setField("envRows", rows)}
                    />
                  </Field>
                </>
              )}
            </AdvancedSection>

            {probeResult && !probeResult.ok && (
              <div className="border-destructive/30 bg-destructive/10 rounded-lg border px-3 py-3 text-sm">
                <p className="text-destructive font-medium">
                  {t("pages.agent.mcp.probe.failed")}
                </p>
                <p className="text-muted-foreground mt-1 text-xs">
                  {probeResult.auth_required
                    ? t("pages.agent.mcp.probe.auth_required")
                    : (probeResult.error ??
                      t("pages.agent.mcp.probe.failed_unknown"))}
                </p>
              </div>
            )}

            {probeResult?.ok && (
              <div className="border-border bg-muted/40 rounded-lg border px-3 py-3 text-sm">
                <p className="flex items-center gap-2 font-medium">
                  <IconCheck className="size-4" />
                  {t("pages.agent.mcp.probe.connected", {
                    count: probeResult.tool_count,
                  })}
                </p>
              </div>
            )}

            {serverError && (
              <p className="text-destructive bg-destructive/10 rounded-lg px-3 py-2 text-sm">
                {serverError}
              </p>
            )}
          </div>

          <SheetFooter className="border-border/70 flex-col-reverse items-stretch border-t px-6 py-4 sm:flex-row sm:flex-wrap sm:items-center sm:justify-end">
            <Button
              type="button"
              variant="ghost"
              onClick={() => onOpenChange(false)}
              disabled={isBusy}
            >
              {t("common.cancel")}
            </Button>
            {draft.authMode !== "oauth" ||
            draft.type === "stdio" ||
            (credentialAuthType === "oauth" && !remoteOriginChanged) ? (
              <Button
                type="button"
                variant="outline"
                onClick={() => void save("save")}
                disabled={isBusy}
              >
                {savingMode === "save" ? (
                  <IconLoader2 className="size-4 animate-spin" />
                ) : (
                  <IconDeviceFloppy className="size-4" />
                )}
                {t("pages.agent.mcp.form.save")}
              </Button>
            ) : null}
            <Button type="submit" disabled={isBusy}>
              {savingMode === "test" || savingMode === "oauth" ? (
                <IconLoader2 className="size-4 animate-spin" />
              ) : draft.authMode === "oauth" && draft.type !== "stdio" ? (
                <IconLogin2 className="size-4" />
              ) : (
                <IconPlugConnected className="size-4" />
              )}
              {draft.authMode === "oauth" && draft.type !== "stdio"
                ? credentialAuthType === "oauth" && !remoteOriginChanged
                  ? t("pages.agent.mcp.form.save_and_reconnect")
                  : t("pages.agent.mcp.form.save_and_login")
                : t("pages.agent.mcp.form.save_and_test")}
            </Button>
          </SheetFooter>
        </form>
      </SheetContent>
    </Sheet>
  )
}
