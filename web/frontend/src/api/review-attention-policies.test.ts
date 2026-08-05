import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"
import {
  isExactJSONObject,
  parseExactJSON,
  stringifyExactJSON,
} from "@/api/review-attention-json"
import {
  ReviewAttentionPoliciesAPIError,
  getReviewAttentionPolicies,
  putReviewAttentionPolicies,
} from "@/api/review-attention-policies"

vi.mock("@/api/http", () => ({
  launcherFetch: vi.fn(),
}))

const mockedLauncherFetch = vi.mocked(launcherFetch)

const responseBody = `{
  "global": {
    "review.submitted": [
      {
        "id": "deterministic",
        "kind": "deterministic",
        "when": "true",
        "title": "Confirm",
        "questions": {
          "limit": 9007199254740993,
          "decimal": 1.2300e+400,
          "__proto__": {"constructor": 9007199254740995},
          "Foo": "one",
          "foo": "two"
        }
      }
    ]
  },
  "repositories": {
    "Acme/Widgets": {
      "review.submitted": {
        "mode": "overlay",
        "gates": [{"id": "skip", "kind": "zero"}]
      },
      "review.disabled": {"mode": "disable"}
    }
  },
  "catalog_revision": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "config_revision": "opaque-config-1",
  "effects": {"gateway_effect": "applied"}
}`

function rawJSONResponse(
  body: BodyInit,
  status = 200,
  contentType = "application/json",
): Response {
  return new Response(body, {
    status,
    headers: { "Content-Type": contentType },
  })
}

