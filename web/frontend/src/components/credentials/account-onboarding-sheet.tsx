import { IconLoader2, IconPlus } from "@tabler/icons-react"
import { type FormEvent, useEffect, useMemo, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import type { ModelProviderOption } from "@/api/models"
import type {
  OAuthMethod,
  OAuthProvider,
  OAuthProviderStatus,
} from "@/api/oauth"
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
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"

const DEFAULT_PROVIDERS: OAuthProviderStatus[] = [
  {
    provider: "openai",
    credential_id: "openai",
    display_name: "OpenAI",
    methods: ["browser", "device_code", "token"],
    logged_in: false,
    status: "not_logged_in",
  },
  {
    provider: "anthropic",
    credential_id: "anthropic",
    display_name: "Anthropic",
    methods: ["token"],
    logged_in: false,
    status: "not_logged_in",
  },
  {
    provider: "google-antigravity",
    credential_id: "google-antigravity",
    display_name: "Google Antigravity",
    methods: ["browser"],
    logged_in: false,
    status: "not_logged_in",
  },
  {
    provider: "github-copilot",
    credential_id: "github-copilot",
    display_name: "GitHub Copilot",
    methods: ["token"],
    logged_in: false,
    status: "not_logged_in",
  },
]

const ACCOUNT_NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9._-]*$/

interface AccountOnboardingSheetProps {
  open: boolean
  account?: OAuthProviderStatus
  providers: OAuthProviderStatus[]
  providerOptions: ModelProviderOption[]
  registeredAccounts: OAuthProviderStatus[]
  activeAction: string
  error?: string
  onOpenChange: (open: boolean) => void
  onStartBrowserOAuth: (
    provider: OAuthProvider,
    credentialID?: string,
  ) => Promise<boolean>
  onStartDeviceCode: (credentialID?: string) => Promise<boolean>
  onSaveToken: (
    provider: OAuthProvider,
    token: string,
    credentialID?: string,
  ) => Promise<boolean>
}

function actionKey(provider: OAuthProvider, method: OAuthMethod): string {
  if (method === "device_code") return `${provider}:device`
  return `${provider}:${method}`
}

function preferredRenewalMethod(
  account: OAuthProviderStatus | undefined,
): OAuthMethod {
  if (!account) return "browser"
  if (account.auth_method === "token" && account.methods.includes("token")) {
    return "token"
  }
  if (account.methods.includes("browser")) return "browser"
  if (account.methods.includes("device_code")) return "device_code"
  return account.methods[0] ?? "token"
}

