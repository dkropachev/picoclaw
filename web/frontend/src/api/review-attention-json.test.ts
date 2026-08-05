import { describe, expect, it } from "vitest"

import {
  type ExactJSONValue,
  cloneExactJSON,
  createExactJSONObject,
  isExactJSONNumber,
  isExactJSONObject,
  parseExactJSON,
  stringifyExactJSON,
  trimGoSpace,
} from "@/api/review-attention-json"

describe("review attention exact JSON", () => {
  it("preserves every numeric token without converting through a browser number", () => {
    const source =
      '{"large":9007199254740993,"negative":-9007199254740993,"decimal":1.2300e+400,"negative_zero":-0,"scaled_zero":0e400}'
    const parsed = parseExactJSON(source)

    expect(stringifyExactJSON(parsed)).toBe(source)
    if (!isExactJSONObject(parsed)) throw new Error("object expected")
    expect(isExactJSONNumber(parsed.large)).toBe(true)
    if (!isExactJSONNumber(parsed.large)) throw new Error("number expected")
    expect(parsed.large.source).toBe("9007199254740993")
  })

  it("retains prototype-shaped and case-distinct keys as own members", () => {
    const source =
      '{"__proto__":{"polluted":true},"constructor":1,"prototype":2,"Foo":"one","foo":"two"}'
    const parsed = parseExactJSON(source)

    expect(isExactJSONObject(parsed)).toBe(true)
    if (!isExactJSONObject(parsed)) throw new Error("object expected")
    expect(Object.getPrototypeOf(parsed)).toBeNull()
    expect(Object.hasOwn(parsed, "__proto__")).toBe(true)
    expect(Object.hasOwn(parsed, "constructor")).toBe(true)
    expect(Object.keys(parsed)).toEqual([
      "__proto__",
      "constructor",
      "prototype",
      "Foo",
      "foo",
    ])
    expect(stringifyExactJSON(parsed)).toBe(source)
    expect(({} as { polluted?: boolean }).polluted).toBeUndefined()
  })

  it("clones the branded tree without relying on structuredClone", () => {
    const original = parseExactJSON(
      '{"nested":[9007199254740993,{"__proto__":"kept"}]}',
    )
    const cloned = cloneExactJSON(original)

    expect(cloned).not.toBe(original)
    expect(stringifyExactJSON(cloned)).toBe(
      '{"nested":[9007199254740993,{"__proto__":"kept"}]}',
    )
  })

  it("rejects duplicate keys but permits keys that differ only by case", () => {
    expect(() => parseExactJSON('{"key":1,"key":1}')).toThrow(
      /Duplicate JSON key/,
    )
    expect(() => parseExactJSON('{"Key":1,"key":2}')).not.toThrow()
  })

  it("rejects malformed syntax, non-JSON whitespace, and trailing input", () => {
    for (const source of [
      "",
      "01",
      "1.",
      "1e",
      "[1,]",
      '{"a":1,}',
      "true false",
      "\u00a0null",
      '"unterminated',
      '"bad\\xescape"',
    ]) {
      expect(() => parseExactJSON(source), source).toThrow(SyntaxError)
    }
  })

  it("rejects lone surrogates while accepting a valid surrogate pair", () => {
    expect(() => parseExactJSON('"\\ud800"')).toThrow(/Unicode scalar/)
    expect(() => parseExactJSON('"\\udc00"')).toThrow(/Unicode scalar/)
    expect(parseExactJSON('"\\ud83d\\ude00"')).toBe("😀")
    expect(() => stringifyExactJSON("\ud800")).toThrow(/Unicode scalar/)
  })

  it("enforces encoded-byte, nesting, and node limits at their boundaries", () => {
    expect(parseExactJSON('"é"', { maximumBytes: 4 })).toBe("é")
    expect(() => parseExactJSON('"é"', { maximumBytes: 3 })).toThrow(
      /byte limit/,
    )
    expect(() => parseExactJSON("[[]]", { maximumDepth: 0 })).toThrow(
      /nesting limit/,
    )
    expect(() => parseExactJSON("[true,false]", { maximumNodes: 2 })).toThrow(
      /node limit/,
    )
    expect(parseExactJSON("[true,false]", { maximumNodes: 3 })).toBeDefined()
  })

  it("rejects foreign-prototype and cyclic objects during serialization", () => {
    expect(() =>
      stringifyExactJSON({ value: "unsafe prototype" } as ExactJSONValue),
    ).toThrow(/null prototype/)

    const cyclic = createExactJSONObject()
    cyclic.self = cyclic
    expect(() => stringifyExactJSON(cyclic)).toThrow(/Cyclic/)
    expect(() => cloneExactJSON(cyclic)).toThrow(/Cyclic/)
  })

  it("constructs safe objects with dangerous keys", () => {
    const value = createExactJSONObject([
      ["__proto__", "safe"],
      ["constructor", parseExactJSON("9007199254740993")],
    ])

    expect(Object.getPrototypeOf(value)).toBeNull()
    expect(stringifyExactJSON(value)).toBe(
      '{"__proto__":"safe","constructor":9007199254740993}',
    )
    expect(() =>
      createExactJSONObject([
        ["same", true],
        ["same", false],
      ]),
    ).toThrow(/Duplicate JSON key/)
  })

  it("matches Go strings.TrimSpace instead of ECMAScript trim edges", () => {
    expect(trimGoSpace("\u0085 value \u0085")).toBe("value")
    expect(trimGoSpace("\ufeffvalue\ufeff")).toBe("\ufeffvalue\ufeff")
  })
})
