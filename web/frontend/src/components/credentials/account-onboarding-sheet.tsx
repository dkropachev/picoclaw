import { IconLoader2, IconPlus } from "@tabler/icons-react"
import { type FormEvent, useEffect, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"

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
  providers: OAuthProviderStatus[]
  registeredAccounts: OAuthProviderStatus[]
  activeAction: string
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

export function AccountOnboardingSheet({
  open,
  providers,
  registeredAccounts,
  activeAction,
  onOpenChange,
  onStartBrowserOAuth,
  onStartDeviceCode,
  onSaveToken,
}: AccountOnboardingSheetProps) {
  const { t } = useTranslation()
  const providerOptions = providers.length > 0 ? providers : DEFAULT_PROVIDERS
  const [provider, setProvider] = useState<OAuthProvider>("openai")
  const [method, setMethod] = useState<OAuthMethod>("browser")
  const [accountName, setAccountName] = useState("")
  const [token, setToken] = useState("")
  const [errors, setErrors] = useState<Record<string, string>>({})

  const methods = useMemo(
    () =>
      providerOptions.find((item) => item.provider === provider)?.methods ?? [],
    [provider, providerOptions],
  )
  const actionBusy = activeAction !== ""
  const submitting = activeAction === actionKey(provider, method)
  const normalizedCredentialID = `${provider}:${accountName.trim().toLowerCase()}`
  const accountAlreadyExists = registeredAccounts.some(
    (item) => item.credential_id === normalizedCredentialID,
  )
  const methodLabel = (item: OAuthMethod) => {
    if (item === "browser") return t("credentials.actions.browser")
    if (item === "device_code") return t("credentials.actions.deviceCode")
    return t("accounts.methods.token")
  }

  useEffect(() => {
    if (!open) {
      return
    }
    setProvider("openai")
    setMethod("browser")
    setAccountName("")
    setToken("")
    setErrors({})
  }, [open])

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

    if (!name) {
      nextErrors.accountName = t("accounts.onboarding.nameRequired")
    } else if (name.toLowerCase() === provider) {
      nextErrors.accountName = t("accounts.onboarding.nameReserved")
    } else if (!ACCOUNT_NAME_RE.test(name)) {
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

    const credentialID = accountName.trim()
    const ok =
      method === "browser"
        ? await onStartBrowserOAuth(provider, credentialID)
        : method === "device_code"
          ? await onStartDeviceCode(credentialID)
          : await onSaveToken(provider, token.trim(), credentialID)

    if (ok) {
      onOpenChange(false)
    }
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-md">
        <SheetHeader>
          <SheetTitle>{t("accounts.onboarding.title")}</SheetTitle>
          <SheetDescription>
            {t("accounts.onboarding.description")}
          </SheetDescription>
        </SheetHeader>

        <form className="flex min-h-0 flex-1 flex-col" onSubmit={handleSubmit}>
          <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-4">
            <Field label={t("accounts.fields.provider")} required>
              <Select
                value={provider}
                onValueChange={(value) => {
                  setProvider(value as OAuthProvider)
                  setErrors({})
                }}
              >
                <SelectTrigger className="w-full">
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
            </Field>

            <Field label={t("accounts.fields.method")} required>
              <Select
                value={method}
                onValueChange={(value) => {
                  setMethod(value as OAuthMethod)
                  setErrors({})
                }}
              >
                <SelectTrigger className="w-full">
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

            <Field
              label={t("accounts.fields.name")}
              hint={t("accounts.onboarding.nameHint")}
              error={errors.accountName}
              required
            >
              <Input
                value={accountName}
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

            {method === "token" && (
              <Field
                label={t("accounts.fields.token")}
                error={errors.token}
                required
              >
                <KeyInput
                  value={token}
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
              {method === "token"
                ? t("accounts.onboarding.save")
                : t("accounts.onboarding.start")}
            </Button>
          </SheetFooter>
        </form>
      </SheetContent>
    </Sheet>
  )
}
