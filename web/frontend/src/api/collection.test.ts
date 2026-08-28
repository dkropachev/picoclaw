import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  CollectionAPIError,
  collectionListURL,
  collectionQueryByteLength,
  collectionRequest,
  collectionUTF8BytePositionToUTF16Offset,
  maximumCollectionPageSize,
  maximumCollectionQueryBytes,
  projectCollectionQuerySchema,
  truncateCollectionQuery,
} from "@/api/collection"
import { launcherFetch } from "@/api/http"

vi.mock("@/api/http", () => ({ launcherFetch: vi.fn() }))

const mockedLauncherFetch = vi.mocked(launcherFetch)

describe("collection API foundation", () => {
  beforeEach(() => mockedLauncherFetch.mockReset())

  it("builds bounded list URLs while preserving opaque cursors", () => {
    expect(
      collectionListURL("/api/things", {
        query: "  status = ready  ",
        cursor: "opaque+/= cursor",
        limit: maximumCollectionPageSize + 25,
      }),
    ).toBe(
      "/api/things?query=status+%3D+ready&cursor=opaque%2B%2F%3D+cursor&limit=200",
    )
    expect(collectionListURL("/api/things", { limit: 0 })).toBe(
      "/api/things?limit=1",
    )
  })

  it("truncates UTF-8 queries only at code-point boundaries", () => {
    const value = `${"a".repeat(maximumCollectionQueryBytes - 2)}💡tail`
    const truncated = truncateCollectionQuery(value)
    expect(truncated).toBe("a".repeat(maximumCollectionQueryBytes - 2))
    expect(collectionQueryByteLength(truncated)).toBe(
      maximumCollectionQueryBytes - 2,
    )
  })

  it("converts zero-based UTF-8 byte positions into DOM string offsets", () => {
    const value = "naïve 💡 status"
    expect(collectionUTF8BytePositionToUTF16Offset(value, 2)).toBe(2)
    expect(collectionUTF8BytePositionToUTF16Offset(value, 3)).toBe(2)
    expect(collectionUTF8BytePositionToUTF16Offset(value, 7)).toBe(6)
    expect(collectionUTF8BytePositionToUTF16Offset(value, 11)).toBe(8)
  })

  it("projects structured query errors and rejects unsafe error codes", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          code: "invalid_query",
          message: "Unexpected operator\nnear status",
          position: 17,
        }),
        { status: 400 },
      ),
    )
    const first = collectionRequest("/api/things")
    await expect(first).rejects.toMatchObject({
      name: "CollectionAPIError",
      status: 400,
      code: "invalid_query",
      position: 17,
      message: "Unexpected operator near status",
    })

    mockedLauncherFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          code: "unsafe code with spaces",
          message: "x".repeat(2_000),
          position: maximumCollectionQueryBytes + 1,
        }),
        { status: 422 },
      ),
    )
    try {
      await collectionRequest("/api/things")
      throw new Error("expected request to fail")
    } catch (error) {
      expect(error).toBeInstanceOf(CollectionAPIError)
      expect(error).toMatchObject({ code: undefined, position: undefined })
      expect(collectionQueryByteLength((error as Error).message)).toBe(1024)
    }
  })

  it("passes abort signals through and supports empty success responses", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      new Response(null, { status: 204 }),
    )
    const controller = new AbortController()
    await expect(
      collectionRequest<void>(
        "/api/things",
        { method: "DELETE" },
        controller.signal,
      ),
    ).resolves.toBeUndefined()
    expect(mockedLauncherFetch).toHaveBeenCalledWith("/api/things", {
      method: "DELETE",
      signal: controller.signal,
    })
  })

  it("projects a closed, bounded collection query schema", () => {
    expect(
      projectCollectionQuerySchema(
        {
          fields: [
            {
              name: "status",
              type: "enum",
              operators: ["=", "!=", "IN", "NOT IN"],
              sortable: true,
              suggested_values: ["ready", "running"],
            },
            {
              name: "count",
              type: "number",
              operators: ["=", ">", "<="],
              sortable: true,
            },
          ],
          default_order: [{ field: "status", direction: "ASC" }],
        },
        [{ field: "status", direction: "ASC" }],
      ),
    ).toEqual({
      fields: [
        {
          name: "status",
          type: "enum",
          operators: ["=", "!=", "IN", "NOT IN"],
          sortable: true,
          suggested_values: ["ready", "running"],
        },
        {
          name: "count",
          type: "number",
          operators: ["=", ">", "<="],
          sortable: true,
        },
      ],
    })
  })

  it.each([
    [
      "wrong order",
      (schema: QuerySchemaFixture) => {
        schema.default_order[0] = { field: "status", direction: "DESC" }
      },
    ],
    [
      "duplicate field",
      (schema: QuerySchemaFixture) => {
        schema.fields.push({ ...schema.fields[0]! })
      },
    ],
    [
      "type-incompatible operator",
      (schema: QuerySchemaFixture) => {
        schema.fields[0]!.operators = [">"]
      },
    ],
    [
      "duplicate operator",
      (schema: QuerySchemaFixture) => {
        schema.fields[0]!.operators = ["=", "="]
      },
    ],
    [
      "noncanonical field",
      (schema: QuerySchemaFixture) => {
        schema.fields[0]!.name = "Status"
      },
    ],
    [
      "untrimmed suggestion",
      (schema: QuerySchemaFixture) => {
        schema.fields[0]!.suggested_values = [" ready"]
      },
    ],
    [
      "duplicate suggestion",
      (schema: QuerySchemaFixture) => {
        schema.fields[0]!.suggested_values = ["ready", "READY"]
      },
    ],
    [
      "control suggestion",
      (schema: QuerySchemaFixture) => {
        schema.fields[0]!.suggested_values = ["ready\nnow"]
      },
    ],
  ])("rejects a %s in collection query schemas", (_name, mutate) => {
    const schema = querySchemaFixture()
    mutate(schema)
    expect(() =>
      projectCollectionQuerySchema(schema, [
        { field: "status", direction: "ASC" },
      ]),
    ).toThrow(
      expect.objectContaining({ code: "malformed_response", status: 502 }),
    )
  })
})

interface QuerySchemaFixture {
  fields: Array<{
    name: string
    type: string
    operators: string[]
    sortable: boolean
    suggested_values?: string[]
  }>
  default_order: Array<{ field: string; direction: "ASC" | "DESC" }>
}

function querySchemaFixture(): QuerySchemaFixture {
  return {
    fields: [
      {
        name: "status",
        type: "enum",
        operators: ["=", "!="],
        sortable: true,
        suggested_values: ["ready"],
      },
    ],
    default_order: [{ field: "status", direction: "ASC" }],
  }
}
