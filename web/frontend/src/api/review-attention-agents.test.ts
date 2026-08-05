import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"
import {
  type ReviewAttentionAgentCatalog,
  ReviewAttentionAgentsAPIError,
  getReviewAttentionAgents,
} from "@/api/review-attention-agents"

vi.mock("@/api/http", () => ({
  launcherFetch: vi.fn(),
}))

const mockedLauncherFetch = vi.mocked(launcherFetch)
const configRevision = `sha256:${"a".repeat(64)}`
const nextConfigRevision = `sha256:${"b".repeat(64)}`
const responseMaximumBytes = 512 << 10

function catalogPage(
  overrides: Partial<ReviewAttentionAgentCatalog> = {},
): ReviewAttentionAgentCatalog {
  return {
    agents: [{ id: "main", name: "Main" }],
    default_agent_id: "main",
    config_revision: configRevision,
    ...overrides,
  }
}

function fullPage(): ReviewAttentionAgentCatalog["agents"] {
  return Array.from({ length: 256 }, (_, index) => ({
    id: `agent-${String(index).padStart(3, "0")}`,
    name: `Agent ${index}`,
  }))
}

function jsonResponse(
  value: unknown,
  options: { contentType?: string; status?: number } = {},
) {
  return new Response(JSON.stringify(value), {
    status: options.status,
    headers: {
      "Content-Type": options.contentType ?? "application/json",
    },
  })
}

