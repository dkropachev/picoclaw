import { IconPlus, IconTrash } from "@tabler/icons-react"
import type { ReactNode } from "react"
import { useTranslation } from "react-i18next"

import type {
  ThreadPolicyConfig,
  ThreadPolicyRuleType,
  ThreadPolicyThresholdLogic,
} from "@/api/tools"
import { ConfigChangeNotice } from "@/components/config-change-notice"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"

import type { ThreadPolicyDraftUpdater } from "./types"

interface ThreadPolicyTabProps {
  draft: ThreadPolicyConfig | null
  isLoading: boolean
  hasError: boolean
  isSaving: boolean
  isDirty: boolean
  onSave: () => void
  onUpdateDraft: ThreadPolicyDraftUpdater
}

const ruleTypes: ThreadPolicyRuleType[] = [
  "coding",
  "reviewing",
  "investigating",
  "general",
]

export function ThreadPolicyTab({
  draft,
  isLoading,
  hasError,
  isSaving,
  isDirty,
  onSave,
  onUpdateDraft,
}: ThreadPolicyTabProps) {
  const { t } = useTranslation()

  return (
    <div className="animate-in fade-in slide-in-from-bottom-2 space-y-12 pt-2 duration-500">
      {hasError ? (
        <div className="py-20 text-center">
          <p className="text-destructive font-medium">
            {t(
              "pages.agent.tools.thread_policy.load_error",
              "Failed to load thread policy",
            )}
          </p>
        </div>
      ) : isLoading || !draft ? (
        <LoadingState />
      ) : (
        <>
          <div className="flex flex-col gap-6 sm:flex-row sm:items-start sm:justify-between">
            <div className="max-w-xl space-y-3">
              <h1 className="text-foreground/90 text-2xl font-semibold tracking-tight">
                {t("pages.agent.tools.thread_policy.title", "Thread Policy")}
              </h1>
              <p className="text-muted-foreground/80 text-[14px] leading-relaxed">
                {t(
                  "pages.agent.tools.thread_policy.description",
                  "Configure when the main chat may become or join a PicoClaw thread.",
                )}
              </p>
            </div>

            <Button
              onClick={onSave}
              disabled={!isDirty || isSaving}
              className="h-10 shrink-0 rounded-xl px-6 shadow-sm transition-all active:scale-95"
            >
              {t("pages.agent.tools.thread_policy.save", "Save Changes")}
            </Button>
          </div>

          {isDirty && (
            <ConfigChangeNotice
              kind="save"
              title={t("common.saveChangesTitle")}
              description={t(
                "pages.agent.tools.thread_policy.unsaved_prompt",
                "Save thread policy changes to update model routing behavior.",
              )}
            />
          )}

          <div className="space-y-10">
            <section className="space-y-4">
              <h3 className="text-foreground/80 text-[13px] font-bold tracking-widest uppercase">
                {t("pages.agent.tools.thread_policy.behavior", "Behavior")}
              </h3>

              <div className="bg-card border-border/40 divide-border/40 divide-y overflow-hidden rounded-2xl border shadow-sm">
                <SettingRow
                  label={t(
                    "pages.agent.tools.thread_policy.enabled",
                    "Enable Policy",
                  )}
                  description={t(
                    "pages.agent.tools.thread_policy.enabled_desc",
                    "When enabled, the model sees the thread routing policy in the main chat prompt.",
                  )}
                >
                  <Switch
                    checked={draft.enabled}
                    onCheckedChange={(checked) =>
                      onUpdateDraft((current) => ({
                        ...current,
                        enabled: checked,
                      }))
                    }
                  />
                </SettingRow>

                <SettingRow
                  label={t("pages.agent.tools.thread_policy.mode", "Mode")}
                  description={t(
                    "pages.agent.tools.thread_policy.mode_desc",
                    "Tool registers or attaches after rule thresholds; auto may create or switch after thresholds; suggest only surfaces the option.",
                  )}
                >
                  <Select
                    value={draft.mode}
                    onValueChange={(value) =>
                      onUpdateDraft((current) => ({
                        ...current,
                        mode: value as ThreadPolicyConfig["mode"],
                      }))
                    }
                  >
                    <SelectTrigger className="bg-muted/40 hover:bg-muted/60 focus:ring-foreground/5 focus:border-border/80 w-full rounded-xl border-transparent shadow-none transition-all sm:w-64">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent className="border-border/40 rounded-xl shadow-lg">
                      <SelectItem value="auto">
                        {t(
                          "pages.agent.tools.thread_policy.modes.auto",
                          "Auto",
                        )}
                      </SelectItem>
                      <SelectItem value="tool">
                        {t(
                          "pages.agent.tools.thread_policy.modes.tool",
                          "Tool",
                        )}
                      </SelectItem>
                      <SelectItem value="suggest">
                        {t(
                          "pages.agent.tools.thread_policy.modes.suggest",
                          "Suggest",
                        )}
                      </SelectItem>
                      <SelectItem value="off">
                        {t("pages.agent.tools.thread_policy.modes.off", "Off")}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                </SettingRow>
              </div>
            </section>

            <section className="space-y-4">
              <div className="flex items-center justify-between gap-4">
                <h3 className="text-foreground/80 text-[13px] font-bold tracking-widest uppercase">
                  {t("pages.agent.tools.thread_policy.rules", "Rules")}
                </h3>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="rounded-xl"
                  onClick={() =>
                    onUpdateDraft((current) => ({
                      ...current,
                      rules: [
                        ...current.rules,
                        {
                          type: "coding",
                          description: "",
                          min_messages: 12,
                          min_text_chars: 6000,
                          threshold_logic: "any",
                        },
                      ],
                    }))
                  }
                >
                  <IconPlus className="size-4" />
                  {t("pages.agent.tools.thread_policy.add_rule", "Add Rule")}
                </Button>
              </div>

              <div className="bg-card border-border/40 divide-border/40 divide-y overflow-hidden rounded-2xl border shadow-sm">
                {draft.rules.length === 0 ? (
                  <div className="text-muted-foreground p-5 text-[13px]">
                    {t(
                      "pages.agent.tools.thread_policy.no_rules",
                      "No routing rules configured.",
                    )}
                  </div>
                ) : (
                  draft.rules.map((rule, index) => (
                    <div
                      key={`${rule.type}-${index}`}
                      className="grid gap-3 p-5 md:grid-cols-[12rem_minmax(0,1fr)_auto]"
                    >
                      <Select
                        value={rule.type}
                        onValueChange={(value) =>
                          onUpdateDraft((current) => ({
                            ...current,
                            rules: current.rules.map((item, itemIndex) =>
                              itemIndex === index
                                ? {
                                    ...item,
                                    type: value as ThreadPolicyRuleType,
                                  }
                                : item,
                            ),
                          }))
                        }
                      >
                        <SelectTrigger className="bg-muted/40 hover:bg-muted/60 focus:ring-foreground/5 focus:border-border/80 rounded-xl border-transparent shadow-none transition-all">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent className="border-border/40 rounded-xl shadow-lg">
                          {ruleTypes.map((type) => (
                            <SelectItem key={type} value={type}>
                              {t(`threads.types.${type}`, type)}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>

                      <Input
                        value={rule.description}
                        onChange={(event) =>
                          onUpdateDraft((current) => ({
                            ...current,
                            rules: current.rules.map((item, itemIndex) =>
                              itemIndex === index
                                ? {
                                    ...item,
                                    description: event.target.value,
                                  }
                                : item,
                            ),
                          }))
                        }
                        className="bg-muted/40 hover:bg-muted/60 focus-visible:bg-background focus-visible:border-border/80 focus-visible:ring-foreground/5 rounded-xl border-transparent shadow-none transition-all duration-300"
                        placeholder={t(
                          "pages.agent.tools.thread_policy.rule_placeholder",
                          "When should this thread type be used?",
                        )}
                      />

                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        className="rounded-xl"
                        title={t(
                          "pages.agent.tools.thread_policy.remove_rule",
                          "Remove rule",
                        )}
                        aria-label={t(
                          "pages.agent.tools.thread_policy.remove_rule",
                          "Remove rule",
                        )}
                        onClick={() =>
                          onUpdateDraft((current) => ({
                            ...current,
                            rules: current.rules.filter(
                              (_, itemIndex) => itemIndex !== index,
                            ),
                          }))
                        }
                      >
                        <IconTrash className="size-4" />
                      </Button>

                      <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_12rem] md:col-span-3">
                        <label className="space-y-1">
                          <span className="text-muted-foreground text-[12px] font-medium">
                            {t(
                              "pages.agent.tools.thread_policy.min_messages",
                              "Min Messages",
                            )}
                          </span>
                          <Input
                            type="number"
                            min={0}
                            inputMode="numeric"
                            value={rule.min_messages ?? ""}
                            onChange={(event) =>
                              onUpdateDraft((current) => ({
                                ...current,
                                rules: current.rules.map((item, itemIndex) =>
                                  itemIndex === index
                                    ? {
                                        ...item,
                                        min_messages: parseThresholdInteger(
                                          event.target.value,
                                        ),
                                      }
                                    : item,
                                ),
                              }))
                            }
                            className="bg-muted/40 hover:bg-muted/60 focus-visible:bg-background focus-visible:border-border/80 focus-visible:ring-foreground/5 rounded-xl border-transparent shadow-none transition-all duration-300"
                            placeholder="0"
                          />
                        </label>

                        <label className="space-y-1">
                          <span className="text-muted-foreground text-[12px] font-medium">
                            {t(
                              "pages.agent.tools.thread_policy.min_text_chars",
                              "Min Text Chars",
                            )}
                          </span>
                          <Input
                            type="number"
                            min={0}
                            inputMode="numeric"
                            value={rule.min_text_chars ?? ""}
                            onChange={(event) =>
                              onUpdateDraft((current) => ({
                                ...current,
                                rules: current.rules.map((item, itemIndex) =>
                                  itemIndex === index
                                    ? {
                                        ...item,
                                        min_text_chars: parseThresholdInteger(
                                          event.target.value,
                                        ),
                                      }
                                    : item,
                                ),
                              }))
                            }
                            className="bg-muted/40 hover:bg-muted/60 focus-visible:bg-background focus-visible:border-border/80 focus-visible:ring-foreground/5 rounded-xl border-transparent shadow-none transition-all duration-300"
                            placeholder="0"
                          />
                        </label>

                        <label className="space-y-1">
                          <span className="text-muted-foreground text-[12px] font-medium">
                            {t(
                              "pages.agent.tools.thread_policy.threshold_logic",
                              "Threshold Logic",
                            )}
                          </span>
                          <Select
                            value={rule.threshold_logic ?? "any"}
                            onValueChange={(value) =>
                              onUpdateDraft((current) => ({
                                ...current,
                                rules: current.rules.map((item, itemIndex) =>
                                  itemIndex === index
                                    ? {
                                        ...item,
                                        threshold_logic:
                                          value as ThreadPolicyThresholdLogic,
                                      }
                                    : item,
                                ),
                              }))
                            }
                          >
                            <SelectTrigger className="bg-muted/40 hover:bg-muted/60 focus:ring-foreground/5 focus:border-border/80 rounded-xl border-transparent shadow-none transition-all">
                              <SelectValue />
                            </SelectTrigger>
                            <SelectContent className="border-border/40 rounded-xl shadow-lg">
                              <SelectItem value="any">
                                {t(
                                  "pages.agent.tools.thread_policy.threshold_any",
                                  "Any Limit",
                                )}
                              </SelectItem>
                              <SelectItem value="all">
                                {t(
                                  "pages.agent.tools.thread_policy.threshold_all",
                                  "All Limits",
                                )}
                              </SelectItem>
                            </SelectContent>
                          </Select>
                        </label>
                      </div>
                    </div>
                  ))
                )}
              </div>
            </section>

            <section className="space-y-4">
              <h3 className="text-foreground/80 text-[13px] font-bold tracking-widest uppercase">
                {t(
                  "pages.agent.tools.thread_policy.instructions",
                  "Instructions",
                )}
              </h3>
              <Textarea
                value={draft.instructions}
                onChange={(event) =>
                  onUpdateDraft((current) => ({
                    ...current,
                    instructions: event.target.value,
                  }))
                }
                className="bg-card border-border/40 min-h-36 rounded-2xl p-4 shadow-sm"
                placeholder={t(
                  "pages.agent.tools.thread_policy.instructions_placeholder",
                  "Additional instructions for the model...",
                )}
              />
            </section>
          </div>
        </>
      )}
    </div>
  )
}

function parseThresholdInteger(value: string): number | undefined {
  const parsed = Number.parseInt(value, 10)
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return undefined
  }
  return parsed
}

function SettingRow({
  label,
  description,
  children,
}: {
  label: string
  description: string
  children: ReactNode
}) {
  return (
    <div className="hover:bg-muted/10 flex flex-col justify-between gap-4 p-5 transition-colors sm:flex-row sm:items-center">
      <div className="w-full space-y-1 sm:max-w-md">
        <label className="text-foreground/90 text-[15px] font-semibold tracking-tight">
          {label}
        </label>
        <p className="text-muted-foreground/80 text-[13px] leading-relaxed">
          {description}
        </p>
      </div>
      {children}
    </div>
  )
}

function LoadingState() {
  return (
    <div className="space-y-8">
      <Skeleton className="h-24 rounded-2xl" />
      <Skeleton className="h-64 rounded-2xl" />
    </div>
  )
}
