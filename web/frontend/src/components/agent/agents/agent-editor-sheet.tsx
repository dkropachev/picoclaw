import {
  IconAlertTriangle,
  IconDeviceFloppy,
  IconLoader2,
} from "@tabler/icons-react"
import { useBlocker } from "@tanstack/react-router"
import {
  type FormEvent,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"
import { useTranslation } from "react-i18next"

import {
  type AgentInfo,
  type AgentMutationInput,
  AgentsAPIError,
} from "@/api/agents"
import type { ModelAlias } from "@/api/models"
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
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
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

import {
  type AgentDraft,
  type AgentDraftErrors,
  agentDraftFromInfo,
  agentInputFromDraft,
  emptyAgentDraft,
  validateAgentDraft,
} from "./agent-form"
import { AgentTokenListField } from "./agent-token-list-field"

const INHERIT_ACCOUNT_VALUE = "__picoclaw_inherit_account__"

export interface AgentEditorSession {
  mode: "create" | "edit"
  agent: AgentInfo | null
  revision: string
}

export function AgentEditorSheet({
  session,
  agents,
  accountRefs,
  modelAliases,
  modelRouterNames,
  latestRevision,
  onSubmit,
  onConflict,
  onClose,
}: {
  session: AgentEditorSession | null
  agents: AgentInfo[]
  accountRefs: string[]
  modelAliases: ModelAlias[]
  modelRouterNames: string[]
  latestRevision: string
  onSubmit: (
    input: AgentMutationInput,
    expectedRevision: string,
    mode: "create" | "edit",
  ) => Promise<void>
  onConflict: () => Promise<void>
  onClose: () => void
}) {
  const { t } = useTranslation()
  const [draft, setDraft] = useState<AgentDraft>(emptyAgentDraft)
  const [initialDraft, setInitialDraft] = useState<AgentDraft>(emptyAgentDraft)
  const [revision, setRevision] = useState("")
  const [errors, setErrors] = useState<AgentDraftErrors>({})
  const [serverError, setServerError] = useState("")
  const [conflicted, setConflicted] = useState(false)
  const [conflictReloadAvailable, setConflictReloadAvailable] = useState(false)
  const [saving, setSaving] = useState(false)
  const [discardOpen, setDiscardOpen] = useState(false)
  const initializedSessionRef = useRef<AgentEditorSession | null>(null)
  const proceedingNavigationRef = useRef(false)

  useEffect(() => {
    if (session == null || initializedSessionRef.current === session) return
    initializedSessionRef.current = session
    const next =
      session.mode === "edit" && session.agent != null
        ? agentDraftFromInfo(session.agent)
        : emptyAgentDraft()
    setDraft(next)
    setInitialDraft(next)
    setRevision(session.revision)
    setErrors({})
    setServerError("")
    setConflicted(false)
    setConflictReloadAvailable(false)
    setSaving(false)
    setDiscardOpen(false)
  }, [session])

  useEffect(() => {
    if (session == null) initializedSessionRef.current = null
  }, [session])

  const dirty = JSON.stringify(draft) !== JSON.stringify(initialDraft)
  const shouldBlockNavigation = useCallback(
    () => session != null && dirty,
    [dirty, session],
  )
  const navigationBlocker = useBlocker({
    shouldBlockFn: shouldBlockNavigation,
    enableBeforeUnload: shouldBlockNavigation,
    disabled: session == null || !dirty,
    withResolver: true,
  })

  useEffect(() => {
    if (navigationBlocker.status === "blocked") {
      setDiscardOpen(true)
    }
  }, [navigationBlocker.status])

  const originalID =
    session?.mode === "edit" ? (session.agent?.id ?? "") : undefined
  const latestAgent =
    session?.mode === "edit"
      ? agents.find((agent) => agent.id === originalID)
      : undefined
  const peerIDs = useMemo(
    () => agents.map((agent) => agent.id).filter((id) => id !== draft.id),
    [agents, draft.id],
  )
  const modelAliasNames = useMemo(
    () => modelAliases.map((alias) => alias.name),
    [modelAliases],
  )
  const primaryModelNames = useMemo(
    () => [...new Set([...modelAliasNames, ...modelRouterNames])],
    [modelAliasNames, modelRouterNames],
  )
  const retainedUnknownAgentIDs =
    session?.mode === "edit"
      ? (session.agent?.subagents?.allow_agents.filter((id) => id !== "*") ??
        [])
      : []

  const update = <Key extends keyof AgentDraft>(
    key: Key,
    value: AgentDraft[Key],
  ) => {
    setDraft((current) => ({ ...current, [key]: value }))
    setErrors((current) => ({ ...current, [errorField(key)]: undefined }))
    setServerError("")
  }

  const requestClose = () => {
    if (saving) return
    if (dirty) {
      setDiscardOpen(true)
      return
    }
    onClose()
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (session == null || saving) return
    const validation = validateAgentDraft(
      draft,
      agents.map((agent) => agent.id),
      originalID,
      retainedUnknownAgentIDs,
    )
    setErrors(validation)
    if (Object.keys(validation).length > 0) return

    setSaving(true)
    setServerError("")
    try {
      await onSubmit(agentInputFromDraft(draft), revision, session.mode)
    } catch (error) {
      if (
        error instanceof AgentsAPIError &&
        error.status === 409 &&
        error.code === "config_revision_mismatch"
      ) {
        setConflicted(true)
        setConflictReloadAvailable(false)
        setServerError(
          t(
            "pages.agent.agents.conflict.description",
            "Agent configuration changed after this editor opened. Review the latest configuration before saving.",
          ),
        )
        try {
          await onConflict()
          setConflictReloadAvailable(true)
        } catch {
          setConflictReloadAvailable(false)
          setServerError(
            t(
              "pages.agent.agents.conflict.refresh_failed",
              "Configuration changed, and the latest revision could not be loaded. Close and retry.",
            ),
          )
        }
      } else {
        setServerError(
          error instanceof Error
            ? humanizeAPIMessage(error.message)
            : t(
                "pages.agent.agents.toast.save_failed",
                "Failed to save the agent.",
              ),
        )
      }
    } finally {
      setSaving(false)
    }
  }

  const reloadLatest = () => {
    if (session?.mode === "edit") {
      if (latestAgent == null) {
        setServerError(
          t(
            "pages.agent.agents.conflict.deleted",
            "This agent no longer exists. Close the editor and review the current list.",
          ),
        )
        return
      }
      const next = agentDraftFromInfo(latestAgent)
      setDraft(next)
      setInitialDraft(next)
    }
    setRevision(latestRevision)
    setErrors({})
    setServerError("")
    setConflicted(false)
    setConflictReloadAvailable(false)
  }

  const changeDiscardOpen = (open: boolean) => {
    if (!open && navigationBlocker.status === "blocked") {
      if (!proceedingNavigationRef.current) {
        navigationBlocker.reset()
      }
      proceedingNavigationRef.current = false
    }
    setDiscardOpen(open)
  }

  const discardChanges = () => {
    if (navigationBlocker.status === "blocked") {
      proceedingNavigationRef.current = true
      navigationBlocker.proceed()
    }
    onClose()
  }

  return (
    <>
      <Sheet
        open={session != null}
        onOpenChange={(open) => !open && requestClose()}
      >
        <SheetContent className="gap-0 data-[side=right]:!w-full data-[side=right]:sm:!max-w-xl">
          <SheetHeader className="border-border border-b pr-12">
            <SheetTitle>
              {session?.mode === "edit"
                ? t("pages.agent.agents.form.edit_title", "Edit agent")
                : t("pages.agent.agents.form.create_title", "Create agent")}
            </SheetTitle>
            <SheetDescription>
              {t(
                "pages.agent.agents.form.description",
                "Manage the configured policy used when PicoClaw starts this agent.",
              )}
            </SheetDescription>
          </SheetHeader>

          <form
            className="flex min-h-0 flex-1 flex-col"
            onSubmit={(event) => void submit(event)}
          >
            <div className="min-h-0 flex-1 space-y-6 overflow-y-auto p-4">
              <div className="border-border bg-muted/30 rounded-lg border p-3">
                <p className="text-sm font-medium">
                  {t(
                    "pages.agent.agents.configured_policy",
                    "Configured policy",
                  )}
                </p>
                <p className="text-muted-foreground mt-1 text-xs">
                  {t(
                    "pages.agent.agents.configured_policy_hint",
                    "An agent workspace AGENT.md file can override the configured name, model, and skills at runtime.",
                  )}
                </p>
              </div>

              <section className="space-y-4" aria-labelledby="agent-identity">
                <h3 id="agent-identity" className="text-sm font-semibold">
                  {t("pages.agent.agents.form.identity", "Identity")}
                </h3>
                <div className="space-y-2">
                  <Label htmlFor="agent-id">
                    {t("pages.agent.agents.form.id", "Agent ID")}
                  </Label>
                  <Input
                    id="agent-id"
                    value={draft.id}
                    disabled={session?.mode === "edit" || saving}
                    aria-invalid={Boolean(errors.id)}
                    autoComplete="off"
                    placeholder="reviewer"
                    onChange={(event) => update("id", event.target.value)}
                  />
                  <p className="text-muted-foreground text-xs">
                    {session?.mode === "edit"
                      ? t(
                          "pages.agent.agents.form.id_immutable",
                          "Agent IDs cannot be changed after creation.",
                        )
                      : t(
                          "pages.agent.agents.form.id_hint",
                          "Lowercase letters, numbers, underscores, and hyphens.",
                        )}
                  </p>
                  {errors.id && (
                    <p className="text-destructive text-xs">{errors.id}</p>
                  )}
                </div>
                <div className="space-y-2">
                  <Label htmlFor="agent-name">
                    {t("pages.agent.agents.form.name", "Configured name")}
                  </Label>
                  <Input
                    id="agent-name"
                    value={draft.name}
                    disabled={saving}
                    placeholder={draft.id || "Reviewer"}
                    onChange={(event) => update("name", event.target.value)}
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="agent-workspace">
                    {t("pages.agent.agents.form.workspace", "Workspace")}
                  </Label>
                  <Input
                    id="agent-workspace"
                    value={draft.workspace}
                    disabled={saving}
                    placeholder={t(
                      "pages.agent.agents.form.workspace_placeholder",
                      "Inherit the default workspace",
                    )}
                    onChange={(event) =>
                      update("workspace", event.target.value)
                    }
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="agent-account-ref">
                    {t("pages.agent.agents.form.account", "Provider account")}
                  </Label>
                  <Select
                    value={draft.accountRef || INHERIT_ACCOUNT_VALUE}
                    disabled={saving}
                    onValueChange={(value) =>
                      update(
                        "accountRef",
                        value === INHERIT_ACCOUNT_VALUE ? "" : value,
                      )
                    }
                  >
                    <SelectTrigger id="agent-account-ref" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={INHERIT_ACCOUNT_VALUE}>
                        {t(
                          "pages.agent.agents.form.account_inherit",
                          "Inherit default account",
                        )}
                      </SelectItem>
                      {draft.accountRef &&
                        !accountRefs.includes(draft.accountRef) && (
                          <SelectItem value={draft.accountRef} disabled>
                            {t(
                              "pages.agent.agents.form.not_configured",
                              "{{name}} (not configured)",
                              { name: draft.accountRef },
                            )}
                          </SelectItem>
                        )}
                      {accountRefs.map((accountRef) => (
                        <SelectItem key={accountRef} value={accountRef}>
                          {accountRef}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <p className="text-muted-foreground text-xs">
                    {t(
                      "pages.agent.agents.form.account_hint",
                      "Choose a concrete account or account router independently from the model alias.",
                    )}
                  </p>
                </div>
              </section>

              <section className="space-y-4" aria-labelledby="agent-model">
                <h3 id="agent-model" className="text-sm font-semibold">
                  {t("pages.agent.agents.form.model_policy", "Model policy")}
                </h3>
                <div className="grid gap-4 sm:grid-cols-2">
                  <div className="space-y-2">
                    <Label htmlFor="agent-primary-mode">
                      {t(
                        "pages.agent.agents.form.primary_policy",
                        "Primary model alias",
                      )}
                    </Label>
                    <Select
                      value={draft.primaryMode}
                      disabled={saving}
                      onValueChange={(value) => {
                        setDraft((current) => ({
                          ...current,
                          modelConfigured: true,
                          primaryMode: value as AgentDraft["primaryMode"],
                        }))
                        setErrors((current) => ({
                          ...current,
                          primary: undefined,
                        }))
                        setServerError("")
                      }}
                    >
                      <SelectTrigger id="agent-primary-mode" className="w-full">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="inherit">
                          {t("pages.agent.agents.policy.inherit", "Inherit")}
                        </SelectItem>
                        <SelectItem value="custom">
                          {t("pages.agent.agents.policy.custom", "Custom")}
                        </SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor="agent-fallback-mode">
                      {t(
                        "pages.agent.agents.form.fallback_policy",
                        "Fallback model aliases",
                      )}
                    </Label>
                    <Select
                      value={draft.fallbackMode}
                      disabled={saving}
                      onValueChange={(value) => {
                        setDraft((current) => ({
                          ...current,
                          modelConfigured: true,
                          fallbackMode: value as AgentDraft["fallbackMode"],
                        }))
                        setErrors((current) => ({
                          ...current,
                          fallbacks: undefined,
                        }))
                        setServerError("")
                      }}
                    >
                      <SelectTrigger
                        id="agent-fallback-mode"
                        className="w-full"
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="inherit">
                          {t("pages.agent.agents.policy.inherit", "Inherit")}
                        </SelectItem>
                        <SelectItem value="none">
                          {t("pages.agent.agents.policy.none", "None")}
                        </SelectItem>
                        <SelectItem value="custom">
                          {t("pages.agent.agents.policy.custom", "Custom")}
                        </SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                </div>
                {draft.primaryMode === "custom" && (
                  <div className="space-y-2">
                    <Label htmlFor="agent-primary">
                      {t(
                        "pages.agent.agents.form.primary_model",
                        "Primary model alias",
                      )}
                    </Label>
                    <Select
                      value={draft.primary}
                      disabled={saving}
                      onValueChange={(value) => update("primary", value)}
                    >
                      <SelectTrigger
                        id="agent-primary"
                        className="w-full"
                        aria-invalid={Boolean(errors.primary)}
                      >
                        <SelectValue
                          placeholder={t(
                            "pages.agent.agents.form.select_alias",
                            "Select model alias",
                          )}
                        />
                      </SelectTrigger>
                      <SelectContent>
                        {draft.primary &&
                          !primaryModelNames.includes(draft.primary) && (
                            <SelectItem value={draft.primary} disabled>
                              {t(
                                "pages.agent.agents.form.not_configured",
                                "{{name}} (not configured)",
                                { name: draft.primary },
                              )}
                            </SelectItem>
                          )}
                        {primaryModelNames.map((modelName) => (
                          <SelectItem key={modelName} value={modelName}>
                            {modelName}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    {errors.primary && (
                      <p className="text-destructive text-xs">
                        {errors.primary}
                      </p>
                    )}
                  </div>
                )}
                {draft.fallbackMode === "custom" && (
                  <AgentTokenListField
                    label={t(
                      "pages.agent.agents.form.fallback_order",
                      "Fallback order",
                    )}
                    description={t(
                      "pages.agent.agents.form.fallback_order_hint",
                      "Model aliases are tried from top to bottom.",
                    )}
                    values={draft.fallbacks}
                    input={draft.fallbackInput}
                    suggestions={modelAliasNames}
                    restrictToSuggestions
                    disabled={saving}
                    error={errors.fallbacks}
                    placeholder={t(
                      "pages.agent.agents.form.select_alias",
                      "Select model alias",
                    )}
                    onChange={(values) => update("fallbacks", values)}
                    onInputChange={(value) => update("fallbackInput", value)}
                  />
                )}
                {draft.modelConfigured && (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    disabled={saving}
                    onClick={() => {
                      setDraft((current) => ({
                        ...current,
                        modelConfigured: false,
                        primaryMode: "inherit",
                        primary: "",
                        fallbackMode: "inherit",
                        fallbacks: [],
                        fallbackInput: "",
                      }))
                      setErrors((current) => ({
                        ...current,
                        primary: undefined,
                        fallbacks: undefined,
                      }))
                      setServerError("")
                    }}
                  >
                    {t(
                      "pages.agent.agents.form.reset_model",
                      "Reset alias policy to inherited defaults",
                    )}
                  </Button>
                )}
              </section>

              <section className="space-y-4" aria-labelledby="agent-skills">
                <h3 id="agent-skills" className="text-sm font-semibold">
                  {t("pages.agent.agents.form.skills_policy", "Skills policy")}
                </h3>
                <div className="space-y-2">
                  <Label htmlFor="agent-skills-mode">
                    {t("pages.agent.agents.form.skills", "Available skills")}
                  </Label>
                  <Select
                    value={draft.skillsMode}
                    disabled={saving}
                    onValueChange={(value) =>
                      update("skillsMode", value as AgentDraft["skillsMode"])
                    }
                  >
                    <SelectTrigger id="agent-skills-mode" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="all">
                        {t(
                          "pages.agent.agents.policy.all_skills",
                          "All skills",
                        )}
                      </SelectItem>
                      <SelectItem value="selected">
                        {t(
                          "pages.agent.agents.policy.selected_skills",
                          "Selected skills",
                        )}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                {draft.skillsMode === "selected" && (
                  <AgentTokenListField
                    label={t(
                      "pages.agent.agents.form.selected_skills",
                      "Selected skills",
                    )}
                    values={draft.skills}
                    input={draft.skillsInput}
                    disabled={saving}
                    error={errors.skills}
                    placeholder="review-helper"
                    onChange={(values) => update("skills", values)}
                    onInputChange={(value) => update("skillsInput", value)}
                  />
                )}
              </section>

              <section className="space-y-4" aria-labelledby="agent-delegation">
                <h3 id="agent-delegation" className="text-sm font-semibold">
                  {t(
                    "pages.agent.agents.form.delegation_policy",
                    "Delegation policy",
                  )}
                </h3>
                <div className="space-y-2">
                  <Label htmlFor="agent-delegation-mode">
                    {t("pages.agent.agents.form.delegation", "May delegate to")}
                  </Label>
                  <Select
                    value={draft.delegationMode}
                    disabled={saving}
                    onValueChange={(value) =>
                      update(
                        "delegationMode",
                        value as AgentDraft["delegationMode"],
                      )
                    }
                  >
                    <SelectTrigger
                      id="agent-delegation-mode"
                      className="w-full"
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="none">
                        {t(
                          "pages.agent.agents.policy.no_delegation",
                          "No delegation",
                        )}
                      </SelectItem>
                      <SelectItem value="all">
                        {t("pages.agent.agents.policy.all_peers", "All peers")}
                      </SelectItem>
                      <SelectItem value="selected">
                        {t(
                          "pages.agent.agents.policy.selected_agents",
                          "Selected agents",
                        )}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                {draft.delegationMode === "selected" && (
                  <AgentTokenListField
                    label={t(
                      "pages.agent.agents.form.selected_agents",
                      "Selected agents",
                    )}
                    description={t(
                      "pages.agent.agents.form.unknown_agents_hint",
                      "Unknown configured IDs are retained until you remove them.",
                    )}
                    values={draft.delegateAgentIDs}
                    input={draft.delegateAgentInput}
                    suggestions={peerIDs}
                    disabled={saving}
                    error={errors.delegation}
                    placeholder="main"
                    onChange={(values) => update("delegateAgentIDs", values)}
                    onInputChange={(value) =>
                      update("delegateAgentInput", value)
                    }
                  />
                )}
              </section>

              {serverError && (
                <div
                  className="bg-destructive/10 text-destructive rounded-lg p-3 text-sm"
                  role="alert"
                >
                  <div className="flex items-start gap-2">
                    <IconAlertTriangle className="mt-0.5 size-4 shrink-0" />
                    <p>{serverError}</p>
                  </div>
                  {conflicted && conflictReloadAvailable && (
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      className="mt-3"
                      onClick={reloadLatest}
                    >
                      {t(
                        "pages.agent.agents.conflict.reload",
                        "Reload latest configuration",
                      )}
                    </Button>
                  )}
                </div>
              )}
            </div>

            <SheetFooter className="border-border bg-background border-t sm:flex-row sm:justify-end">
              <Button
                type="button"
                variant="outline"
                disabled={saving}
                onClick={requestClose}
              >
                {t("common.cancel", "Cancel")}
              </Button>
              <Button type="submit" disabled={saving || conflicted}>
                {saving ? (
                  <IconLoader2 className="size-4 animate-spin" />
                ) : (
                  <IconDeviceFloppy className="size-4" />
                )}
                {saving
                  ? t("common.saving", "Saving…")
                  : t("common.save", "Save")}
              </Button>
            </SheetFooter>
          </form>
        </SheetContent>
      </Sheet>

      <AlertDialog open={discardOpen} onOpenChange={changeDiscardOpen}>
        <AlertDialogContent size="sm">
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(
                "pages.agent.agents.discard.title",
                "Discard unsaved changes?",
              )}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(
                "pages.agent.agents.discard.description",
                "Your changes to this configured policy will be lost.",
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>
              {t("pages.agent.agents.discard.keep", "Keep editing")}
            </AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={discardChanges}>
              {t("pages.agent.agents.discard.confirm", "Discard changes")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

function errorField(key: keyof AgentDraft): keyof AgentDraftErrors {
  switch (key) {
    case "id":
      return "id"
    case "primary":
    case "primaryMode":
    case "modelConfigured":
      return "primary"
    case "fallbackMode":
    case "fallbacks":
    case "fallbackInput":
      return "fallbacks"
    case "skillsMode":
    case "skills":
    case "skillsInput":
      return "skills"
    case "delegationMode":
    case "delegateAgentIDs":
    case "delegateAgentInput":
      return "delegation"
    default:
      return "id"
  }
}

function humanizeAPIMessage(message: string): string {
  if (!/^[a-z0-9_]+$/.test(message)) return message
  const words = message.replaceAll("_", " ")
  return `${words.charAt(0).toLocaleUpperCase()}${words.slice(1)}.`
}
