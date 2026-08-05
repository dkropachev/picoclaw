import {
  IconGitPullRequest,
  IconInbox,
  IconSettings,
} from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"

export type ReviewWorkbenchView = "inbox" | "development" | "policies"

export function ReviewWorkbenchTabs({
  active,
  onChange,
  navigationDisabled = false,
}: {
  active: ReviewWorkbenchView
  onChange: (view: ReviewWorkbenchView) => void
  navigationDisabled?: boolean
}) {
  const { t } = useTranslation()
  const tabs = [
    {
      id: "inbox" as const,
      label: t("pages.reviews.views.inbox", "Review inbox"),
      icon: IconInbox,
    },
    {
      id: "development" as const,
      label: t("pages.reviews.views.development", "My PR feedback"),
      icon: IconGitPullRequest,
    },
    {
      id: "policies" as const,
      label: t("pages.reviews.views.policies", "Attention policies"),
      icon: IconSettings,
    },
  ]

  return (
    <div
      role="group"
      aria-label={t("pages.reviews.views.label", "Review view")}
      className="border-border flex shrink-0 gap-1 overflow-x-auto border-b px-3 pt-2"
    >
      {tabs.map((tab) => {
        const selected = tab.id === active
        const Icon = tab.icon
        return (
          <Button
            key={tab.id}
            type="button"
            aria-current={selected ? "page" : undefined}
            variant={selected ? "secondary" : "ghost"}
            size="sm"
            className="shrink-0 rounded-b-none"
            disabled={!selected && navigationDisabled}
            onClick={selected ? undefined : () => onChange(tab.id)}
          >
            <Icon className="size-4" />
            {tab.label}
          </Button>
        )
      })}
    </div>
  )
}
