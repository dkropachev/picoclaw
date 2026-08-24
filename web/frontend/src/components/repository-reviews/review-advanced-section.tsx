import { IconChevronRight } from "@tabler/icons-react"
import { type ReactNode, useState } from "react"

import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"

export function ReviewAdvancedSection({
  children,
  description,
  title = "Advanced",
}: {
  children: ReactNode
  description?: string
  title?: string
}) {
  const [open, setOpen] = useState(false)
  return (
    <Collapsible
      open={open}
      onOpenChange={setOpen}
      className="group/review-advanced border-border rounded-lg border"
    >
      <CollapsibleTrigger
        type="button"
        className="hover:bg-muted/50 flex w-full cursor-pointer items-center justify-between rounded-lg px-4 py-3 text-left text-sm font-medium"
      >
        <span>
          {title}
          {description && (
            <span className="text-muted-foreground ml-2 font-normal">
              {description}
            </span>
          )}
        </span>
        <IconChevronRight className="size-4 opacity-60 transition-transform group-data-[state=open]/review-advanced:rotate-90" />
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="border-border space-y-4 border-t p-4">{children}</div>
      </CollapsibleContent>
    </Collapsible>
  )
}