describe("review attention policies API", () => {
  beforeEach(() => {
    mockedLauncherFetch.mockReset()
  })

  it("gets the strict catalog without rounding arbitrary question numbers", async () => {
    mockedLauncherFetch.mockResolvedValue(rawJSONResponse(responseBody))

    const snapshot = await getReviewAttentionPolicies()

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/reviews/attention-policies",
      expect.anything(),
    )
    const questions = snapshot.global["review.submitted"][0].questions
    expect(questions).toBeDefined()
    expect(stringifyExactJSON(questions!)).toBe(
      '{"limit":9007199254740993,"decimal":1.2300e+400,"__proto__":{"constructor":9007199254740995},"Foo":"one","foo":"two"}',
    )
    expect(isExactJSONObject(questions)).toBe(true)
    if (!isExactJSONObject(questions)) throw new Error("object expected")
    expect(Object.getPrototypeOf(questions)).toBeNull()
    expect(Object.hasOwn(questions, "__proto__")).toBe(true)
    expect(snapshot.repositories["Acme/Widgets"]["review.disabled"]).toEqual({
      mode: "disable",
      gates: [],
    })
    expect(snapshot.effects.gateway_effect).toBe("applied")
  })

  it("sends a full CAS replacement while retaining untouched numeric tokens", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(rawJSONResponse(responseBody))
      .mockResolvedValueOnce(
        rawJSONResponse(
          responseBody
            .replace("opaque-config-1", "opaque-config-2")
            .replace(/a{64}/, "b".repeat(64))
            .replace("Confirm", "Confirm locally"),
        ),
      )
    const snapshot = await getReviewAttentionPolicies()
    snapshot.global["review.submitted"][0].title = "Confirm locally"
    delete snapshot.repositories["Acme/Widgets"]["review.disabled"]

    const saved = await putReviewAttentionPolicies(
      snapshot,
      snapshot.config_revision,
    )

    expect(saved.config_revision).toBe("opaque-config-2")
    expect(mockedLauncherFetch).toHaveBeenCalledTimes(2)
    const [, init] = mockedLauncherFetch.mock.calls[1]
    expect(init).toMatchObject({
      method: "PUT",
      headers: { "Content-Type": "application/json" },
    })
    const body = String(init?.body)
    expect(body).toContain('"expected_config_revision":"opaque-config-1"')
    expect(body).toContain("9007199254740993")
    expect(body).toContain("9007199254740995")
    expect(body).not.toContain("9007199254740992")
    expect(body).not.toContain("review.disabled")
    expect(body).toContain('"global":{')
    expect(body).toContain('"repositories":{')

    const request = parseExactJSON(body)
    expect(isExactJSONObject(request)).toBe(true)
    if (!isExactJSONObject(request)) throw new Error("object expected")
    expect(Object.keys(request)).toEqual([
      "expected_config_revision",
      "global",
      "repositories",
    ])
  })

  it("uses an optional abort signal for reads and writes", async () => {
    const controller = new AbortController()
    mockedLauncherFetch
      .mockResolvedValueOnce(rawJSONResponse(responseBody))
      .mockResolvedValueOnce(rawJSONResponse(responseBody))

    const snapshot = await getReviewAttentionPolicies(controller.signal)
    await putReviewAttentionPolicies(
      snapshot,
      snapshot.config_revision,
      controller.signal,
    )

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/reviews/attention-policies",
      { signal: controller.signal },
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/reviews/attention-policies",
      expect.objectContaining({ signal: controller.signal }),
    )
  })

  it("surfaces fixed conflicts without retrying against a newer revision", async () => {
    mockedLauncherFetch.mockResolvedValue(
      rawJSONResponse('{"error":"config_revision_mismatch"}', 409),
    )

    const promise = putReviewAttentionPolicies(
      { global: {}, repositories: {} },
      "stale-revision",
    )
    await expect(promise).rejects.toMatchObject({
      name: "ReviewAttentionPoliciesAPIError",
      status: 409,
      code: "config_revision_mismatch",
    })
    await expect(promise).rejects.toBeInstanceOf(
      ReviewAttentionPoliciesAPIError,
    )
    expect(mockedLauncherFetch).toHaveBeenCalledOnce()
  })

  it("rejects a missing local CAS revision before issuing a request", async () => {
    for (const revision of [
      "  ",
      'bad"revision',
      "bad,revision",
      "bad\u0001revision",
      "x".repeat(4095),
    ]) {
      await expect(
        putReviewAttentionPolicies({ global: {}, repositories: {} }, revision),
      ).rejects.toMatchObject({
        status: 400,
        code: "expected_config_revision_required",
      })
    }
    expect(mockedLauncherFetch).not.toHaveBeenCalled()
  })

  it("rejects config revisions that cannot fence the companion agent request", async () => {
    const malformed = [
      'bad\\"revision',
      "bad,revision",
      "bad\\u0001revision",
      "x".repeat(4095),
    ]
    for (const revision of malformed) {
      mockedLauncherFetch.mockResolvedValueOnce(
        rawJSONResponse(responseBody.replace("opaque-config-1", revision)),
      )
    }

    for (let index = 0; index < malformed.length; index += 1) {
      await expect(getReviewAttentionPolicies()).rejects.toMatchObject({
        status: 502,
        code: "invalid_attention_policy_response",
      })
    }
  })

  it("rejects unknown, null, incomplete, and inconsistent response shapes", async () => {
    const malformed = [
      responseBody.replace(
        '"effects": {"gateway_effect": "applied"}',
        '"effects": {"gateway_effect": "applied"}, "raw_config": true',
      ),
      responseBody.replace('"global": {', '"global": null, "ignored": {'),
      responseBody.replace('"kind": "deterministic"', '"kind": "future"'),
      responseBody.replace(
        '"id": "deterministic"',
        '"id": "deterministic", "secret": "leak"',
      ),
      responseBody.replace(
        '"gateway_effect": "applied"',
        '"gateway_effect": "future"',
      ),
      responseBody.replace(
        '"kind": "deterministic",\n        "when": "true",\n        "title": "Confirm",',
        '"kind": "ai_isolated_context",\n        "criteria": "Ask",\n        "title": "Ask",',
      ),
      responseBody.replace(
        '"kind": "deterministic",',
        '"kind": "deterministic",\n        "agent_id": "main",',
      ),
      responseBody.replace('"kind": "deterministic"', '"kind": "zero"'),
      responseBody.replace(
        '"mode": "overlay",\n        "gates": [{"id": "skip", "kind": "zero"}]',
        '"mode": "overlay"',
      ),
      responseBody.replace(
        '"review.disabled": {"mode": "disable"}',
        '"review.disabled": {"mode": "disable", "gates": [{"id": "hidden", "kind": "zero"}]}',
      ),
    ]
    for (const body of malformed) {
      mockedLauncherFetch.mockResolvedValueOnce(rawJSONResponse(body))
    }

    for (let index = 0; index < malformed.length; index += 1) {
      await expect(getReviewAttentionPolicies()).rejects.toMatchObject({
        status: 502,
        code: "invalid_attention_policy_response",
      })
    }
  })

  it("rejects semantically invalid catalogs before editor hydration", async () => {
    const zero = (id: string) => ({ id, kind: "zero" })
    const working = (id: string, agentID: string) => ({
      id,
      kind: "ai_working_context",
      agent_id: agentID,
      criteria: "Ask only when owner intent is required.",
      title: "Owner input",
    })
    const malformed = [
      semanticResponse({ Bad: [] }, {}),
      semanticResponse({ "review.submitted": [zero("Bad")] }, {}),
      semanticResponse(
        { "review.submitted": [zero("duplicate"), zero("duplicate")] },
        {},
      ),
      semanticResponse({}, { "Acme/Widgets": {}, "acme/widgets": {} }),
      semanticResponse(
        {
          "review.submitted": [
            {
              id: "deterministic",
              kind: "deterministic",
              when: "1_e2",
              title: "Confirm",
              questions: [],
            },
          ],
        },
        {},
      ),
      semanticResponse(
        {
          "review.submitted": [
            {
              id: "deterministic",
              kind: "deterministic",
              when: "true",
              title: "x".repeat((4 << 10) + 1),
              questions: [],
            },
          ],
        },
        {},
      ),
      semanticResponse(
        { "review.submitted": [working("global", "main")] },
        {
          "Acme/Widgets": {
            "review.submitted": {
              mode: "overlay",
              gates: [working("repository", "reviewer")],
            },
          },
        },
      ),
    ]
    for (const body of malformed) {
      mockedLauncherFetch.mockResolvedValueOnce(rawJSONResponse(body))
    }

    for (let index = 0; index < malformed.length; index += 1) {
      await expect(getReviewAttentionPolicies()).rejects.toMatchObject({
        status: 502,
        code: "invalid_attention_policy_response",
      })
    }
  })

  it("rejects malformed media types, invalid UTF-8, and oversized responses", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(rawJSONResponse(responseBody, 200, "text/plain"))
      .mockResolvedValueOnce(
        rawJSONResponse(new Uint8Array([0xff, 0xfe, 0xfd])),
      )
      .mockResolvedValueOnce(
        new Response("{}", {
          status: 200,
          headers: {
            "Content-Type": "application/json",
            "Content-Length": String(2 << 20),
          },
        }),
      )

    for (let index = 0; index < 3; index += 1) {
      await expect(getReviewAttentionPolicies()).rejects.toMatchObject({
        status: 502,
        code: "invalid_attention_policy_response",
      })
    }
  })

  it("rejects questions beyond backend depth and Go-escaped byte limits", async () => {
    const htmlHeavyQuestion = JSON.stringify("<".repeat(22_000))
    const depth65Question = `${"[".repeat(65)}null${"]".repeat(65)}`
    const htmlHeavyResponse = responseWithQuestions(htmlHeavyQuestion)
    expect(new TextEncoder().encode(htmlHeavyQuestion).byteLength).toBeLessThan(
      128 << 10,
    )
    expect(new TextEncoder().encode(htmlHeavyResponse).byteLength).toBeLessThan(
      (1 << 20) + (64 << 10),
    )

    mockedLauncherFetch
      .mockResolvedValueOnce(rawJSONResponse(htmlHeavyResponse))
      .mockResolvedValueOnce(
        rawJSONResponse(responseWithQuestions(depth65Question)),
      )

    for (let index = 0; index < 2; index += 1) {
      await expect(getReviewAttentionPolicies()).rejects.toMatchObject({
        status: 502,
        code: "invalid_attention_policy_response",
      })
    }
  })

  it("falls back to a safe code for malformed error responses", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(rawJSONResponse("not json", 500, "text/plain"))
      .mockResolvedValueOnce(
        rawJSONResponse('{"error":"unsafe message!"}', 422),
      )

    await expect(getReviewAttentionPolicies()).rejects.toMatchObject({
      status: 500,
      code: "attention_policies_unavailable",
    })
    await expect(getReviewAttentionPolicies()).rejects.toMatchObject({
      status: 422,
      code: "attention_policies_unavailable",
    })
  })
})

function responseWithQuestions(questions: string): string {
  return `{
    "global": {
      "review.submitted": [{
        "id": "deterministic",
        "kind": "deterministic",
        "when": "true",
        "title": "Confirm",
        "questions": ${questions}
      }]
    },
    "repositories": {},
    "catalog_revision": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "config_revision": "opaque-config-1",
    "effects": {"gateway_effect": "applied"}
  }`
}

function semanticResponse(global: unknown, repositories: unknown): string {
  return JSON.stringify({
    global,
    repositories,
    catalog_revision:
      "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    config_revision: "opaque-config-1",
    effects: { gateway_effect: "applied" },
  })
}