export function AccountOnboardingSheet({
  open,
  account,
  providers,
  registeredAccounts,
  activeAction,
  error,
  onOpenChange,
  onStartBrowserOAuth,
  onStartDeviceCode,
  onSaveToken,
}: AccountOnboardingSheetProps) {
  const { t } = useTranslation()
  const renewalMode = account != null
  const renewalCredentialID = account?.credential_id || account?.provider || ""
  const initialProvider = account?.provider ?? "openai"
  const initialMethod = preferredRenewalMethod(account)
  const providerOptions = useMemo(() => {
    const merged = new Map<string, OAuthProviderStatus>()
    const add = (item: OAuthProviderStatus) => {
      if (!item.provider || item.provider === "router") {
        return
      }
      merged.set(item.provider, item)
    }

    for (const item of providers.length > 0 ? providers : DEFAULT_PROVIDERS) {
      add(item)
    }

    return [...merged.values()].sort((a, b) =>
      a.display_name.localeCompare(b.display_name),
    )
  }, [providers])
  const [provider, setProvider] = useState<OAuthProvider>("openai")
  const [method, setMethod] = useState<OAuthMethod>("browser")
  const [accountName, setAccountName] = useState("")
  const [token, setToken] = useState("")
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [submissionPending, setSubmissionPending] = useState(false)
  const sessionRef = useRef(0)

  const methods = useMemo(() => {
    if (account?.provider === provider) {
      return account.methods
    }
    return (
      providerOptions.find((item) => item.provider === provider)?.methods ?? []
    )
  }, [account, provider, providerOptions])
  const actionBusy = activeAction !== "" || submissionPending
  const submitting =
    submissionPending || activeAction === actionKey(provider, method)
  const trimmedAccountName = accountName.trim()
  const normalizedCredentialID = trimmedAccountName
    ? `${provider}:${trimmedAccountName.toLowerCase()}`
    : ""
  const accountAlreadyExists = registeredAccounts.some(
    (item) =>
      normalizedCredentialID && item.credential_id === normalizedCredentialID,
  )
  const methodLabel = (item: OAuthMethod) => {
    if (item === "browser") return t("credentials.actions.browser")
    if (item === "device_code") return t("credentials.actions.deviceCode")
    return t("accounts.methods.token")
  }

  useEffect(() => {
    sessionRef.current += 1
    if (!open) {
      setAccountName("")
      setToken("")
      setErrors({})
      setSubmissionPending(false)
      return
    }
    setProvider(initialProvider)
    setMethod(initialMethod)
    setAccountName("")
    setToken("")
    setErrors({})
    setSubmissionPending(false)
  }, [initialMethod, initialProvider, open, renewalCredentialID])

  useEffect(() => {
    if (methods.length === 0) {
      return
    }
    if (!methods.includes(method)) {
      setMethod(methods[0] as OAuthMethod)
    }
  }, [method, methods])

  const validate = () => {
    const nextErrors: Record<string, string> = {}
    const name = accountName.trim()

    if (!renewalMode && !name && method === "token") {
      nextErrors.accountName = t("accounts.onboarding.nameRequired")
    } else if (!renewalMode && name && name.toLowerCase() === provider) {
      nextErrors.accountName = t("accounts.onboarding.nameReserved")
    } else if (!renewalMode && name && !ACCOUNT_NAME_RE.test(name)) {
      nextErrors.accountName = t("accounts.onboarding.nameInvalid")
    }

    if (method === "token" && !token.trim()) {
      nextErrors.token = t("accounts.onboarding.tokenRequired")
    }

    setErrors(nextErrors)
    return Object.keys(nextErrors).length === 0
  }

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!validate()) {
      return
    }

    const session = sessionRef.current
    const credentialID = renewalMode
      ? renewalCredentialID
      : trimmedAccountName || undefined
    setSubmissionPending(true)

    let ok: boolean
    try {
      ok =
        method === "browser"
          ? await onStartBrowserOAuth(provider, credentialID)
          : method === "device_code"
            ? await onStartDeviceCode(credentialID)
            : await onSaveToken(provider, token.trim(), credentialID)
    } finally {
      if (sessionRef.current === session) {
        setSubmissionPending(false)
      }
    }

    if (ok && sessionRef.current === session) {
      sessionRef.current += 1
      setAccountName("")
      setToken("")
      setErrors({})
      onOpenChange(false)
    }
  }

  const handleSheetOpenChange = (nextOpen: boolean) => {
    if (!nextOpen && submissionPending) {
      return
    }
    if (!nextOpen) {
      sessionRef.current += 1
      setAccountName("")
      setToken("")
      setErrors({})
    }
    onOpenChange(nextOpen)
  }

  return (
    <Sheet open={open} onOpenChange={handleSheetOpenChange}>
      <SheetContent className="data-[side=right]:!w-full data-[side=right]:sm:!w-[448px] data-[side=right]:sm:!max-w-[448px]">
        <SheetHeader>
          <SheetTitle>
            {renewalMode
              ? t("accounts.renewal.title")
              : t("accounts.onboarding.title")}
          </SheetTitle>
          <SheetDescription>
            {renewalMode
              ? t("accounts.renewal.description")
              : t("accounts.onboarding.description")}
          </SheetDescription>
        </SheetHeader>

        <form className="flex min-h-0 flex-1 flex-col" onSubmit={handleSubmit}>
          <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-4">
            {error && (
              <div
                role="alert"
                aria-live="polite"
                className="text-destructive bg-destructive/10 rounded-lg px-3 py-2 text-sm"
              >
                {error}
              </div>
            )}

            <Field label={t("accounts.fields.provider")} required>
              {renewalMode ? (
                <Input
                  value={account?.display_name ?? provider}
                  aria-label={t("accounts.fields.provider")}
                  readOnly
                />
              ) : (
                <Select
                  value={provider}
                  onValueChange={(value) => {
                    setProvider(value as OAuthProvider)
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
                    {providerOptions.map((item) => (
                      <SelectItem key={item.provider} value={item.provider}>
                        {item.display_name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </Field>

            <Field label={t("accounts.fields.method")} required>
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
                  {methods.map((item) => (
                    <SelectItem key={item} value={item}>
                      {methodLabel(item as OAuthMethod)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>

            {renewalMode ? (
              <Field label={t("accounts.fields.credentialID")}>
                <Input
                  value={renewalCredentialID}
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
                      setErrors((prev) => ({ ...prev, accountName: "" }))
                    }
                  }}
                  placeholder={t("accounts.onboarding.namePlaceholder")}
                />
                {accountAlreadyExists && !errors.accountName && (
                  <p className="text-muted-foreground mt-2 text-xs leading-normal">
                    {t("accounts.onboarding.nameExists")}
                  </p>
                )}
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
                      setErrors((prev) => ({ ...prev, token: "" }))
                    }
                  }}
                  placeholder={
                    provider === "anthropic"
                      ? t("credentials.fields.anthropicToken")
                      : provider === "github-copilot"
                        ? t("credentials.fields.githubCopilotToken")
                        : t("credentials.fields.openaiToken")
                  }
                />
              </Field>
            )}
          </div>

          <SheetFooter>
            <Button type="submit" disabled={actionBusy}>
              {submitting ? (
                <IconLoader2 className="size-4 animate-spin" />
              ) : (
                <IconPlus className="size-4" />
              )}
              {renewalMode
                ? method === "token"
                  ? t("accounts.renewal.save")
                  : t("accounts.renewal.start")
                : method === "token"
                  ? t("accounts.onboarding.save")
                  : t("accounts.onboarding.start")}
            </Button>
          </SheetFooter>
        </form>
      </SheetContent>
    </Sheet>
  )
}
