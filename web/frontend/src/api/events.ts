import { launcherFetch } from "@/api/http"

export type EventRoutingStatus = "pending" | "claimed" | "succeeded" | "dead"

export type DispatchStatus =
  | "pending"
  | "claimed"
  | "running"
  | "succeeded"
  | "failed"
  | "dead"

export interface EventActor {
  id?: string
  type?: string
  display_name?: string
  attributes?: Record<string, string>
}

export interface EventSubject {
  id?: string
  type?: string
  name?: string
  url?: string
  attributes?: Record<string, string>
}

export interface EventRouting {
  status: EventRoutingStatus
  lease_until?: string
  available_at: string
  attempts: number
  last_error?: string
  updated_at: string
}

export interface EventView {
  id: string
  source: string
  connector: string
  type: string
  actor?: EventActor
  subject?: EventSubject
  occurred_at?: string
  received_at: string
  attributes?: Record<string, string>
  replay_of?: string
  payload_bytes: number
  routing: EventRouting
}

export interface EventPage {
  events: EventView[]
  next_cursor?: string
}

export interface DispatchView {
  id: string
  event_id: string
  workflow_ref: string
  workflow_revision?: string
  run_id: string
  status: DispatchStatus
  lease_until?: string
  available_at: string
  attempts: number
  last_error?: string
  created_at: string
  updated_at: string
  linked_at?: string
  finished_at?: string
}

export interface DispatchPage {
  dispatches: DispatchView[]
  next_cursor?: string
}

export interface ReplayResult {
  event: EventView
}

export interface EventListParams {
  source?: string
  connector?: string
  type?: string
  routingStatus?: EventRoutingStatus
  limit?: number
  cursor?: string
}

export interface DispatchListParams {
  eventID?: string
  workflowRef?: string
  status?: DispatchStatus
  limit?: number
  cursor?: string
}

export class EventAPIError extends Error {
  readonly status: number

  constructor(message: string, status: number) {
    super(message)
    this.name = "EventAPIError"
    this.status = status
  }
}

export const REPLAY_OUTCOME_UNKNOWN_MESSAGE =
  "replay outcome unknown; inspect events before retrying"

const MALFORMED_RESPONSE_MESSAGE =
  "The event service returned a malformed response."
const EVENT_ID_PATTERN = /^ev_[0-9a-f]{32}$/
const DISPATCH_ID_PATTERN = /^dsp_[0-9a-f]{32}$/
const MAX_WORKFLOW_REF_BYTES = 1024
const MAX_WORKFLOW_REVISION_BYTES = 256

function setOptionalParam(
  params: URLSearchParams,
  name: string,
  value: string | number | undefined,
): void {
  if (value !== undefined && value !== "") {
    params.set(name, String(value))
  }
}

function withQuery(path: string, params: URLSearchParams): string {
  const query = params.toString()
  return query === "" ? path : `${path}?${query}`
}

async function errorFromResponse(response: Response): Promise<EventAPIError> {
  const fallback =
    `Event request failed: ${response.status} ${response.statusText}`.trim()

  try {
    const body = (await response.json()) as { error?: unknown }
    if (typeof body.error === "string" && body.error !== "") {
      return new EventAPIError(body.error, response.status)
    }
  } catch {
    // Fall through to the status-based error.
  }

  return new EventAPIError(fallback, response.status)
}