function rawJSONResponse(source: string, status = 200) {
  return new Response(source, {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

async function expectInvalidResponse(value: unknown) {
  mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(value))
  const failure = await getReviewAttentionAgents({
    expectedConfigRevision: configRevision,
  }).catch((error: unknown) => error)
  expect(failure).toBeInstanceOf(ReviewAttentionAgentsAPIError)
  expect(failure).toMatchObject({
    name: "ReviewAttentionAgentsAPIError",
    status: 502,
    code: "invalid_attention_agents_response",
    message: "invalid_attention_agents_response",
  })
  expect(failure).not.toBeInstanceOf(TypeError)
}

describe("review attention agent catalog API", () => {
  beforeEach(() => {
    mockedLauncherFetch.mockReset()
  })

  it("loads an exact first identity page with a strong revision fence and cancellation", async () => {
    const controller = new AbortController()
    const response = catalogPage({
      agents: [
        { id: "builder", name: "Builder" },
        { id: "reviewer", name: "Reviewer" },
      ],
      // The default is catalog-wide and is not required to be on this page.
      default_agent_id: "main",
    })
    mockedLauncherFetch.mockResolvedValue(jsonResponse(response))

    await expect(
      getReviewAttentionAgents({
        expectedConfigRevision: configRevision,
        signal: controller.signal,
      }),
    ).resolves.toEqual(response)
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/reviews/attention-agents",
      {
        headers: { "If-Match": `"${configRevision}"` },
        signal: controller.signal,
      },
    )
  })

  it("uses the backend's exact canonical cursor query for subsequent pages", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse(
        catalogPage({
          agents: fullPage().map((agent, index) => ({
            ...agent,
            id: `page-two-${String(index).padStart(3, "0")}`,
          })),
          default_agent_id: "main",
          next_cursor: "512",
        }),
      ),
    )

    await expect(
      getReviewAttentionAgents({
        expectedConfigRevision: configRevision,
        cursor: "256",
      }),
    ).resolves.toMatchObject({ next_cursor: "512" })
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/reviews/attention-agents?cursor=256",
      {
        headers: { "If-Match": `"${configRevision}"` },
        signal: undefined,
      },
    )
  })

  it("accepts a full terminal page and backend-normalized Go scalar values", async () => {
    const agents = fullPage()
    agents[0] = { id: agents[0].id, name: "" }
    agents[1] = { id: agents[1].id, name: "\ufeffReviewer\ufeff" }
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse(catalogPage({ agents, default_agent_id: "elsewhere" })),
    )

    const result = await getReviewAttentionAgents({
      expectedConfigRevision: configRevision,
    })
    expect(result.agents).toHaveLength(256)
    expect(result.agents.slice(0, 2)).toEqual([
      { id: "agent-000", name: "" },
      { id: "agent-001", name: "\ufeffReviewer\ufeff" },
    ])
    expect(result).not.toHaveProperty("next_cursor")
  })

  it("propagates fetch cancellation without converting it into a server error", async () => {
    const aborted = new DOMException("cancelled", "AbortError")
    mockedLauncherFetch.mockRejectedValue(aborted)

    await expect(
      getReviewAttentionAgents({
        expectedConfigRevision: configRevision,
        signal: new AbortController().signal,
      }),
    ).rejects.toBe(aborted)
  })

  it("rejects unknown, missing, non-identity, and excessive response shapes", async () => {
    const malformed = [
      {},
      null,
      { ...catalogPage(), agents: {} },
      { ...catalogPage(), agents: [] },
      { ...catalogPage(), agents: [{ id: "main" }] },
      {
        ...catalogPage(),
        agents: [{ id: "main", name: "Main", secret: true }],
      },
      { ...catalogPage(), default_agent_id: null },
      { ...catalogPage(), config_revision: null },
      { ...catalogPage(), next_cursor: null },
      { ...catalogPage(), raw_config: true },
      catalogPage({
        agents: Array.from({ length: 257 }, (_, index) => ({
          id: `agent-${String(index).padStart(3, "0")}`,
          name: `Agent ${index}`,
        })),
      }),
    ]

    for (const value of malformed) await expectInvalidResponse(value)

    mockedLauncherFetch.mockResolvedValueOnce(
      rawJSONResponse(
        `{"agents":[{"id":"main","name":"Main"}],"default_agent_id":"main","config_revision":"${configRevision}","agents":[]}`,
      ),
    )
    await expect(
      getReviewAttentionAgents({ expectedConfigRevision: configRevision }),
    ).rejects.toMatchObject({
      status: 502,
      code: "invalid_attention_agents_response",
    })
  })

  it("requires canonical, unique identities in strict lexical order", async () => {
    const malformed = [
      catalogPage({ agents: [{ id: "Main", name: "Main" }] }),
      catalogPage({ agents: [{ id: "a".repeat(65), name: "Main" }] }),
      catalogPage({
        agents: [
          { id: "main", name: "Main" },
          { id: "main", name: "Duplicate" },
        ],
      }),
      catalogPage({
        agents: [
          { id: "reviewer", name: "Reviewer" },
          { id: "builder", name: "Builder" },
        ],
      }),
      catalogPage({ default_agent_id: "Main" }),
    ]

    for (const value of malformed) await expectInvalidResponse(value)
  })

  it("requires already-normalized bounded control-free names", async () => {
    const malformed = [
      catalogPage({ agents: [{ id: "main", name: " Main" }] }),
      catalogPage({ agents: [{ id: "main", name: "Main\u00a0" }] }),
      catalogPage({ agents: [{ id: "main", name: "Main\u0001" }] }),
      catalogPage({ agents: [{ id: "main", name: "é".repeat(129) }] }),
    ]

    for (const value of malformed) await expectInvalidResponse(value)

    mockedLauncherFetch.mockResolvedValueOnce(
      rawJSONResponse(
        `{"agents":[{"id":"main","name":"\\ud800"}],"default_agent_id":"main","config_revision":"${configRevision}"}`,
      ),
    )
    await expect(
      getReviewAttentionAgents({ expectedConfigRevision: configRevision }),
    ).rejects.toMatchObject({ code: "invalid_attention_agents_response" })
  })

  it("requires the successful response to remain on the requested config revision", async () => {
    await expectInvalidResponse(
      catalogPage({ config_revision: nextConfigRevision }),
    )
    await expectInvalidResponse(catalogPage({ config_revision: "bad,etag" }))
    await expectInvalidResponse(catalogPage({ config_revision: "" }))
  })

  it("validates next_cursor against page fullness and the current page boundary", async () => {
    const malformed = [
      catalogPage({ next_cursor: "256" }),
      catalogPage({ agents: fullPage(), next_cursor: "512" }),
      catalogPage({ agents: fullPage(), next_cursor: "0256" }),
      catalogPage({ agents: fullPage(), next_cursor: "257" }),
      catalogPage({ agents: fullPage(), next_cursor: "4294967296" }),
    ]
    for (const value of malformed) await expectInvalidResponse(value)

    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse(catalogPage({ agents: fullPage(), next_cursor: "512" })),
    )
    await expect(
      getReviewAttentionAgents({
        expectedConfigRevision: configRevision,
        cursor: "256",
      }),
    ).resolves.toMatchObject({ next_cursor: "512" })

    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse(catalogPage({ agents: fullPage() })),
    )
    await expect(
      getReviewAttentionAgents({
        expectedConfigRevision: configRevision,
        cursor: "4294967040",
      }),
    ).rejects.toMatchObject({ code: "invalid_attention_agents_response" })
  })

  it("rejects unsafe revisions and noncanonical cursors before issuing a request", async () => {
    const invalidRevisions: unknown[] = [
      null,
      "",
      " stale",
      "stale ",
      "bad,revision",
      'bad"revision',
      "bad\u007frevision",
      "x".repeat(4095),
    ]
    for (const revision of invalidRevisions) {
      await expect(
        getReviewAttentionAgents({
          expectedConfigRevision: revision as string,
        }),
      ).rejects.toMatchObject({
        status: 400,
        code: "invalid_attention_agents_request",
      })
    }

    const invalidCursors: unknown[] = [
      null,
      "",
      "0",
      "0256",
      "1",
      "257",
      "-256",
      "4294967296",
      "256&extra=1",
    ]
    for (const cursor of invalidCursors) {
      await expect(
        getReviewAttentionAgents({
          expectedConfigRevision: configRevision,
          cursor: cursor as string,
        }),
      ).rejects.toMatchObject({
        status: 400,
        code: "invalid_attention_agents_request",
      })
    }

    await expect(
      getReviewAttentionAgents(
        null as unknown as Parameters<typeof getReviewAttentionAgents>[0],
      ),
    ).rejects.toMatchObject({ code: "invalid_attention_agents_request" })
    expect(mockedLauncherFetch).not.toHaveBeenCalled()
  })

  it("rejects malformed media, invalid UTF-8, and declared or streamed oversized success bodies", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse(catalogPage(), { contentType: "text/plain" }),
      )
      .mockResolvedValueOnce(
        jsonResponse(catalogPage(), {
          contentType: "application/json; charset=utf-8; version=1",
        }),
      )
      .mockResolvedValueOnce(
        new Response(new Uint8Array([0xff, 0xfe]), {
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response("{}", {
          headers: {
            "Content-Type": "application/json",
            "Content-Length": String(responseMaximumBytes + 1),
          },
        }),
      )
      .mockResolvedValueOnce(
        new Response("x".repeat(responseMaximumBytes + 1), {
          headers: { "Content-Type": "application/json" },
        }),
      )

    for (let index = 0; index < 5; index += 1) {
      await expect(
        getReviewAttentionAgents({ expectedConfigRevision: configRevision }),
      ).rejects.toMatchObject({
        status: 502,
        code: "invalid_attention_agents_response",
      })
    }
  })

  it("accepts only the bounded JSON media-type forms emitted by the backend", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse(catalogPage(), {
          contentType: "application/json; charset=utf-8",
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse(catalogPage(), {
          contentType: 'Application/JSON; Charset="UTF-8"',
        }),
      )

    await expect(
      getReviewAttentionAgents({ expectedConfigRevision: configRevision }),
    ).resolves.toMatchObject({ config_revision: configRevision })
    await expect(
      getReviewAttentionAgents({ expectedConfigRevision: configRevision }),
    ).resolves.toMatchObject({ config_revision: configRevision })
  })

  it("maps only fixed server error envelopes and never exposes backend details", async () => {
    const cases = [
      {
        response: jsonResponse(
          { error: "config_revision_mismatch" },
          { status: 409 },
        ),
        expected: { status: 409, code: "config_revision_mismatch" },
      },
      {
        response: jsonResponse(
          { error: "invalid_attention_agents_request" },
          { status: 400 },
        ),
        expected: { status: 400, code: "invalid_attention_agents_request" },
      },
      {
        response: jsonResponse(
          { error: "attention_agents_unavailable" },
          { status: 503 },
        ),
        expected: { status: 503, code: "attention_agents_unavailable" },
      },
      {
        response: jsonResponse(
          { error: "private backend detail" },
          { status: 409 },
        ),
        expected: { status: 409, code: "attention_agents_unavailable" },
      },
      {
        response: jsonResponse(
          { error: "config_revision_mismatch", detail: "private" },
          { status: 409 },
        ),
        expected: { status: 409, code: "attention_agents_unavailable" },
      },
    ]

    for (const testCase of cases) {
      mockedLauncherFetch.mockResolvedValueOnce(testCase.response)
      const failure = await getReviewAttentionAgents({
        expectedConfigRevision: configRevision,
      }).catch((error: unknown) => error)
      expect(failure).toMatchObject({
        name: "ReviewAttentionAgentsAPIError",
        message: testCase.expected.code,
        ...testCase.expected,
      })
      expect(String(failure)).not.toContain("private backend detail")
      expect(String(failure)).not.toContain("private")
    }
  })

  it("bounds malformed non-success bodies before applying the unavailable fallback", async () => {
    mockedLauncherFetch.mockResolvedValue(
      new Response("x".repeat((64 << 10) + 1), {
        status: 503,
        headers: { "Content-Type": "application/json" },
      }),
    )

    await expect(
      getReviewAttentionAgents({ expectedConfigRevision: configRevision }),
    ).rejects.toMatchObject({
      status: 503,
      code: "attention_agents_unavailable",
      message: "attention_agents_unavailable",
    })
  })
})
