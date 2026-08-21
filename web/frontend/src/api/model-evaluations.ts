import { launcherFetch } from "@/api/http"

const BASE = "/api/model-evaluations"

export type EvaluationStatus =
  | "draft"
  | "preflighting"
  | "ready"
  | "running"
  | "judging"
  | "analyzing"
  | "completed"
  | "canceling"
  | "canceled"
  | "failed"

export type EvaluationCodeType = "hotpath-code" | "code" | "test" | "bench-test"

export interface EvaluationFocus {
  code_types?: EvaluationCodeType[]
  include_folders?: string[]
  exclude_folders?: string[]
  free_text?: string
}

export interface EvaluationLanguageProgress {
  available_files: number
  selected_files: number
  completed_files: number
  selected_bytes: number
  regions: string[]
  limited: boolean
}

export interface EvaluationProgress {
  stage: string
  languages: Record<string, EvaluationLanguageProgress>
  total_files: number
  selected_files: number
  completed_files: number
  total_tasks: number
  completed_tasks: number
  current_model?: string
  current_path?: string
  message?: string
  percent: number
  updated_at?: string
}

export interface EvaluationUsage {
  requests: number
  input_tokens: number
  cached_input_tokens: number
  output_tokens: number
  reasoning_tokens: number
  duration_millis: number
  estimated_cost_usd?: number
}

export interface EvaluationCorpusFile {
  candidate_id: string
  path: string
  blob_sha: string
  size_bytes: number
  language: string
  code_type: EvaluationCodeType
  module: string
  region: string
  chunks: Array<{
    id: string
    start_line: number
    end_line: number
    content_hash: string
  }>
}

export interface EvaluationCorpus {
  commit_sha: string
  inventory_hash: string
  policy_hash: string
  rubric_hash: string
  selector_run_id: string
  selection_rationale?: string
  files: EvaluationCorpusFile[]
  language_counts: Record<string, number>
  generated_at: string
}

export interface EvaluationCorpusPage {
  files: EvaluationCorpusFile[]
  total: number
  offset: number
  next_offset?: number
  commit_sha?: string
  inventory_hash?: string
  selection_rationale?: string
  generated_at?: string
  language_counts: Record<string, number>
}

export interface EvaluationComparison {
  model_alias: string
  concrete_models: Record<string, number>
  completion: "pending" | "completed" | "partial" | "failed"
  failure?: string
  failures: number
  rank: number
  overall_score?: number
  scores: Record<string, number>
  languages: string[]
  regions: string[]
  files_analyzed: number
  bytes_analyzed: number
  confirmed_findings: number
  unsupported_files: number
  usage: EvaluationUsage
  verdict?: string
  summary?: string
  strengths?: string[]
  limitations?: string[]
}

export interface RepositoryModelEvaluation {
  schema_version: number
  id: string
  version: number
  status: EvaluationStatus
  repository: string
  ref: string
  candidate_models: string[]
  selector_model_alias: string
  judge_model_alias: string
  focus: EvaluationFocus
  default_files_per_language: number
  files_per_language: Record<string, number>
  corpus?: EvaluationCorpus
  progress: EvaluationProgress
  usage: EvaluationUsage
  model_stats: Record<string, unknown>
  comparisons: EvaluationComparison[]
  warnings: string[]
  run_ids: string[]
  failure?: string
  created_at: string
  updated_at: string
  started_at?: string
  finished_at?: string
}

export interface RepositoryModelEvaluationSummary {
  id: string
  version: number
  status: EvaluationStatus
  repository: string
  ref: string
  candidate_models: string[]
  progress: EvaluationProgress
  usage: EvaluationUsage
  warnings: string[]
  failure?: string
  created_at: string
  updated_at: string
  finished_at?: string
}

export interface EvaluationModelOption {
  alias: string
  resolved_model: string
  provider?: string
  available: boolean
  blocked_reason?: string
  default?: boolean
}

