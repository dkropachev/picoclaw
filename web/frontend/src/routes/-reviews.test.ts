import { describe, expect, it } from "vitest"

import {
  normalizeReviewsSearch,
  reviewsSearchIsCanonical,
} from "@/routes/reviews"

const caseID = `prc_${"a".repeat(32)}`

describe("reviews route search", () => {
  it("keeps only the safe human navigation state", () => {
    expect(
      normalizeReviewsSearch({
        case: caseID,
        status: "submission_unknown",
        repository: " octo/repo ",
        cursor: "server-owned",
        instruction: "never in the URL",
        error: "never in the URL",
      }),
    ).toEqual({
      case: caseID,
      status: "submission_unknown",
      repository: "octo/repo",
    })
  })

  it("uses one canonical policy view without retaining inbox or private state", () => {
    expect(
      normalizeReviewsSearch({
        view: "policies",
        case: caseID,
        repository: "octo/repo",
        decision: "review.submitted",
        questions: "private",
        revision: "opaque",
      }),
    ).toEqual({ view: "policies" })
  })

  it("canonicalizes every present invalid or repeated view to the empty inbox URL", () => {
    for (const view of [["policies"], "unknown", "", null, undefined]) {
      expect(
        normalizeReviewsSearch({
          view,
          case: caseID,
          status: "open",
          repository: "octo/repo",
        }),
      ).toEqual({})
    }
  })

  it("rejects malformed identifiers, repeated values, and unknown states", () => {
    expect(
      normalizeReviewsSearch({
        case: `prc_${"A".repeat(32)}`,
        status: "pending",
        repository: ["octo/repo"],
      }),
    ).toEqual({})
  })

  it("bounds the repository filter by UTF-8 bytes", () => {
    const exact = "é".repeat(256)
    expect(normalizeReviewsSearch({ repository: exact })).toEqual({
      repository: exact,
    })
    expect(normalizeReviewsSearch({ repository: `${exact}a` })).toEqual({})
  })

  it("detects noncanonical and sensitive raw state", () => {
    expect(
      reviewsSearchIsCanonical(
        { repository: " octo/repo ", cursor: "opaque" },
        { repository: "octo/repo" },
      ),
    ).toBe(false)
    expect(
      reviewsSearchIsCanonical(
        { case: caseID, status: "open" },
        { case: caseID, status: "open" },
      ),
    ).toBe(true)
  })
})
