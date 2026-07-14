import { IconFilter, IconPlus, IconSearch } from "@tabler/icons-react"
import { useAtom, useAtomValue } from "jotai"
import { useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type ThreadSummary,
  type ThreadType,
  createThread,
  getThreads,
} from "@/api/threads"
import { ThreadTile } from "@/components/threads/thread-tile"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { ScrollArea } from "@/components/ui/scroll-area"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { switchChatSession } from "@/features/chat/controller"
import { chatAtom } from "@/store/chat"
import {
  threadSearchFocusNonceAtom,
  threadSearchQueryAtom,
} from "@/store/threads"

const THREAD_TYPES: Array<ThreadType | "all"> = [
  "all",
  "coding",
  "reviewing",
  "investigating",
  "general",
]

export function ThreadSidebar() {
  const { t } = useTranslation()
  const inputRef = useRef<HTMLInputElement>(null)
  const [query, setQuery] = useAtom(threadSearchQueryAtom)
  const focusNonce = useAtomValue(threadSearchFocusNonceAtom)
  const { activeSessionId } = useAtomValue(chatAtom)
  const [selectedType, setSelectedType] = useState<ThreadType | "all">("all")
  const [threads, setThreads] = useState<ThreadSummary[]>([])
  const [isLoading, setIsLoading] = useState(false)
  const [loadError, setLoadError] = useState(false)

  useEffect(() => {
    inputRef.current?.focus()
    inputRef.current?.select()
  }, [focusNonce])

  useEffect(() => {
    const timer = window.setTimeout(() => {
      setIsLoading(true)
      setLoadError(false)
      void getThreads({
        query,
        type: selectedType === "all" ? "" : selectedType,
        limit: 80,
      })
        .then((items) => setThreads(items))
        .catch((error) => {
          console.error("Failed to load threads:", error)
          setLoadError(true)
        })
        .finally(() => setIsLoading(false))
    }, 160)

    return () => window.clearTimeout(timer)
  }, [query, selectedType, activeSessionId])

  const openThread = (threadId: string) => {
    void switchChatSession(threadId)
  }

  const handleCreateThread = async () => {
    try {
      const thread = await createThread({
        type: selectedType === "all" ? "general" : selectedType,
        title: query.trim() || t("threads.newThread"),
        source_query: query.trim(),
      })
      setThreads((prev) => [
        thread,
        ...prev.filter((item) => item.id !== thread.id),
      ])
      void switchChatSession(thread.id)
    } catch (error) {
      console.error("Failed to create thread:", error)
      setLoadError(true)
    }
  }

  return (
    <section className="bg-background flex min-h-0 flex-1 flex-col">
      <div className="border-border/50 flex h-14 shrink-0 items-center justify-between border-b px-4">
        <div className="flex items-center gap-2">
          <IconSearch className="text-muted-foreground size-4" />
          <h1 className="text-base font-semibold">{t("threads.title")}</h1>
        </div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-9 gap-2"
          title={t("threads.newThread")}
          aria-label={t("threads.newThread")}
          onClick={() => void handleCreateThread()}
        >
          <IconPlus className="size-4" />
          <span>{t("threads.newThread")}</span>
        </Button>
      </div>

      <div className="border-border/50 flex shrink-0 flex-col gap-3 border-b p-4 md:flex-row">
        <div className="relative min-w-0 flex-1">
          <IconSearch className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
          <Input
            ref={inputRef}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder={t("threads.searchPlaceholder")}
            className="h-9 pl-8"
          />
        </div>
        <Select
          value={selectedType}
          onValueChange={(value) =>
            setSelectedType(value as ThreadType | "all")
          }
        >
          <SelectTrigger className="h-9 w-full md:w-[220px]">
            <div className="flex items-center gap-2">
              <IconFilter className="text-muted-foreground size-4" />
              <SelectValue />
            </div>
          </SelectTrigger>
          <SelectContent>
            {THREAD_TYPES.map((type) => (
              <SelectItem key={type} value={type}>
                {t(`threads.types.${type}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      <ScrollArea className="min-h-0 flex-1">
        <div className="grid gap-3 p-4 md:grid-cols-2 xl:grid-cols-3">
          {loadError && (
            <div className="text-destructive bg-destructive/5 rounded-md px-3 py-2 text-sm md:col-span-2 xl:col-span-3">
              {t("threads.loadFailed")}
            </div>
          )}
          {!loadError && isLoading && threads.length === 0 && (
            <div className="text-muted-foreground px-2 py-12 text-center text-sm md:col-span-2 xl:col-span-3">
              {t("threads.loading")}
            </div>
          )}
          {!loadError && !isLoading && threads.length === 0 && (
            <div className="text-muted-foreground px-2 py-12 text-center text-sm md:col-span-2 xl:col-span-3">
              {t("threads.empty")}
            </div>
          )}
          {threads.map((thread) => (
            <ThreadTile
              key={thread.id}
              thread={thread}
              active={thread.id === activeSessionId}
              onOpen={openThread}
            />
          ))}
        </div>
      </ScrollArea>
    </section>
  )
}
