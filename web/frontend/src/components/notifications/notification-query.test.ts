import { describe, expect, it } from "vitest"

import {
  buildNotificationSimpleQuery,
  insertNotificationQuerySuggestion,
  maximumNotificationQueryLength,
  normalizeNotificationRouteSearch,
  notificationInboxHref,
  notificationQueryByteLength,
  truncateNotificationQuery,
  withNotificationSort,
} from "@/components/notifications/notification-query"

describe("notification query helpers", () => {
  it("normalizes route input and bounds query length", () => {
    expect(
      normalizeNotificationRouteSearch({ query: "  status = open  " }),
    ).toEqual({ query: "status = open" })
    expect(
      normalizeNotificationRouteSearch({
        query: "💡".repeat(maximumNotificationQueryLength),
      }).query,
    ).toSatisfy(
      (query: string | undefined) =>
        Boolean(query) &&
        notificationQueryByteLength(query ?? "") <=
          maximumNotificationQueryLength,
    )
    expect(normalizeNotificationRouteSearch({ query: 42 })).toEqual({})
  })

  it("truncates query limits at UTF-8 code-point boundaries", () => {
    const query = truncateNotificationQuery(
      `${"a".repeat(maximumNotificationQueryLength - 2)}💡tail`,
    )
    expect(query).toBe("a".repeat(maximumNotificationQueryLength - 2))
    expect(notificationQueryByteLength(query)).toBe(
      maximumNotificationQueryLength - 2,
    )
  })

  it("keeps query and selected notification in shareable URLs", () => {
    expect(
      notificationInboxHref(
        "status = open AND read = false",
        "ntf_11111111111111111111111111111111",
      ),
    ).toBe(
      "/notifications/ntf_11111111111111111111111111111111?query=status+%3D+open+AND+read+%3D+false",
    )
  })

  it("builds allowlisted simple filters", () => {
    expect(
      buildNotificationSimpleQuery({
        statuses: ["open"],
        priorities: ["critical", "high"],
        repository: "",
        text: "",
        unreadOnly: true,
        excludeSnoozed: true,
        sort: "priority",
      }),
    ).toBe(
      "status = open AND priority IN (critical, high) AND read = false AND snoozed = false ORDER BY priority DESC, updated DESC",
    )
  })

  it("quotes simple text filters without changing query structure", () => {
    expect(
      buildNotificationSimpleQuery({
        statuses: [],
        priorities: [],
        repository: 'owner/"release"',
        text: "path\\to",
        unreadOnly: false,
        excludeSnoozed: false,
        sort: "updated",
      }),
    ).toBe(
      'repository ~ "owner/\\"release\\"" AND text ~ "path\\\\to" ORDER BY updated DESC',
    )
  })

  it("replaces only top-level ORDER BY clauses", () => {
    expect(
      withNotificationSort(
        'text ~ "ORDER BY owner" AND (status = open OR status = resolved) ORDER BY created ASC',
        "repository",
      ),
    ).toBe(
      'text ~ "ORDER BY owner" AND (status = open OR status = resolved) ORDER BY repository ASC, updated DESC',
    )
  })

  it("inserts suggestions before an existing sort clause", () => {
    expect(
      insertNotificationQuerySuggestion(
        "status = open ORDER BY updated DESC",
        "read = false",
      ),
    ).toBe("status = open AND read = false ORDER BY updated DESC")
  })
})
