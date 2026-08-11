import { IconSend, IconSparkles } from "@tabler/icons-react"
import type { FormEvent, RefObject } from "react"
import { useTranslation } from "react-i18next"

import type { ExactJSONValue } from "@/api/review-attention-json"
import {
  isExactJSONObject,
  stringifyExactJSON,
  trimGoSpace,
} from "@/api/review-attention-json"
import {
  type AttentionConversationProjection,
  type AttentionConversationStatus,
  type AttentionConversationTurnStatus,
  findActionableAttentionTurn,
} from "@/components/reviews/attention-conversation-model"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { cn } from "@/lib/utils"

export function AttentionConversation({
  projection,
  loading,
  loadError,
  actionError,
  response,
  pending,
  maximumResponseBytes,
  idPrefix = "review-attention",
  context = "outbound-review",
  attentionInputRef,
  recoveryButtonRef,
  onResponseChange,
  onSubmit,
  onRetryLoad,
  onRetryContinuation,
}: {
  projection?: AttentionConversationProjection
  loading: boolean
  loadError: unknown
  actionError?: string
  response: string
  pending: boolean
  maximumResponseBytes: number
  idPrefix?: string
  context?: "outbound-review" | "pr-development"
  attentionInputRef: RefObject<HTMLTextAreaElement | null>
  recoveryButtonRef: RefObject<HTMLButtonElement | null>
  onResponseChange: (response: string) => void
  onSubmit: (event: FormEvent) => void
  onRetryLoad: () => void
  onRetryContinuation: () => void
}) {
  const { t } = useTranslation()
  const actionable = findActionableAttentionTurn(projection)
  const waiting = actionable?.status === "waiting"
  const recoveryRequired = actionable?.status === "recovery_required"
  const normalizedResponse = trimGoSpace(response)
  const responseBytes = utf8ByteLength(normalizedResponse)
  const responseTooLarge = responseBytes > maximumResponseBytes
  const headingID = `${idPrefix}-conversation-heading`
  const responseID = `${idPrefix}-response`
  const responseHelpID = `${idPrefix}-response-help`
  const isPRDevelopment = context === "pr-development"

  return (
    <div
      className="border-border bg-muted/20 mt-3 rounded-lg border p-3"
      aria-labelledby={headingID}
    >
      <div className="flex items-center gap-2">
        <IconSparkles className="text-muted-foreground size-4" />
        <h4 id={headingID} className="text-sm font-medium">
          {isPRDevelopment
            ? t(
                "pages.reviews.development.attention_title",
                "PR decision gates",
              )
            : t("pages.reviews.attention.title", "AI attention")}
        </h4>
        {projection?.turns.length ? (
          <Badge variant="outline">{projection.turns.length}</Badge>
        ) : null}
      </div>

      {isPRDevelopment ? (
        <p className="text-muted-foreground mt-2 text-xs">
          {t(
            "pages.reviews.development.attention_safety",
            "Your reply continues this gate; it does not directly edit code, run CI, push commits, acknowledge a review, or merge the pull request.",
          )}
        </p>
      ) : null}

      {loading ? (
        <p className="text-muted-foreground mt-3 text-sm" role="status">
          {t(
            "pages.reviews.attention.loading",
            "Checking for an attention request…",
          )}
        </p>
      ) : loadError ? (
        <div className="mt-3 grid justify-items-start gap-2" role="alert">
          <p className="text-destructive text-sm">
            {isPRDevelopment
              ? t(
                  "pages.reviews.development.attention_load_error",
                  "PR decision gates are temporarily unavailable.",
                )
              : t(
                  "pages.reviews.attention.load_error",
                  "AI attention is temporarily unavailable.",
                )}
          </p>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={onRetryLoad}
          >
            {t("pages.reviews.retry", "Retry")}
          </Button>
        </div>
      ) : projection ? (
        <>
          {projection.turns.length > 0 ? (
            <div className="mt-3 grid gap-3">
              {projection.turns.map((turn, index) => (
                <article
                  key={`${index}-${turn.title}`}
                  aria-label={t(
                    "pages.reviews.attention.turn",
                    "Attention turn {{number}}",
                    { number: index + 1 },
                  )}
                  className="grid gap-2"
                >
                  <div className="bg-muted mr-auto max-w-[95%] rounded-lg px-3 py-2 text-sm">
                    <div className="mb-1 flex flex-wrap items-center gap-2 text-[11px] opacity-70">
                      <span>
                        {isPRDevelopment
                          ? t("pages.reviews.development.gate_actor", "Gate")
                          : t("pages.reviews.chat.assistant", "Assistant")}
                      </span>
                      <span>·</span>
                      <span>{attentionTurnStatusLabel(turn.status, t)}</span>
                    </div>
                    <p className="font-medium whitespace-pre-wrap">
                      {turn.title}
                    </p>
                    <AttentionQuestions questions={turn.questions} />
                  </div>
                  {turn.response !== undefined ? (
                    <div className="bg-primary text-primary-foreground ml-auto max-w-[95%] rounded-lg px-3 py-2 text-sm">
                      <div className="mb-1 text-[11px] opacity-70">
                        {t("pages.reviews.chat.you", "You")}
                      </div>
                      <p className="whitespace-pre-wrap">{turn.response}</p>
                    </div>
                  ) : null}
                </article>
              ))}
            </div>
          ) : null}

          <p
            className="text-muted-foreground mt-3 text-xs"
            role="status"
            aria-live="polite"
          >
            {attentionStatusLabel(
              projection.status,
              projection.turns.length > 0,
              isPRDevelopment,
              t,
            )}
          </p>

          {actionError ? (
            <p className="text-destructive mt-2 text-sm" role="alert">
              {actionError}
            </p>
          ) : null}

          {waiting && projection.can_respond ? (
            <form className="mt-3 grid gap-2" onSubmit={onSubmit}>
              <Label htmlFor={responseID} className="text-xs font-medium">
                {isPRDevelopment
                  ? t(
                      "pages.reviews.development.attention_response",
                      "Reply to the current PR gate",
                    )
                  : t(
                      "pages.reviews.attention.response",
                      "Reply to the AI attention request",
                    )}
              </Label>
              <div className="flex min-w-0 items-end gap-2">
                <Textarea
                  ref={attentionInputRef}
                  id={responseID}
                  value={response}
                  maxLength={maximumResponseBytes}
                  disabled={pending}
                  aria-invalid={responseTooLarge}
                  aria-describedby={responseHelpID}
                  className="min-h-20 flex-1"
                  placeholder={t(
                    "pages.reviews.attention.response_placeholder",
                    "Explain what should happen next…",
                  )}
                  onChange={(event) => onResponseChange(event.target.value)}
                />
                <Button
                  type="submit"
                  size="icon"
                  disabled={
                    pending || normalizedResponse === "" || responseTooLarge
                  }
                  aria-label={t(
                    "pages.reviews.attention.send",
                    "Send attention reply",
                  )}
                >
                  <IconSend />
                </Button>
              </div>
              <p
                id={responseHelpID}
                className={cn(
                  "text-xs",
                  responseTooLarge
                    ? "text-destructive"
                    : "text-muted-foreground",
                )}
              >
                {t(
                  "pages.reviews.attention.response_bytes",
                  "{{bytes}} / {{maximum}} UTF-8 bytes",
                  { bytes: responseBytes, maximum: maximumResponseBytes },
                )}
              </p>
            </form>
          ) : null}

          {recoveryRequired && projection.can_respond ? (
            <div className="mt-3 grid justify-items-start gap-2">
              <p className="text-muted-foreground text-xs">
                {t(
                  "pages.reviews.attention.recovery_help",
                  "Your reply is saved. Retry the interrupted continuation without changing it.",
                )}
              </p>
              <Button
                ref={recoveryButtonRef}
                type="button"
                variant="outline"
                size="sm"
                disabled={pending}
                onClick={onRetryContinuation}
              >
                {pending
                  ? t(
                      "pages.reviews.attention.retrying_continuation",
                      "Retrying…",
                    )
                  : t(
                      "pages.reviews.attention.retry_continuation",
                      "Retry continuation",
                    )}
              </Button>
            </div>
          ) : null}
        </>
      ) : null}
    </div>
  )
}

