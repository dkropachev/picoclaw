import { IconCheck, IconLogout, IconUser } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import type { OAuthProvider, OAuthProviderStatus } from "@/api/oauth"
import { Button } from "@/components/ui/button"

function normalizeCredentialID(provider: OAuthProvider, credentialID: string) {
  const raw = credentialID.trim().toLowerCase()
  if (!raw || raw === provider) {
    return provider
  }
  if (raw.includes(":")) {
    return raw
  }
  return `${provider}:${raw}`
}

function credentialLabel(
  provider: OAuthProvider,
  credentialID: string | undefined,
  defaultLabel: string,
) {
  const normalized = normalizeCredentialID(provider, credentialID ?? "")
  if (normalized === provider) {
    return defaultLabel
  }
  return normalized
}

function sortCredentials(
  provider: OAuthProvider,
  credentials: OAuthProviderStatus[],
) {
  return [...credentials].sort((a, b) => {
    const aID = normalizeCredentialID(provider, a.credential_id ?? "")
    const bID = normalizeCredentialID(provider, b.credential_id ?? "")
    if (aID === provider) return -1
    if (bID === provider) return 1
    return aID.localeCompare(bID)
  })
}

function credentialTestID(credentialID: string) {
  return `credential-row-${credentialID.replaceAll(/[^a-z0-9_-]+/g, "-")}`
}

interface NamedCredentialListProps {
  provider: OAuthProvider
  credentials: OAuthProviderStatus[]
  selectedCredentialID: string
  actionBusy: boolean
  onSelectCredentialID: (credentialID: string) => void
  onAskLogout: (credentialID: string) => void
}

export function NamedCredentialList({
  provider,
  credentials,
  selectedCredentialID,
  actionBusy,
  onSelectCredentialID,
  onAskLogout,
}: NamedCredentialListProps) {
  const { t } = useTranslation()
  const selected = normalizeCredentialID(provider, selectedCredentialID)
  const sortedCredentials = sortCredentials(provider, credentials)
  const defaultLabel = t("credentials.labels.default", "Default")

  if (sortedCredentials.length === 0) {
    return (
      <div className="text-muted-foreground rounded-md border border-dashed px-3 py-2 text-xs">
        {t("credentials.labels.noneSaved", "No saved credentials")}
      </div>
    )
  }

  return (
    <div className="max-h-36 overflow-y-auto rounded-md border">
      {sortedCredentials.map((credential) => {
        const credentialID = normalizeCredentialID(
          provider,
          credential.credential_id ?? "",
        )
        const isSelected = credentialID === selected

        return (
          <div
            key={credentialID}
            data-testid={credentialTestID(credentialID)}
            className="border-border/60 flex min-h-10 items-center gap-2 border-b px-2 py-1.5 last:border-b-0"
          >
            <IconUser className="text-muted-foreground size-3.5 shrink-0" />
            <div className="min-w-0 flex-1">
              <div className="flex min-w-0 items-center gap-1.5">
                <span className="truncate text-xs font-medium">
                  {credentialLabel(provider, credentialID, defaultLabel)}
                </span>
                {isSelected ? (
                  <span className="bg-primary/10 text-primary inline-flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-[10px] font-medium">
                    <IconCheck className="size-3" />
                    {t("credentials.labels.selected", "Selected")}
                  </span>
                ) : null}
              </div>
              <div className="text-muted-foreground truncate text-[11px]">
                {credential.auth_method
                  ? credential.auth_method.toUpperCase()
                  : t("credentials.status.connected")}
                {credential.account_id ? ` - ${credential.account_id}` : ""}
                {credential.email ? ` - ${credential.email}` : ""}
              </div>
            </div>
            <Button
              size="sm"
              variant={isSelected ? "secondary" : "outline"}
              disabled={actionBusy}
              onClick={() => onSelectCredentialID(credentialID)}
            >
              {t("credentials.actions.use", "Use")}
            </Button>
            <Button
              size="icon-sm"
              variant="ghost"
              disabled={actionBusy}
              title={t("credentials.actions.logout")}
              aria-label={t("credentials.actions.logout")}
              onClick={() => onAskLogout(credentialID)}
              className="text-destructive hover:bg-destructive/10 hover:text-destructive"
            >
              <IconLogout className="size-3.5" />
            </Button>
          </div>
        )
      })}
    </div>
  )
}
