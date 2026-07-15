export const DEFAULT_THREAD_SOURCE_QUERY = "New thread"

const GENERIC_THREAD_START_PROMPT =
  "Start this thread by asking what we should work on here."

function normalizeText(value: string) {
  return value.replace(/\s+/g, " ").trim()
}

function isMeaningfulThreadTask(value: string) {
  const normalized = normalizeText(value).toLowerCase()
  return (
    normalized !== "" &&
    normalized !== DEFAULT_THREAD_SOURCE_QUERY.toLowerCase() &&
    normalized !== GENERIC_THREAD_START_PROMPT.toLowerCase()
  )
}

function stripThreadRequestPrefix(value: string) {
  let task = normalizeText(value)
  task = task.replace(/^(?:can|could|would)\s+you\s+/i, "")
  task = task.replace(/^please\s+/i, "")
  task = task.replace(
    /^(?:start|create|open|make)(?:\s+me)?\s+(?:a\s+)?(?:new\s+)?thread(?:\s+(?:about|for|on|to|regarding))?\s*/i,
    "",
  )
  task = task.replace(
    /^(?:a\s+)?thread\s+(?:about|for|on|to|regarding)\s+/i,
    "",
  )
  return normalizeText(task)
}

function cleanDirective(value: string) {
  return normalizeText(value)
    .replace(/^i\s+(?:want|need|would like)\s+you\s+to\s+/i, "")
    .replace(/^(?:can|could|would)\s+you\s+/i, "")
    .replace(/^please\s+/i, "")
    .replace(/\bas family of\b/gi, "as a family of")
    .trim()
}

function contextualizeDirective(directive: string, topic: string) {
  const cleanedDirective = cleanDirective(directive)
  const cleanedTopic = normalizeText(topic)
  const topicAction = cleanedTopic.replace(/^planning\s+to\s+/i, "")
  if (!cleanedTopic) {
    return cleanedDirective
  }

  return normalizeText(
    cleanedDirective
      .replace(/\bgo\s+there\b/gi, topicAction)
      .replace(/\bget\s+there\b/gi, topicAction),
  )
}

export function buildThreadSourceQuery(query: string = "") {
  const normalized = normalizeText(query)
  return normalized || DEFAULT_THREAD_SOURCE_QUERY
}

export function buildThreadInitialPrompt(query: string = "") {
  const task = stripThreadRequestPrefix(query)
  if (!isMeaningfulThreadTask(task)) {
    return ""
  }

  const directiveMatch = task.match(
    /^(.+?)(?:[,.;:]\s*|\s+and\s+)i\s+(?:want|need|would like)\s+you\s+to\s+(.+)$/i,
  )
  if (directiveMatch) {
    return contextualizeDirective(directiveMatch[2], directiveMatch[1])
  }

  return cleanDirective(task)
}

export function buildThreadInitialPromptFromCandidates(
  ...candidates: Array<string | undefined>
) {
  for (const candidate of candidates) {
    const prompt = buildThreadInitialPrompt(candidate ?? "")
    if (prompt) {
      return prompt
    }
  }
  return ""
}
