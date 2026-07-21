import { IconCheck, IconCopy } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"
import { useCopyToClipboard } from "@/hooks/use-copy-to-clipboard"
import { formatMessageTime } from "@/hooks/use-pico-chat"
import { cn } from "@/lib/utils"
import type { ChatAttachment } from "@/store/chat"

interface UserMessageProps {
  content: string
  attachments?: ChatAttachment[]
  timestamp?: string | number
}

export function UserMessage({
  content,
  attachments = [],
  timestamp = "",
}: UserMessageProps) {
  const { t } = useTranslation()
  const { copy, isCopied } = useCopyToClipboard()
  const hasText = content.trim().length > 0
  const isCommand = content.trim().startsWith("/")
  const imageAttachments = attachments.filter(
    (attachment) => attachment.type === "image",
  )
  const copyMessageLabel = isCopied
    ? t("chat.copiedLabel")
    : t("chat.copyMessage")
  const formattedTimestamp =
    timestamp !== "" ? formatMessageTime(timestamp) : ""

  return (
    <div className="group flex w-full flex-col items-end gap-1.5">
      {imageAttachments.length > 0 && (
        <div className="flex max-w-[78%] flex-wrap justify-end gap-2 sm:max-w-[70%]">
          {imageAttachments.map((attachment, index) => (
            <img
              key={`${attachment.url}-${index}`}
              src={attachment.url}
              alt={attachment.filename || t("chat.uploadedImage")}
              className="max-h-72 max-w-full object-cover"
            />
          ))}
        </div>
      )}

      {hasText && (
        <div className="relative max-w-[78%] sm:max-w-[70%]">
          <div
            className={cn(
              "wrap-break-word whitespace-pre-wrap",
              isCommand
                ? "border-border bg-background text-foreground dark:bg-background rounded-xl border px-4 py-3 font-mono text-[14px]"
                : "bg-secondary text-secondary-foreground dark:bg-muted dark:text-foreground rounded-[1.35rem] px-4 py-2.5 text-[15px] leading-relaxed",
            )}
          >
            {isCommand ? (
              <div className="flex items-start gap-2.5">
                <span className="text-muted-foreground font-bold select-none">
                  ❯
                </span>
                <span className="mt-[1px]">{content}</span>
              </div>
            ) : (
              content
            )}
          </div>
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className={cn(
              "bg-background/80 hover:bg-muted absolute top-2 right-2 h-7 w-7 opacity-0 transition-opacity group-hover:opacity-100",
              isCommand
                ? "text-zinc-700 dark:text-zinc-200"
                : "text-muted-foreground",
            )}
            onClick={() => void copy(content)}
            aria-label={copyMessageLabel}
            title={copyMessageLabel}
          >
            {isCopied ? (
              <IconCheck className="h-4 w-4 text-green-500" />
            ) : (
              <IconCopy className="h-4 w-4" />
            )}
          </Button>
        </div>
      )}

      {formattedTimestamp && (
        <span className="px-1 text-[12px] text-zinc-400">
          {formattedTimestamp}
        </span>
      )}
    </div>
  )
}