function AttentionQuestions({ questions }: { questions: ExactJSONValue }) {
  const { t } = useTranslation()
  const aiQuestions = projectedAIQuestions(questions)
  if (aiQuestions) {
    return (
      <div className="text-muted-foreground mt-2 grid gap-2">
        <p className="text-xs font-medium">
          {t("pages.reviews.attention.gate", "Gate {{id}}", {
            id: aiQuestions.gateID,
          })}
        </p>
        {aiQuestions.reason ? (
          <p className="whitespace-pre-wrap">{aiQuestions.reason}</p>
        ) : null}
        {aiQuestions.questions.length > 0 ? (
          <ul className="list-disc space-y-1 pl-5">
            {aiQuestions.questions.map((question, index) => (
              <li key={`${index}-${question}`} className="whitespace-pre-wrap">
                {question}
              </li>
            ))}
          </ul>
        ) : null}
      </div>
    )
  }
  if (typeof questions === "string") {
    return (
      <p className="text-muted-foreground mt-2 whitespace-pre-wrap">
        {questions}
      </p>
    )
  }
  if (
    Array.isArray(questions) &&
    questions.every((question) => typeof question === "string")
  ) {
    return questions.length > 0 ? (
      <ul className="text-muted-foreground mt-2 list-disc space-y-1 pl-5">
        {questions.map((question, index) => (
          <li key={`${index}-${question}`} className="whitespace-pre-wrap">
            {question}
          </li>
        ))}
      </ul>
    ) : null
  }
  return (
    <pre className="border-border bg-background text-muted-foreground mt-2 max-h-56 overflow-auto rounded border p-2 text-xs break-all whitespace-pre-wrap">
      {stringifyExactJSON(questions, {
        maximumBytes: 256 << 10,
        maximumDepth: 64,
        maximumNodes: 100_000,
      })}
    </pre>
  )
}