export interface EvaluationOptions {
  models: EvaluationModelOption[]
  repositories: Array<{ id: string; repository: string; label: string }>
  code_types: EvaluationCodeType[]
  max_files_per_language: number
  default_files_per_language: number
  max_candidate_models: number
}

export interface EvaluationConfigInput {
  repository: string
  ref?: string
  candidate_models: string[]
  selector_model_alias: string
  judge_model_alias: string
  focus?: EvaluationFocus
  default_files_per_language?: number
  files_per_language?: Record<string, number>
  expected_version?: number
}

export class ModelEvaluationAPIError extends Error {
  readonly status: number
  readonly code?: string

  constructor(status: number, message: string, code?: string) {
    super(message)
    this.name = "ModelEvaluationAPIError"
    this.status = status
    this.code = code
  }
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const response = await launcherFetch(path, options)
  if (!response.ok) {
    const detail = await response.text().catch(() => "")
    let message = detail
    let code: string | undefined
    if (detail) {
      try {
        const parsed = JSON.parse(detail) as {
          code?: unknown
          message?: unknown
        }
        if (typeof parsed.message === "string") message = parsed.message
        if (typeof parsed.code === "string") code = parsed.code
      } catch {
        // Preserve a non-JSON server message.
      }
    }
    throw new ModelEvaluationAPIError(
      response.status,
      message || `API error: ${response.status}`,
      code,
    )
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

function json(method: string, body: unknown): RequestInit {
  return {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  }
}

export async function listModelEvaluations(
  signal?: AbortSignal,
): Promise<RepositoryModelEvaluationSummary[]> {
  const response = await request<{
    evaluations?: RepositoryModelEvaluationSummary[]
  }>(BASE, { signal })
  return (response.evaluations ?? []).map(normalizeSummary)
}

export async function getModelEvaluation(
  id: string,
  signal?: AbortSignal,
): Promise<RepositoryModelEvaluation> {
  const response = await request<{ evaluation: RepositoryModelEvaluation }>(
    `${BASE}/${encodeURIComponent(id)}`,
    { signal },
  )
  return normalizeEvaluation(response.evaluation)
}

export async function createModelEvaluation(
  input: EvaluationConfigInput,
): Promise<RepositoryModelEvaluation> {
  const response = await request<{ evaluation: RepositoryModelEvaluation }>(
    BASE,
    json("POST", input),
  )
  return normalizeEvaluation(response.evaluation)
}

export async function updateModelEvaluation(
  id: string,
  input: EvaluationConfigInput,
): Promise<RepositoryModelEvaluation> {
  const response = await request<{ evaluation: RepositoryModelEvaluation }>(
    `${BASE}/${encodeURIComponent(id)}`,
    json("PATCH", input),
  )
  return normalizeEvaluation(response.evaluation)
}

export async function deleteModelEvaluation(
  id: string,
  expectedVersion: number,
): Promise<void> {
  await request(
    `${BASE}/${encodeURIComponent(id)}`,
    json("DELETE", { expected_version: expectedVersion }),
  )
}

export async function runModelEvaluationAction(
  id: string,
  action: "preflight" | "start" | "cancel" | "resume" | "restart",
  expectedVersion: number,
): Promise<RepositoryModelEvaluation> {
  const response = await request<{ evaluation: RepositoryModelEvaluation }>(
    `${BASE}/${encodeURIComponent(id)}/${action}`,
    json("POST", { expected_version: expectedVersion }),
  )
  return normalizeEvaluation(response.evaluation)
}

export async function getModelEvaluationCorpus(
  id: string,
  offset = 0,
  limit = 100,
  signal?: AbortSignal,
): Promise<EvaluationCorpusPage> {
  const response = await request<{
    files?: EvaluationCorpusFile[]
    total?: number
    offset?: number
    next_offset?: number
    commit_sha?: string
    inventory_hash?: string
    selection_rationale?: string
    generated_at?: string
    language_counts?: Record<string, number>
  }>(
    `${BASE}/${encodeURIComponent(id)}/corpus?offset=${offset}&limit=${limit}`,
    { signal },
  )
  return {
    files: response.files ?? [],
    total: response.total ?? 0,
    offset: response.offset ?? 0,
    ...(response.next_offset == null
      ? {}
      : { next_offset: response.next_offset }),
    ...(response.commit_sha ? { commit_sha: response.commit_sha } : {}),
    ...(response.inventory_hash
      ? { inventory_hash: response.inventory_hash }
      : {}),
    ...(response.selection_rationale
      ? { selection_rationale: response.selection_rationale }
      : {}),
    ...(response.generated_at ? { generated_at: response.generated_at } : {}),
    language_counts: response.language_counts ?? {},
  }
}

export async function getModelEvaluationOptions(
  signal?: AbortSignal,
): Promise<EvaluationOptions> {
  const response = await request<Partial<EvaluationOptions>>(
    `${BASE}/options`,
    { signal },
  )
  return {
    models: response.models ?? [],
    repositories: response.repositories ?? [],
    code_types: response.code_types ?? [
      "hotpath-code",
      "code",
      "test",
      "bench-test",
    ],
    max_files_per_language: response.max_files_per_language ?? 20,
    default_files_per_language: response.default_files_per_language ?? 20,
    max_candidate_models: response.max_candidate_models ?? 8,
  }
}

const emptyUsage: EvaluationUsage = {
  requests: 0,
  input_tokens: 0,
  cached_input_tokens: 0,
  output_tokens: 0,
  reasoning_tokens: 0,
  duration_millis: 0,
}

function normalizeUsage(value?: Partial<EvaluationUsage>): EvaluationUsage {
  return { ...emptyUsage, ...value }
}

function normalizeProgress(
  value?: Partial<EvaluationProgress>,
): EvaluationProgress {
  return {
    stage: value?.stage ?? "idle",
    languages: value?.languages ?? {},
    total_files: value?.total_files ?? 0,
    selected_files: value?.selected_files ?? 0,
    completed_files: value?.completed_files ?? 0,
    total_tasks: value?.total_tasks ?? 0,
    completed_tasks: value?.completed_tasks ?? 0,
    percent: Math.min(100, Math.max(0, value?.percent ?? 0)),
    ...(value?.current_model ? { current_model: value.current_model } : {}),
    ...(value?.current_path ? { current_path: value.current_path } : {}),
    ...(value?.message ? { message: value.message } : {}),
    ...(value?.updated_at ? { updated_at: value.updated_at } : {}),
  }
}

function normalizeSummary(
  value: RepositoryModelEvaluationSummary,
): RepositoryModelEvaluationSummary {
  return {
    ...value,
    candidate_models: value.candidate_models ?? [],
    progress: normalizeProgress(value.progress),
    usage: normalizeUsage(value.usage),
    warnings: value.warnings ?? [],
  }
}

function normalizeEvaluation(
  value: RepositoryModelEvaluation,
): RepositoryModelEvaluation {
  return {
    ...value,
    ref: value.ref ?? "",
    candidate_models: value.candidate_models ?? [],
    focus: {
      code_types: value.focus?.code_types ?? [],
      include_folders: value.focus?.include_folders ?? [],
      exclude_folders: value.focus?.exclude_folders ?? [],
      free_text: value.focus?.free_text ?? "",
    },
    default_files_per_language: value.default_files_per_language ?? 20,
    files_per_language: value.files_per_language ?? {},
    progress: normalizeProgress(value.progress),
    usage: normalizeUsage(value.usage),
    model_stats: value.model_stats ?? {},
    comparisons: (value.comparisons ?? []).map((comparison) => ({
      ...comparison,
      concrete_models: comparison.concrete_models ?? {},
      scores: comparison.scores ?? {},
      languages: comparison.languages ?? [],
      regions: comparison.regions ?? [],
      usage: normalizeUsage(comparison.usage),
      strengths: comparison.strengths ?? [],
      limitations: comparison.limitations ?? [],
    })),
    warnings: value.warnings ?? [],
    run_ids: value.run_ids ?? [],
  }
}
