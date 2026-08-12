import { describe, expect, it } from "vitest"

import {
  normalizeReviewsSearch,
  reviewsSearchIsCanonical,
} from "@/routes/reviews"

const caseID = `prc_${"a".repeat(32)}`
const developmentCaseID = `pdc_${"b".repeat(32)}`

describe("reviews route search", () => {
  it("canonicalizes a chat handoff to only its public case and focus", () => {
    expect(
      normalizeReviewsSearch({
        case: caseID,
        focus: "chat",
        status: "submission_unknown",
        repository: " octo/repo ",
        cursor: "server-owned",
        instruction: "never in the URL",
        error: "never in the URL",
      }),
    ).toEqual({ case: caseID, focus: "chat" })
  })

  it("uses one canonical policy view without retaining inbox or private state", () => {
    expect(
      normalizeReviewsSearch({
        view: "policies",
        case: caseID,
        focus: "chat",
        repository: "octo/repo",
        decision: "review.submitted",
        questions: "private",
        revision: "opaque",
      }),
    ).toEqual({ view: "policies" })
  })

  it("keeps bounded portfolio selection and filter state", () => {
    expect(
      normalizeReviewsSearch({
        repo: " octo/repo ",
        pr: "84",
        filter: " role = review ",
        role: "develop",
        review_case: caseID,
        cursor: "server-owned",
      }),
    ).toEqual({
      repo: "octo/repo",
      pr: 84,
      filter: "role = review",
      role: "develop",
      review_case: caseID,
    })
    expect(
      normalizeReviewsSearch({
        view: "review",
        case: caseID,
        repo: "octo/repo",
        pr: 84,
        filter: "status = pending",
        role: "develop",
        instruction: "private",
      }),
    ).toEqual({
      view: "review",
      case: caseID,
      repo: "octo/repo",
      pr: 84,
      filter: "status = pending",
      role: "review",
    })

    expect(
      normalizeReviewsSearch({
        view: "review",
        case: "invalid",
        repo: "octo/repo",
        pr: 84,
        role: "review",
      }),
    ).toEqual({ repo: "octo/repo", pr: 84, role: "review" })

    expect(
      normalizeReviewsSearch({
        repo: "octo/repo",
        pr: 84,
        review_case: "invalid",
      }),
    ).toEqual({ repo: "octo/repo", pr: 84 })
  })

  it("keeps only safe development selection and repository state", () => {
    expect(
      normalizeReviewsSearch({
        view: "development",
        case: developmentCaseID,
        repository: " octo/repo ",
        pull_number: 84,
        focus: "chat",
        questions: "private",
        cursor: "opaque",
      }),
    ).toEqual({
      view: "development",
      case: developmentCaseID,
      focus: "chat",
    })

    expect(
      normalizeReviewsSearch({
        view: "development",
        case: caseID,
        repository: "octo/repo with space",
        pull_number: "01",
      }),
    ).toEqual({ view: "development" })

    for (const pullNumber of [0, -1, 2_147_483_648, 1.5, "01", [84]]) {
      expect(
        normalizeReviewsSearch({
          view: "development",
          pull_number: pullNumber,
        }),
      ).toEqual({ view: "development" })
    }
  })

  it("keeps development chat focus only with one valid public case", () => {
    expect(
      normalizeReviewsSearch({
        view: "development",
        case: developmentCaseID,
        focus: "chat",
        run: "private",
        task: "private",
        response_token: "private",
      }),
    ).toEqual({
      view: "development",
      case: developmentCaseID,
      focus: "chat",
    })
    expect(
      normalizeReviewsSearch({ view: "development", focus: "chat" }),
    ).toEqual({ view: "development" })
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
        focus: ["chat"],
        status: "pending",
        repository: ["octo/repo"],
      }),
    ).toEqual({})
    expect(
      normalizeReviewsSearch({
        case: [caseID, caseID],
        focus: ["chat", "chat"],
        status: ["submitted", "submitted"],
        repository: ["octo/repo", "octo/repo"],
      }),
    ).toEqual({})
  })

  it("keeps only the fixed chat focus paired with one valid case", () => {
    expect(normalizeReviewsSearch({ case: caseID, focus: "chat" })).toEqual({
      case: caseID,
      focus: "chat",
    })
    for (const raw of [
      { focus: "chat" },
      { case: caseID, focus: "attention" },
      { case: caseID, focus: ["chat"] },
    ]) {
      expect(normalizeReviewsSearch(raw)).toEqual(
        raw.case === caseID ? { case: caseID } : {},
      )
    }
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
        { case: caseID, focus: "chat" },
        { case: caseID, focus: "chat" },
      ),
    ).toBe(true)
    expect(
      reviewsSearchIsCanonical(
        { case: caseID, focus: "chat", status: "open" },
        { case: caseID, focus: "chat" },
      ),
    ).toBe(false)
  })
})