function malformedResponse(): EventAPIError {
  return new EventAPIError(MALFORMED_RESPONSE_MESSAGE, 502)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function isOptionalString(value: unknown): value is string | undefined {
  return value === undefined || typeof value === "string"
}

function isOptionalStringRecord(
  value: unknown,
): value is Record<string, string> | undefined {
  return (
    value === undefined ||
    (isRecord(value) &&
      Object.values(value).every((item) => typeof item === "string"))
  )
}

function isNonNegativeInteger(value: unknown): value is number {
  return Number.isInteger(value) && (value as number) >= 0
}

function isEventID(value: unknown): value is string {
  return typeof value === "string" && EVENT_ID_PATTERN.test(value)
}

function isDispatchID(value: unknown): value is string {
  return typeof value === "string" && DISPATCH_ID_PATTERN.test(value)
}

function isBoundedTrimmedString(
  value: unknown,
  maximumBytes: number,
): value is string {
  return (
    typeof value === "string" &&
    value !== "" &&
    value === value.trim() &&
    new TextEncoder().encode(value).byteLength <= maximumBytes
  )
}

function isEventRoutingStatus(value: unknown): value is EventRoutingStatus {
  return (
    value === "pending" ||
    value === "claimed" ||
    value === "succeeded" ||
    value === "dead"
  )
}

function isDispatchStatus(value: unknown): value is DispatchStatus {
  return (
    value === "pending" ||
    value === "claimed" ||
    value === "running" ||
    value === "succeeded" ||
    value === "failed" ||
    value === "dead"
  )
}

function isEventActor(value: unknown): value is EventActor {
  return (
    isRecord(value) &&
    isOptionalString(value.id) &&
    isOptionalString(value.type) &&
    isOptionalString(value.display_name) &&
    isOptionalStringRecord(value.attributes)
  )
}

function isEventSubject(value: unknown): value is EventSubject {
  return (
    isRecord(value) &&
    isOptionalString(value.id) &&
    isOptionalString(value.type) &&
    isOptionalString(value.name) &&
    isOptionalString(value.url) &&
    isOptionalStringRecord(value.attributes)
  )
}

function isEventRouting(value: unknown): value is EventRouting {
  return (
    isRecord(value) &&
    isEventRoutingStatus(value.status) &&
    isOptionalString(value.lease_until) &&
    typeof value.available_at === "string" &&
    isNonNegativeInteger(value.attempts) &&
    isOptionalString(value.last_error) &&
    typeof value.updated_at === "string"
  )
}

function isEventView(value: unknown): value is EventView {
  return (
    isRecord(value) &&
    isEventID(value.id) &&
    typeof value.source === "string" &&
    typeof value.connector === "string" &&
    typeof value.type === "string" &&
    (value.actor === undefined || isEventActor(value.actor)) &&
    (value.subject === undefined || isEventSubject(value.subject)) &&
    isOptionalString(value.occurred_at) &&
    typeof value.received_at === "string" &&
    isOptionalStringRecord(value.attributes) &&
    (value.replay_of === undefined || isEventID(value.replay_of)) &&
    isNonNegativeInteger(value.payload_bytes) &&
    isEventRouting(value.routing)
  )
}

function isDispatchView(value: unknown): value is DispatchView {
  return (
    isRecord(value) &&
    isDispatchID(value.id) &&
    isEventID(value.event_id) &&
    isBoundedTrimmedString(value.workflow_ref, MAX_WORKFLOW_REF_BYTES) &&
    (value.workflow_revision === undefined ||
      isBoundedTrimmedString(
        value.workflow_revision,
        MAX_WORKFLOW_REVISION_BYTES,
      )) &&
    typeof value.run_id === "string" &&
    isDispatchStatus(value.status) &&
    isOptionalString(value.lease_until) &&
    typeof value.available_at === "string" &&
    isNonNegativeInteger(value.attempts) &&
    isOptionalString(value.last_error) &&
    typeof value.created_at === "string" &&
    typeof value.updated_at === "string" &&
    isOptionalString(value.linked_at) &&
    isOptionalString(value.finished_at)
  )
}

function parseDispatchView(value: unknown): DispatchView {
  if (!isDispatchView(value)) {
    throw malformedResponse()
  }
  return value
}

function parseEventView(value: unknown): EventView {
  if (!isEventView(value)) {
    throw malformedResponse()
  }
  return value
}

function parseEventPage(value: unknown): EventPage {
  if (
    !isRecord(value) ||
    !Array.isArray(value.events) ||
    !value.events.every(isEventView) ||
    !isOptionalString(value.next_cursor)
  ) {
    throw malformedResponse()
  }
  return value as unknown as EventPage
}

function parseDispatchPage(value: unknown): DispatchPage {
  if (
    !isRecord(value) ||
    !Array.isArray(value.dispatches) ||
    !value.dispatches.every(isDispatchView) ||
    !isOptionalString(value.next_cursor)
  ) {
    throw malformedResponse()
  }
  return value as unknown as DispatchPage
}

function parseReplayResult(
  value: unknown,
  replayedEventID: string,
): ReplayResult {
  if (
    !isRecord(value) ||
    !isEventView(value.event) ||
    value.event.id === replayedEventID ||
    value.event.replay_of !== replayedEventID
  ) {
    throw malformedResponse()
  }
  return value as unknown as ReplayResult
}

function isSafeReplayFailureStatus(status: number): boolean {
  return (
    status === 400 ||
    status === 401 ||
    status === 403 ||
    status === 404 ||
    status === 503
  )
}

async function parseJSON<T>(
  response: Response,
  parse: (value: unknown) => T,
): Promise<T> {
  let body: unknown
  try {
    body = await response.json()
  } catch {
    throw malformedResponse()
  }
  return parse(body)
}

async function requestJSON<T>(
  path: string,
  parse: (value: unknown) => T,
  init?: RequestInit,
): Promise<T> {
  const response = await launcherFetch(path, init)
  if (!response.ok) {
    throw await errorFromResponse(response)
  }
  return parseJSON(response, parse)
}

export async function listEvents(
  filters: EventListParams = {},
): Promise<EventPage> {
  const params = new URLSearchParams()
  setOptionalParam(params, "source", filters.source)
  setOptionalParam(params, "connector", filters.connector)
  setOptionalParam(params, "type", filters.type)
  setOptionalParam(params, "routing_status", filters.routingStatus)
  setOptionalParam(params, "limit", filters.limit)
  setOptionalParam(params, "cursor", filters.cursor)

  return requestJSON(withQuery("/api/events", params), parseEventPage)
}

export async function getEvent(eventID: string): Promise<EventView> {
  return requestJSON(
    `/api/events/${encodeURIComponent(eventID)}`,
    parseEventView,
  )
}

export async function listEventDispatches(
  filters: DispatchListParams = {},
): Promise<DispatchPage> {
  const params = new URLSearchParams()
  setOptionalParam(params, "event_id", filters.eventID)
  setOptionalParam(params, "workflow_ref", filters.workflowRef)
  setOptionalParam(params, "status", filters.status)
  setOptionalParam(params, "limit", filters.limit)
  setOptionalParam(params, "cursor", filters.cursor)

  return requestJSON(
    withQuery("/api/events/dispatches", params),
    parseDispatchPage,
  )
}

export async function getEventDispatch(
  dispatchID: string,
): Promise<DispatchView> {
  return requestJSON(
    `/api/events/dispatches/${encodeURIComponent(dispatchID)}`,
    (value) => {
      const dispatch = parseDispatchView(value)
      if (dispatch.id !== dispatchID) {
        throw malformedResponse()
      }
      return dispatch
    },
  )
}

export async function getEventPayload(
  eventID: string,
  signal?: AbortSignal,
): Promise<string> {
  const response = await launcherFetch(
    `/api/events/${encodeURIComponent(eventID)}/payload`,
    { signal },
  )
  if (!response.ok) {
    throw await errorFromResponse(response)
  }

  return response.text()
}

export async function replayEvent(eventID: string): Promise<ReplayResult> {
  let response: Response
  try {
    response = await launcherFetch(
      `/api/events/${encodeURIComponent(eventID)}/replay`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: "{}",
      },
    )
  } catch {
    throw new EventAPIError(REPLAY_OUTCOME_UNKNOWN_MESSAGE, 0)
  }

  if (!response.ok) {
    if (!isSafeReplayFailureStatus(response.status)) {
      throw new EventAPIError(REPLAY_OUTCOME_UNKNOWN_MESSAGE, 0)
    }
    throw await errorFromResponse(response)
  }

  try {
    return await parseJSON(response, (value) =>
      parseReplayResult(value, eventID),
    )
  } catch {
    throw new EventAPIError(REPLAY_OUTCOME_UNKNOWN_MESSAGE, 0)
  }
}
