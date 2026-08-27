import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  bulkDeleteEventSources,
  createEventSource,
  deleteEventSource,
  getEventSource,
  getEventSourceSettings,
  listEventSources,
  updateEventSource,
  updateEventSourceSettings,
} from "@/api/event-sources"
import { launcherFetch } from "@/api/http"

vi.mock("@/api/http", () => ({ launcherFetch: vi.fn() }))

const mockedLauncherFetch = vi.mocked(launcherFetch)

describe("event source collection API", () => {
  beforeEach(() => mockedLauncherFetch.mockReset())

  it("sends collection query, cursor, limit, and abort signal", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse(collectionResponse()),
    )
    const controller = new AbortController()

    await listEventSources(
      {
        query: "kind = webhook ORDER BY name ASC",
        cursor: "opaque+/cursor",
        limit: 50,
      },
      controller.signal,
    )

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/event-sources?query=kind+%3D+webhook+ORDER+BY+name+ASC&cursor=opaque%2B%2Fcursor&limit=50",
      { signal: controller.signal },
    )
  })

  it("treats detail IDs as opaque path values", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(detailResponse()))

    await getEventSource("event/source+opaque")

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/event-sources/event%2Fsource%2Bopaque",
      undefined,
    )
  })

  it("uses one revision fence for source mutations and explicit bulk IDs", async () => {
    const source = {
      kind: "webhook" as const,
      name: "github",
      enabled: true,
      format: "github" as const,
      repositories: ["octo/picoclaw"],
      target_user: "octocat",
      poll_notifications: true,
      secret_update: "replace" as const,
      secret: "x".repeat(32),
    }
    mockedLauncherFetch.mockImplementation(async () =>
      jsonResponse({
        ...detailResponse(),
        deleted_ids: ["event-source-id"],
        failures: [],
        effects: effects,
      }),
    )

    await createEventSource(source, "revision-1")
    await updateEventSource("event-source-id", source, "revision-2")
    await deleteEventSource("event-source-id", "revision-3")
    await bulkDeleteEventSources(["event-source-id"], "revision-4")

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/event-sources",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          expected_config_revision: "revision-1",
          event_source: source,
        }),
      }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/event-sources/event-source-id",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({
          expected_config_revision: "revision-2",
          event_source: source,
        }),
      }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      3,
      "/api/event-sources/event-source-id",
      expect.objectContaining({
        method: "DELETE",
        body: JSON.stringify({ expected_config_revision: "revision-3" }),
      }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      4,
      "/api/event-sources/bulk-delete",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          ids: ["event-source-id"],
          config_revision: "revision-4",
        }),
      }),
    )
  })

  it("reads and revision-fences only global event-source settings", async () => {
    const settings = {
      enabled: true,
      database_path: "eventing/events.db",
      retention_days: 30,
      max_payload_bytes: 1_048_576,
      redact_fields: ["customer_number"],
    }
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({
          event_source_settings: settings,
          eligible_channel_adapters: [],
          config_revision: "revision-1",
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          event_source_settings: settings,
          eligible_channel_adapters: [],
          config_revision: "revision-2",
          effects,
        }),
      )

    await getEventSourceSettings()
    await updateEventSourceSettings(settings, "revision-1")

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/event-source-settings",
      undefined,
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/event-source-settings",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({
          expected_config_revision: "revision-1",
          event_source_settings: settings,
        }),
      }),
    )
  })
})

const effects = {
  launcher_effect: "applied",
  catalog_effect: "applied",
  gateway_effect: "restart_required",
}

function collectionResponse() {
  return {
    event_sources: [],
    total: 0,
    canonical_query: "ORDER BY name ASC",
    query_schema: { fields: [] },
    config_revision: "revision-1",
  }
}

function detailResponse() {
  return {
    event_source: {
      id: "event-source-id",
      name: "github",
      kind: "webhook",
      enabled: true,
      format: "github",
      status: "available",
      poll_notifications: true,
      repositories: ["octo/picoclaw"],
      target_user: "octocat",
      secret_configured: true,
    },
    config_revision: "revision-2",
  }
}

function jsonResponse(value: unknown) {
  return new Response(JSON.stringify(value), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  })
}
