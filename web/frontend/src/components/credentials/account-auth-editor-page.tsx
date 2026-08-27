import { IconLoader2, IconPlus, IconRefresh } from "@tabler/icons-react"
import { useQuery } from "@tanstack/react-query"
import { type FormEvent, useEffect, useMemo, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import { type AccountSummary, getAccount } from "@/api/accounts"
import { CollectionAPIError } from "@/api/collection"
import type {
  OAuthMethod,
  OAuthProvider,
  OAuthProviderStatus,
} from "@/api/oauth"
import { CollectionDetailShell } from "@/components/collection"
import { Field, KeyInput } from "@/components/shared-form"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { useCredentialsPage } from "@/hooks/use-credentials-page"

import { getAccountRenewalMethod } from "./account-renewal"
import { DeviceCodeSheet } from "./device-code-sheet"

const accountNamePattern = /^[a-zA-Z0-9][a-zA-Z0-9._-]*$/

export function AccountAuthEditorPage({
  mode,
  id,
  onBack,
  onSaved,
}: {
  mode: "create" | "edit"
  id?: string
  onBack: () => void
  onSaved: (id?: string) => void
}) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ["account", id],
    queryFn: ({ signal }) => getAccount(id ?? "", signal),
    enabled: mode === "edit" && Boolean(id),
    retry: false,
  })
  const account = query.data?.account
  const credentials = useCredentialsPage()
  const providerOptions = useMemo(
    () =>
      credentials.providers
        .filter(
          (provider) => provider.provider && provider.provider !== "router",
        )
        .sort((left, right) =>
          left.display_name.localeCompare(right.display_name),
        ),
    [credentials.providers],
  )
  const registeredAccounts = useMemo(
    () =>
      credentials.providers.flatMap((provider) =>
        provider.credentials?.length ? provider.credentials : [],
      ),
    [credentials.providers],
  )
  const [provider, setProvider] = useState<OAuthProvider>("openai")
  const [method, setMethod] = useState<OAuthMethod>("browser")
  const [accountName, setAccountName] = useState("")
  const [token, setToken] = useState("")
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [submitting, setSubmitting] = useState(false)
  const completedRef = useRef(false)
  const renewalMode = mode === "edit"
  const renewalStatus = useMemo(
    () =>
      account
        ? oauthStatusForAccount(account, credentials.providers)
        : undefined,
    [account, credentials.providers],
  )
  const activeProvider = providerOptions.find(
    (candidate) => candidate.provider === provider,
  )
  const methods = useMemo(
    () =>
      renewalMode
        ? (renewalStatus?.methods ?? [])
        : (activeProvider?.methods ?? []),
    [activeProvider?.methods, renewalMode, renewalStatus?.methods],
  )

  useEffect(() => {
    if (!renewalStatus) return
    setProvider(renewalStatus.provider)
    setMethod(getAccountRenewalMethod(renewalStatus))
  }, [renewalStatus])

  useEffect(() => {
    if (renewalMode || methods.length === 0 || methods.includes(method)) return
    setMethod(methods[0] as OAuthMethod)
  }, [method, methods, renewalMode])

  useEffect(() => {
    if (credentials.credentialsRevision === 0 || completedRef.current) return
    completedRef.current = true
    onSaved(account?.id)
  }, [account?.id, credentials.credentialsRevision, onSaved])

  const validate = () => {
    const next: Record<string, string> = {}
    const name = accountName.trim()
    if (!renewalMode && method === "token" && !name) {
      next.accountName = t("accounts.onboarding.nameRequired")
    } else if (!renewalMode && name && name.toLowerCase() === provider) {
      next.accountName = t("accounts.onboarding.nameReserved")
    } else if (!renewalMode && name && !accountNamePattern.test(name)) {
      next.accountName = t("accounts.onboarding.nameInvalid")
    }
    const credentialID = name ? `${provider}:${name.toLowerCase()}` : ""
    if (
      !renewalMode &&
      credentialID &&
      registeredAccounts.some(
        (candidate) => candidate.credential_id === credentialID,
      )
    ) {
      next.accountName = t("accounts.onboarding.nameExists")
    }
    if (method === "token" && !token.trim()) {
      next.token = t("accounts.onboarding.tokenRequired")
    }
    setErrors(next)
    return Object.keys(next).length === 0
  }

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!validate()) return
    setSubmitting(true)
    credentials.clearError()
    try {
      const credentialID = renewalMode
        ? account?.account
        : accountName.trim() || undefined
      const ok =
        method === "browser"
          ? await credentials.startBrowserOAuth(provider, credentialID)
          : method === "device_code"
            ? await credentials.startOpenAIDeviceCode(credentialID, {
                openImmediately: true,
              })
            : await credentials.saveToken(provider, token.trim(), credentialID)
      if (ok && method === "token" && !completedRef.current) {
        completedRef.current = true
        onSaved(account?.id)
      }
    } finally {
      setSubmitting(false)
    }
  }

  const notFound =
    query.error instanceof CollectionAPIError && query.error.status === 404
  const loadError =
    query.error && !notFound
      ? query.error instanceof Error
        ? query.error.message
        : String(query.error)
      : undefined
  const pageLoading = (renewalMode && query.isLoading) || credentials.loading
  const title = renewalMode
    ? t("accounts.renewal.title")
    : t("accounts.onboarding.title")

  return (
    <>
      <CollectionDetailShell
        title={title}
        identity={
          account ? (
            <span className="font-mono text-xs">{account.account}</span>
          ) : undefined
        }
        loading={pageLoading}
        error={loadError}
        notFound={notFound}
        onBack={onBack}
        onRetry={() => {
          void query.refetch()
        }}
        backLabel="All accounts"
      >
        {!pageLoading && !loadError && !notFound && (
          <form className="space-y-5" onSubmit={submit}>
            <p className="text-muted-foreground text-sm">
              {renewalMode
                ? t("accounts.renewal.description")
                : t("accounts.onboarding.description")}
            </p>

            {credentials.error && (
              <div
                role="alert"
                aria-live="polite"
                className="text-destructive bg-destructive/10 rounded-lg px-3 py-2 text-sm"
              >
                {credentials.error}
              </div>
            )}

            {credentials.activeFlow && (
              <div
                role={
                  credentials.activeFlow.status === "error" ? "alert" : "status"
                }
                className="bg-muted flex flex-wrap items-center justify-between gap-3 rounded-lg border px-4 py-3 text-sm"
              >
                <span>{credentials.flowHint}</span>
                {credentials.activeFlow.status === "pending" &&
                  credentials.activeFlow.method === "browser" && (
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      onClick={credentials.stopLoading}
                    >
                      {t("credentials.actions.stopLoading")}
                    </Button>
                  )}
              </div>
            )}

            <div className="grid gap-5 sm:grid-cols-2">
              <Field label={t("accounts.fields.provider")} required>
                {renewalMode ? (
                  <Input
                    value={
                      renewalStatus?.display_name ?? providerLabel(provider)
                    }
                    aria-label={t("accounts.fields.provider")}
                    readOnly
                  />
                ) : (
                  <Select
                    value={provider}
                    onValueChange={(value) => {
                      setProvider(value)
                      setErrors({})
                    }}
                  >
                    <SelectTrigger
                      className="w-full"
                      aria-label={t("accounts.fields.provider")}
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {providerOptions.map((candidate) => (
                        <SelectItem
                          key={candidate.provider}
                          value={candidate.provider}
                        >
                          {candidate.display_name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              </Field>

              <Field label={t("accounts.fields.method")} required>
                {renewalMode ? (
                  <Input
                    value={methodLabel(method, t)}
                    aria-label={t("accounts.fields.method")}
                    readOnly
                  />
                ) : (
                  <Select
                    value={method}
                    onValueChange={(value) => {
                      setMethod(value as OAuthMethod)
                      setErrors({})
                    }}
                  >
                    <SelectTrigger
                      className="w-full"
                      aria-label={t("accounts.fields.method")}
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {methods.map((candidate) => (
                        <SelectItem key={candidate} value={candidate}>
                          {methodLabel(candidate, t)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              </Field>
            </div>

            {renewalMode ? (
              <Field label={t("accounts.fields.credentialID")}>
                <Input
                  value={account?.account ?? ""}
                  className="font-mono"
                  aria-label={t("accounts.fields.credentialID")}
                  readOnly
                />
              </Field>
            ) : (
              <Field
                label={t("accounts.fields.name")}
                hint={t("accounts.onboarding.nameHint")}
                error={errors.accountName}
              >
                <Input
                  value={accountName}
                  aria-label={t("accounts.fields.name")}
                  onChange={(event) => {
                    setAccountName(event.target.value)
                    if (errors.accountName) {
                      setErrors((current) => ({ ...current, accountName: "" }))
                    }
                  }}
                  placeholder={t("accounts.onboarding.namePlaceholder")}
                />
              </Field>
            )}

            {method === "token" && (
              <Field
                label={t("accounts.fields.token")}
                error={errors.token}
                required
              >
                <KeyInput
                  value={token}
                  ariaLabel={t("accounts.fields.token")}
                  ariaRequired
                  onChange={(value) => {
                    setToken(value)
                    if (errors.token) {
                      setErrors((current) => ({ ...current, token: "" }))
                    }
                  }}
                  placeholder={tokenPlaceholder(provider, t)}
                />
              </Field>
            )}

            <div className="border-border flex justify-end border-t pt-4">
              <Button
                type="submit"
                disabled={
                  submitting ||
                  credentials.activeAction !== "" ||
                  methods.length === 0
                }
              >
                {submitting || credentials.activeAction ? (
                  <IconLoader2 className="animate-spin" />
                ) : renewalMode ? (
                  <IconRefresh />
                ) : (
                  <IconPlus />
                )}
                {renewalMode
                  ? method === "token"
                    ? t("accounts.renewal.save")
                    : t("accounts.renewal.start")
                  : method === "token"
                    ? t("accounts.onboarding.save")
                    : t("accounts.onboarding.start")}
              </Button>
            </div>
          </form>
        )}
      </CollectionDetailShell>

      <DeviceCodeSheet
        open={credentials.deviceSheetOpen}
        flow={credentials.deviceFlow}
        flowHint={credentials.flowHint}
        onOpenChange={credentials.handleDeviceSheetOpenChange}
      />
    </>
  )
}

function oauthStatusForAccount(
  account: AccountSummary,
  providers: OAuthProviderStatus[],
): OAuthProviderStatus {
  const provider = providers.find(
    (candidate) => candidate.provider === account.provider,
  )
  const credential = provider?.credentials?.find(
    (candidate) => candidate.credential_id === account.account,
  )
  return {
    ...(credential ?? provider),
    provider: account.provider,
    credential_id: account.account,
    display_name:
      credential?.display_name ??
      provider?.display_name ??
      providerLabel(account.provider),
    methods:
      credential?.methods ?? provider?.methods ?? fallbackMethods(account),
    logged_in: account.status !== "not_logged_in",
    status: account.status,
    auth_method: account.auth_method,
    expires_at: account.expires_at,
  }
}

function fallbackMethods(account: AccountSummary): OAuthMethod[] {
  if (account.auth_method === "token") return ["token"]
  if (account.provider === "openai") return ["device_code"]
  return ["browser"]
}

function providerLabel(provider: string): string {
  if (provider === "openai") return "OpenAI"
  if (provider === "anthropic") return "Anthropic"
  if (provider === "google-antigravity") return "Google Antigravity"
  if (provider === "github-copilot") return "GitHub Copilot"
  return provider
}

function methodLabel(method: OAuthMethod, t: (key: string) => string): string {
  if (method === "browser") return t("credentials.actions.browser")
  if (method === "device_code") return t("credentials.actions.deviceCode")
  return t("accounts.methods.token")
}

function tokenPlaceholder(
  provider: string,
  t: (key: string) => string,
): string {
  if (provider === "anthropic") return t("credentials.fields.anthropicToken")
  if (provider === "github-copilot") {
    return t("credentials.fields.githubCopilotToken")
  }
  return t("credentials.fields.openaiToken")
}