function projectedAIQuestions(
  questions: ExactJSONValue,
): { gateID: string; reason: string; questions: string[] } | undefined {
  if (!isExactJSONObject(questions)) return undefined
  const keys = Object.keys(questions)
  if (
    keys.length !== 3 ||
    !Object.hasOwn(questions, "gate_id") ||
    !Object.hasOwn(questions, "reason") ||
    !Object.hasOwn(questions, "questions") ||
    typeof questions.gate_id !== "string" ||
    questions.gate_id === "" ||
    typeof questions.reason !== "string"
  ) {
    return undefined
  }
  const prompts = questions.questions
  if (
    !Array.isArray(prompts) ||
    !prompts.every((prompt) => typeof prompt === "string")
  ) {
    return undefined
  }
  return {
    gateID: questions.gate_id,
    reason: questions.reason,
    questions: prompts,
  }
}

function utf8ByteLength(value: string): number {
  return new TextEncoder().encode(value).byteLength
}

function attentionTurnStatusLabel(
  status: AttentionConversationTurnStatus,
  t: Translate,
): string {
  const labels: Record<AttentionConversationTurnStatus, string> = {
    answered: t("pages.reviews.attention.turn_answered", "Answered"),
    waiting: t("pages.reviews.attention.turn_waiting", "Waiting for you"),
    continuing: t("pages.reviews.attention.turn_continuing", "Continuing"),
    recovery_required: t(
      "pages.reviews.attention.turn_recovery",
      "Retry required",
    ),
    canceled: t("pages.reviews.attention.turn_canceled", "Canceled"),
  }
  return labels[status]
}

function attentionStatusLabel(
  status: AttentionConversationStatus,
  hasTurns: boolean,
  isPRDevelopment: boolean,
  t: Translate,
): string {
  if (isPRDevelopment) {
    const labels: Record<AttentionConversationStatus, string> = {
      none: t(
        "pages.reviews.development.attention_status_none",
        "No PR decision gate is active.",
      ),
      queued: t(
        "pages.reviews.development.attention_status_queued",
        "The PR gate check is queued.",
      ),
      processing: t(
        "pages.reviews.development.attention_status_processing",
        "The current PR gate is being evaluated…",
      ),
      checking: t(
        "pages.reviews.development.attention_status_checking",
        "The current PR gate is being evaluated…",
      ),
      waiting: t(
        "pages.reviews.development.attention_status_waiting",
        "The current PR gate is waiting for your reply.",
      ),
      continuing: t(
        "pages.reviews.development.attention_status_continuing",
        "Continuing the current PR gate with your saved reply…",
      ),
      recovery_required: hasTurns
        ? t(
            "pages.reviews.development.attention_status_recovery",
            "Your reply is saved, but the PR gate continuation needs an explicit retry.",
          )
        : t(
            "pages.reviews.development.attention_status_recovery_without_turn",
            "The PR gate stopped in a recovery-required state.",
          ),
      completed: t(
        "pages.reviews.development.attention_status_completed",
        "The PR gate conversation is complete.",
      ),
      not_required: t(
        "pages.reviews.development.attention_status_not_required",
        "No reply is required for the current PR gate.",
      ),
      failed: t(
        "pages.reviews.development.attention_status_failed",
        "The PR gate check could not be completed.",
      ),
    }
    return labels[status]
  }
  const labels: Record<AttentionConversationStatus, string> = {
    none: t(
      "pages.reviews.attention.status_none",
      "No attention request exists for this review.",
    ),
    queued: t(
      "pages.reviews.attention.status_queued",
      "The attention check is queued.",
    ),
    processing: t(
      "pages.reviews.attention.status_processing",
      "AI is checking whether your attention is needed…",
    ),
    checking: t(
      "pages.reviews.attention.status_checking",
      "AI is checking whether your attention is needed…",
    ),
    waiting: t(
      "pages.reviews.attention.status_waiting",
      "AI is waiting for your reply.",
    ),
    continuing: t(
      "pages.reviews.attention.status_continuing",
      "Continuing with your saved reply…",
    ),
    recovery_required: hasTurns
      ? t(
          "pages.reviews.attention.status_recovery",
          "Your reply is saved, but continuation needs an explicit retry.",
        )
      : t(
          "pages.reviews.attention.status_recovery_without_turn",
          "The attention check stopped in a recovery-required state.",
        ),
    completed: t(
      "pages.reviews.attention.status_completed",
      "The attention conversation is complete.",
    ),
    not_required: t(
      "pages.reviews.attention.status_not_required",
      "AI confirmed that your attention is not required.",
    ),
    failed: t(
      "pages.reviews.attention.status_failed",
      "The attention check could not be completed.",
    ),
  }
  return labels[status]
}

type Translate = (
  key: string,
  fallback: string,
  options?: Record<string, unknown>,
) => string
