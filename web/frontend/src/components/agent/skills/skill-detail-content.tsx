import { type ReactNode, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"
import ReactMarkdown from "react-markdown"
import rehypeHighlight from "rehype-highlight"
import rehypeRaw from "rehype-raw"
import rehypeSanitize from "rehype-sanitize"
import remarkGfm from "remark-gfm"

import type { SkillDetailResponse } from "@/api/skills"
import {
  MarkdownCodeBlock,
  MessageCodeBlock,
} from "@/components/chat/message-code-block"
import { cn } from "@/lib/utils"

import { OriginBadge } from "./origin-badge"
import { getOriginLabel, getSkillOriginKind } from "./origin-utils"
import type { SkillDetailView } from "./types"

const detailViews = [
  "preview",
  "raw",
  "meta",
] as const satisfies SkillDetailView[]

export function SkillDetailContent({ skill }: { skill: SkillDetailResponse }) {
  const { t } = useTranslation()
  const [detailView, setDetailView] = useState<SkillDetailView>("preview")
  const origin = getSkillOriginKind(skill)
  const lineCount = useMemo(() => skill.content.split("\n").length, [skill])

  return (
    <div className="space-y-6">
      <div className="border-border/40 bg-card/40 space-y-4 rounded-xl border p-4 shadow-sm">
        <div className="flex flex-wrap items-center gap-2 px-1">
          <OriginBadge origin={origin} label={getOriginLabel(origin, t)} />
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          <MetadataItem
            label={t("pages.agent.skills.metadata.description")}
            value={skill.description || t("pages.agent.skills.no_description")}
          />
          <MetadataItem label="Source" value={skill.source} mono />
          {(skill.registry || skill.registry_name) && (
            <MetadataItem
              label={t("pages.agent.skills.metadata.registry")}
              value={skill.registry || skill.registry_name || ""}
            />
          )}
          {(skill.version || skill.installed_version) && (
            <MetadataItem
              label={t("pages.agent.skills.metadata.version")}
              value={skill.version || skill.installed_version || ""}
            />
          )}
          {skill.registry_url && (
            <MetadataItem
              label={t("pages.agent.skills.metadata.url")}
              value={
                <a
                  href={skill.registry_url}
                  target="_blank"
                  rel="noreferrer"
                  className="text-primary hover:text-primary/80 inline break-all underline-offset-4 hover:underline"
                >
                  {skill.registry_url}
                </a>
              }
              mono
            />
          )}
        </div>
      </div>

      <div
        className="border-border/70 bg-muted/20 inline-flex rounded-lg border p-1 shadow-sm"
        aria-label="Skill detail view"
      >
        {detailViews.map((view) => (
          <button
            key={view}
            type="button"
            className={cn(
              "rounded-md px-4 py-1.5 text-xs font-medium transition-all duration-200",
              detailView === view
                ? "bg-background text-foreground ring-border/30 shadow-[0_1px_3px_rgba(0,0,0,0.1)] ring-1"
                : "text-muted-foreground hover:text-foreground hover:bg-muted/50",
            )}
            aria-pressed={detailView === view}
            onClick={() => setDetailView(view)}
          >
            {t(`pages.agent.skills.detail_tabs.${view}`)}
          </button>
        ))}
      </div>

      {detailView === "preview" && (
        <div className="prose prose-zinc dark:prose-invert prose-sm sm:prose-base prose-pre:rounded-xl prose-pre:border prose-pre:border-border/40 prose-pre:bg-zinc-100 prose-pre:p-0 prose-pre:shadow-sm dark:prose-pre:bg-zinc-950/90 prose-headings:tracking-tight prose-a:text-primary prose-a:no-underline hover:prose-a:underline max-w-none">
          <ReactMarkdown
            remarkPlugins={[remarkGfm]}
            rehypePlugins={[rehypeRaw, rehypeSanitize, rehypeHighlight]}
            components={{ pre: MarkdownCodeBlock }}
          >
            {skill.content}
          </ReactMarkdown>
        </div>
      )}

      {detailView === "raw" && (
        <MessageCodeBlock
          code={skill.content}
          label={t("pages.agent.skills.detail_tabs.raw")}
          className="my-0"
          bodyClassName="text-[13px] leading-relaxed"
        />
      )}

      {detailView === "meta" && (
        <div className="grid gap-4 sm:grid-cols-2">
          <MetadataItem
            label={t("pages.agent.skills.metadata.name")}
            value={skill.name}
          />
          <MetadataItem label="Stable ID" value={skill.id} mono />
          <MetadataItem
            label={t("pages.agent.skills.metadata.lines")}
            value={String(lineCount)}
          />
          <MetadataItem
            label={t("pages.agent.skills.metadata.characters")}
            value={String(skill.content.length)}
          />
        </div>
      )}
    </div>
  )
}

function MetadataItem({
  label,
  value,
  mono = false,
}: {
  label: string
  value: ReactNode
  mono?: boolean
}) {
  return (
    <div className="border-border/70 bg-muted/20 rounded-xl border px-4 py-3">
      <div className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
        {label}
      </div>
      <div
        className={cn(
          "text-foreground mt-2 text-sm leading-6 break-all",
          mono && "font-mono text-xs",
        )}
      >
        {value}
      </div>
    </div>
  )
}
