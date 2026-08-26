import type {
  RepositoryReviewFinding,
  RepositoryReviewFindingContext,
  RepositoryReviewState,
} from "@/api/repository-reviews"

const discussionTextFieldBytes = 8 << 10
const discussionChecks = 32
const discussionCheckBytes = 512
const discussionObservationVariants = 4

function boundedDiscussionText(value: string | undefined): string {
  if (!value) return "none"
  const encoded = new TextEncoder().encode(value)
  if (encoded.byteLength <= discussionTextFieldBytes) return value
  return `${new TextDecoder().decode(encoded.slice(0, discussionTextFieldBytes))}\n[truncated for discussion]`
}

function boundedDiscussionCheck(value: string): string {
  const encoded = new TextEncoder().encode(value)
  return encoded.byteLength <= discussionCheckBytes
    ? value
    : `${new TextDecoder().decode(encoded.slice(0, discussionCheckBytes))}…`
}

export function discussionPrompt<
  T extends Pick<
    RepositoryReviewState,
    "id" | "repository" | "last_commit_sha"
  > & { contexts: RepositoryReviewFindingContext[] },
>(repository: T, findings: RepositoryReviewFinding[]): string {
  const contextByID = new Map(
    repository.contexts.map((context) => [context.id, context]),
  )
  const selectedContextIDs = [
    ...new Set(findings.flatMap((finding) => finding.context_ids)),
  ]
  const lines = [
    "Discuss these validated repository-review findings with me.",
    `Repository: ${repository.repository}`,
    `Repository review ID: ${repository.id}`,
    `Latest reviewed commit SHA: ${repository.last_commit_sha || "unknown"}`,
    "The context IDs below are opaque durable references. Use them as provenance identifiers; do not invent replacement context.",
    "",
  ]
  for (const finding of findings) {
    lines.push(
      `- Finding ${finding.id}: ${boundedDiscussionText(finding.title)}`,
      `  File: ${finding.file.path}${finding.line == null ? "" : `:${finding.line}`}`,
      `  Symbol: ${boundedDiscussionText(finding.symbol || "unknown")}`,
      `  Finding commit SHA: ${finding.commit_sha}`,
      `  Blob SHA: ${finding.file.blob_sha}`,
      `  Models: ${finding.models.join(", ")}`,
      `  Context IDs: ${finding.context_ids.join(", ")}`,
      `  Message: ${boundedDiscussionText(finding.message)}`,
      `  Evidence: ${boundedDiscussionText(finding.evidence)}`,
      `  Impact: ${boundedDiscussionText(finding.impact)}`,
      `  Validation: ${finding.validation.status} — ${boundedDiscussionText(finding.validation.summary)}`,
      `  Validation checks: ${(finding.validation.checks ?? []).slice(0, discussionChecks).map(boundedDiscussionCheck).join("; ") || "none"}`,
    )
    for (const observation of (finding.observations ?? []).slice(
      -discussionObservationVariants,
    )) {
      lines.push(
        `  Observation from ${observation.model} (${observation.context_id}):`,
        `    Severity: ${observation.severity}`,
        `    Evidence: ${boundedDiscussionText(observation.evidence)}`,
        `    Impact: ${boundedDiscussionText(observation.impact)}`,
        `    Validation: ${boundedDiscussionText(observation.validation.summary)}`,
      )
    }
  }
  lines.push("", "Selected context manifests:")
  for (const contextID of selectedContextIDs) {
    const context = contextByID.get(contextID)
    if (!context) {
      lines.push(
        `- Context ${contextID}: metadata unavailable in this snapshot`,
      )
      continue
    }
    lines.push(
      `- Context ${context.id}`,
      `  Commit SHA: ${context.commit_sha}`,
      `  Model: ${context.model}`,
      `  Profile hash: ${context.profile_hash || "unknown"}`,
      `  Files (${context.files.length}):`,
    )
    for (const file of context.files) {
      lines.push(
        `    - ${file.path} | blob ${file.blob_sha} | ${file.size_bytes} bytes`,
      )
    }
  }
  return lines.join("\n")
}

export function githubRepositoryPath(repository: string): string | undefined {
  const normalized = repository.trim()
  const match =
    /^([A-Za-z0-9](?:[A-Za-z0-9-]{0,38}))\/([A-Za-z0-9_.-]+)$/u.exec(normalized)
  if (!match || match[2] === "." || match[2] === "..") return undefined
  return `${match[1]}/${match[2]}`
}

function githubCommitRepositoryPath(repository: string): string | undefined {
  const normalized = repository.trim()
  const shorthand = githubRepositoryPath(normalized)
  if (shorthand) return shorthand

  const scp = /^git@github\.com:(.+)$/iu.exec(normalized)
  if (scp) return githubPath(scp[1])

  let parsed: URL
  try {
    parsed = new URL(normalized)
  } catch {
    return undefined
  }
  if (
    parsed.hostname.toLowerCase() !== "github.com" ||
    !new Set(["git:", "http:", "https:", "ssh:"]).has(parsed.protocol) ||
    parsed.search ||
    parsed.hash
  ) {
    return undefined
  }
  return githubPath(parsed.pathname)
}

export function githubCommitURL(
  repository: string,
  commitSHA: string,
): string | undefined {
  const path = githubCommitRepositoryPath(repository)
  const normalizedSHA = commitSHA.trim().toLowerCase()
  if (!path || !/^(?:[0-9a-f]{40}|[0-9a-f]{64})$/u.test(normalizedSHA)) {
    return undefined
  }
  return `https://github.com/${path}/commit/${normalizedSHA}`
}

export function shortCommitSHA(commitSHA: string): string {
  return commitSHA.trim().slice(0, 8)
}

function githubPath(value: string): string | undefined {
  const match =
    /^\/?([A-Za-z0-9](?:[A-Za-z0-9-]{0,38}))\/([A-Za-z0-9_.-]+?)\/?$/u.exec(
      value,
    )
  if (!match) return undefined
  const repository = match[2].replace(/\.git$/iu, "")
  if (!repository || repository === "." || repository === "..") return undefined
  return `${match[1]}/${repository}`
}
