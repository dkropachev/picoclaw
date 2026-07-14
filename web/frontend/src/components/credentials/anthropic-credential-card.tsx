import {
  IconKey,
  IconLoader2,
  IconPlayerStopFilled,
  IconSparkles,
} from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import type { OAuthProviderStatus } from "@/api/oauth"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

import { CredentialCard } from "./credential-card"
import { NamedCredentialList } from "./named-credential-list"

interface AnthropicCredentialCardProps {
  status?: OAuthProviderStatus
  activeAction: string
  token: string
  credentialID: string
  onTokenChange: (value: string) => void
  onCredentialIDChange: (value: string) => void
  onSelectCredentialID: (value: string) => void
  onStopLoading: () => void
  onSaveToken: () => void
  onAskLogout: () => void
  onAskLogoutCredential: (credentialID: string) => void
}

export function AnthropicCredentialCard({
  status,
  activeAction,
  token,
  credentialID,
  onTokenChange,
  onCredentialIDChange,
  onSelectCredentialID,
  onStopLoading,
  onSaveToken,
  onAskLogout,
  onAskLogoutCredential,
}: AnthropicCredentialCardProps) {
  const { t } = useTranslation()
  const actionBusy = activeAction !== ""
  const tokenLoading = activeAction === "anthropic:token"
  const stopLabel = t("credentials.actions.stopLoading")
  const credentials = status?.credentials ?? []

  return (
    <CredentialCard
      title={
        <span className="inline-flex items-center gap-2">
          <span className="border-muted inline-flex size-6 items-center justify-center rounded-full border">
            <IconSparkles className="size-3.5" />
          </span>
          <span>Anthropic</span>
        </span>
      }
      description={t("credentials.providers.anthropic.description")}
      status={status?.status ?? "not_logged_in"}
      authMethod={status?.auth_method}
      details={
        credentials.length > 0 ? (
          <p>
            {t("credentials.labels.saved", "Saved")}: {credentials.length}
          </p>
        ) : null
      }
      actions={
        <div className="border-muted flex min-h-[208px] flex-col rounded-lg border p-3">
          <div className="flex h-full flex-col gap-3">
            <Input
              value={credentialID}
              onChange={(e) => onCredentialIDChange(e.target.value)}
              placeholder={t(
                "models.field.credentialIDHint",
                "Optional named credential, for example anthropic:work. Leave blank for the provider default.",
              )}
            />

            <NamedCredentialList
              provider="anthropic"
              credentials={credentials}
              selectedCredentialID={credentialID}
              actionBusy={actionBusy}
              onSelectCredentialID={onSelectCredentialID}
              onAskLogout={onAskLogoutCredential}
            />

            <div className="flex items-center gap-2">
              <Input
                value={token}
                onChange={(e) => onTokenChange(e.target.value)}
                type="password"
                placeholder={t("credentials.fields.anthropicToken")}
              />
              <Button
                size="sm"
                className="w-fit"
                disabled={actionBusy || !token.trim()}
                onClick={onSaveToken}
              >
                {tokenLoading && (
                  <IconLoader2 className="size-4 animate-spin" />
                )}
                <IconKey className="size-4" />
                {t("credentials.actions.saveToken")}
              </Button>
              {tokenLoading && (
                <Button
                  size="icon-sm"
                  variant="ghost"
                  onClick={onStopLoading}
                  aria-label={stopLabel}
                  title={stopLabel}
                  className="text-destructive hover:bg-destructive/10 hover:text-destructive"
                >
                  <IconPlayerStopFilled className="size-4" />
                </Button>
              )}
            </div>
          </div>
        </div>
      }
      footer={
        status?.logged_in ? (
          <Button
            variant="ghost"
            size="sm"
            disabled={actionBusy}
            onClick={onAskLogout}
            className="text-destructive hover:bg-destructive/10 hover:text-destructive"
          >
            {activeAction === "anthropic:logout" && (
              <IconLoader2 className="size-4 animate-spin" />
            )}
            {t("credentials.actions.logout")}
          </Button>
        ) : null
      }
    />
  )
}
