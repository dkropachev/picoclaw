const exactJSONNumberBrand = Symbol("review-attention-exact-json-number")

export interface ExactJSONNumber {
  readonly source: string
  readonly [exactJSONNumberBrand]: true
}

export interface ExactJSONObject {
  [key: string]: ExactJSONValue
}

export type ExactJSONValue =
  | null
  | boolean
  | string
  | ExactJSONNumber
  | ExactJSONValue[]
  | ExactJSONObject

export interface ExactJSONLimits {
  maximumBytes: number
  maximumDepth: number
  maximumNodes: number
}

const defaultExactJSONLimits: ExactJSONLimits = {
  maximumBytes: (1 << 20) + (64 << 10),
  maximumDepth: 128,
  maximumNodes: (1 << 20) + (64 << 10),
}

const jsonNumberPattern = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/
const completeJSONNumberPattern =
  /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?$/
// Go's strings.TrimSpace follows unicode.IsSpace. ECMAScript String.trim has
// two observable differences: it omits U+0085 and includes U+FEFF. Policy
// validation must use the server's exact whitespace boundary.
export function trimGoSpace(value: string): string {
  let start = 0
  while (start < value.length && isGoSpace(value.charCodeAt(start))) start += 1
  let end = value.length
  while (end > start && isGoSpace(value.charCodeAt(end - 1))) end -= 1
  return value.slice(start, end)
}

function isGoSpace(code: number): boolean {
  return (
    (code >= 0x0009 && code <= 0x000d) ||
    code === 0x0020 ||
    code === 0x0085 ||
    code === 0x00a0 ||
    code === 0x1680 ||
    (code >= 0x2000 && code <= 0x200a) ||
    code === 0x2028 ||
    code === 0x2029 ||
    code === 0x202f ||
    code === 0x205f ||
    code === 0x3000
  )
}

export function parseExactJSON(
  source: string,
  overrides: Partial<ExactJSONLimits> = {},
): ExactJSONValue {
  const limits = exactJSONLimits(overrides)
  if (new TextEncoder().encode(source).byteLength > limits.maximumBytes) {
    throw new SyntaxError("JSON exceeds the encoded byte limit")
  }

  let index = 0
  let nodes = 0

  const skipWhitespace = () => {
    while (isJSONWhitespace(source[index])) {
      index += 1
    }
  }

  const readString = (): string => {
    if (source[index] !== '"') {
      throw new SyntaxError("JSON string expected")
    }
    const start = index
    index += 1
    while (index < source.length) {
      const code = source.charCodeAt(index)
      if (code === 0x22) {
        index += 1
        let value: unknown
        try {
          value = JSON.parse(source.slice(start, index)) as unknown
        } catch {
          throw new SyntaxError("Invalid JSON string")
        }
        if (typeof value !== "string" || hasUnpairedSurrogate(value)) {
          throw new SyntaxError("Invalid JSON Unicode scalar")
        }
        return value
      }
      if (code < 0x20) {
        throw new SyntaxError("Invalid JSON string character")
      }
      if (code !== 0x5c) {
        index += 1
        continue
      }

      index += 1
      if (index >= source.length) {
        throw new SyntaxError("Unterminated JSON escape")
      }
      const escaped = source[index]
      if ('"\\/bfnrt'.includes(escaped)) {
        index += 1
        continue
      }
      if (escaped !== "u" || index + 4 >= source.length) {
        throw new SyntaxError("Invalid JSON escape")
      }
      for (let offset = 1; offset <= 4; offset += 1) {
        if (!/[0-9a-fA-F]/.test(source[index + offset] ?? "")) {
          throw new SyntaxError("Invalid JSON Unicode escape")
        }
      }
      index += 5
    }
    throw new SyntaxError("Unterminated JSON string")
  }

  const readValue = (depth: number): ExactJSONValue => {
    skipWhitespace()
    if (depth > limits.maximumDepth) {
      throw new SyntaxError("JSON exceeds the nesting limit")
    }
    nodes += 1
    if (nodes > limits.maximumNodes) {
      throw new SyntaxError("JSON exceeds the node limit")
    }

    const character = source[index]
    if (character === '"') {
      return readString()
    }
    if (character === "{") {
      index += 1
      const object = createExactJSONObject()
      skipWhitespace()
      if (source[index] === "}") {
        index += 1
        return object
      }
      while (index < source.length) {
        skipWhitespace()
        const key = readString()
        if (Object.hasOwn(object, key)) {
          throw new SyntaxError(`Duplicate JSON key ${JSON.stringify(key)}`)
        }
        skipWhitespace()
        if (source[index] !== ":") {
          throw new SyntaxError("JSON object colon expected")
        }
        index += 1
        const value = readValue(depth + 1)
        defineExactJSONMember(object, key, value)
        skipWhitespace()
        if (source[index] === "}") {
          index += 1
          return object
        }
        if (source[index] !== ",") {
          throw new SyntaxError("JSON object comma expected")
        }
        index += 1
      }
      throw new SyntaxError("Unterminated JSON object")
    }
    if (character === "[") {
      index += 1
      const values: ExactJSONValue[] = []
      skipWhitespace()
      if (source[index] === "]") {
        index += 1
        return values
      }
      while (index < source.length) {
        values.push(readValue(depth + 1))
        skipWhitespace()
        if (source[index] === "]") {
          index += 1
          return values
        }
        if (source[index] !== ",") {
          throw new SyntaxError("JSON array comma expected")
        }
        index += 1
      }
      throw new SyntaxError("Unterminated JSON array")
    }
    for (const [literal, value] of [
      ["true", true],
      ["false", false],
      ["null", null],
    ] as const) {
      if (source.startsWith(literal, index)) {
        index += literal.length
        return value
      }
    }
    const match = source.slice(index).match(jsonNumberPattern)
    if (match == null) {
      throw new SyntaxError("JSON value expected")
    }
    index += match[0].length
    return createExactJSONNumber(match[0])
  }

  const value = readValue(0)
  skipWhitespace()
  if (index !== source.length) {
    throw new SyntaxError("Trailing JSON input")
  }
  return value
}

export function stringifyExactJSON(
  value: ExactJSONValue,
  overrides: Partial<ExactJSONLimits> = {},
): string {
  const limits = exactJSONLimits(overrides)
  const encoder = new TextEncoder()
  const chunks: string[] = []
  const active = new WeakSet<object>()
  let bytes = 0
  let nodes = 0

  const append = (chunk: string) => {
    bytes += encoder.encode(chunk).byteLength
    if (bytes > limits.maximumBytes) {
      throw new TypeError("JSON exceeds the encoded byte limit")
    }
    chunks.push(chunk)
  }

  const write = (item: ExactJSONValue, depth: number) => {
    if (depth > limits.maximumDepth) {
      throw new TypeError("JSON exceeds the nesting limit")
    }
    nodes += 1
    if (nodes > limits.maximumNodes) {
      throw new TypeError("JSON exceeds the node limit")
    }

    if (item === null) {
      append("null")
      return
    }
    if (typeof item === "boolean") {
      append(item ? "true" : "false")
      return
    }
    if (typeof item === "string") {
      if (hasUnpairedSurrogate(item)) {
        throw new TypeError("Invalid JSON Unicode scalar")
      }
      append(JSON.stringify(item))
      return
    }
    if (isExactJSONNumber(item)) {
      if (!completeJSONNumberPattern.test(item.source)) {
        throw new TypeError("Invalid exact JSON number")
      }
      append(item.source)
      return
    }
    if (Array.isArray(item)) {
      if (active.has(item)) {
        throw new TypeError("Cyclic JSON value")
      }
      active.add(item)
      append("[")
      for (let itemIndex = 0; itemIndex < item.length; itemIndex += 1) {
        if (itemIndex > 0) append(",")
        write(item[itemIndex] as ExactJSONValue, depth + 1)
      }
      append("]")
      active.delete(item)
      return
    }
    if (!isExactJSONObject(item)) {
      throw new TypeError("JSON objects must have a null prototype")
    }
    if (active.has(item)) {
      throw new TypeError("Cyclic JSON value")
    }
    active.add(item)
    append("{")
    const keys = Object.keys(item)
    for (let keyIndex = 0; keyIndex < keys.length; keyIndex += 1) {
      const key = keys[keyIndex]
      if (hasUnpairedSurrogate(key)) {
        throw new TypeError("Invalid JSON object key")
      }
      if (keyIndex > 0) append(",")
      append(JSON.stringify(key))
      append(":")
      write(item[key] as ExactJSONValue, depth + 1)
    }
    append("}")
    active.delete(item)
  }

  write(value, 0)
  return chunks.join("")
}

export function createExactJSONObject(
  entries: Iterable<readonly [string, ExactJSONValue]> = [],
): ExactJSONObject {
  const object = Object.create(null) as ExactJSONObject
  for (const [key, value] of entries) {
    if (Object.hasOwn(object, key)) {
      throw new TypeError(`Duplicate JSON key ${JSON.stringify(key)}`)
    }
    if (hasUnpairedSurrogate(key)) {
      throw new TypeError("Invalid JSON object key")
    }
    defineExactJSONMember(object, key, value)
  }
  return object
}

export function cloneExactJSON(value: ExactJSONValue): ExactJSONValue {
  const active = new WeakSet<object>()

  const clone = (item: ExactJSONValue): ExactJSONValue => {
    if (
      item === null ||
      typeof item === "boolean" ||
      typeof item === "string"
    ) {
      return item
    }
    if (isExactJSONNumber(item)) {
      return createExactJSONNumber(item.source)
    }
    if (active.has(item)) {
      throw new TypeError("Cyclic JSON value")
    }
    active.add(item)
    let cloned: ExactJSONValue
    if (Array.isArray(item)) {
      cloned = item.map(clone)
    } else {
      if (!isExactJSONObject(item)) {
        throw new TypeError("JSON objects must have a null prototype")
      }
      cloned = createExactJSONObject(
        Object.keys(item).map((key) => [key, clone(item[key])] as const),
      )
    }
    active.delete(item)
    return cloned
  }

  return clone(value)
}

export function isExactJSONObject(value: unknown): value is ExactJSONObject {
  return (
    typeof value === "object" &&
    value !== null &&
    !Array.isArray(value) &&
    !isExactJSONNumber(value) &&
    Object.getPrototypeOf(value) === null
  )
}

export function isExactJSONNumber(value: unknown): value is ExactJSONNumber {
  return (
    typeof value === "object" &&
    value !== null &&
    (value as Partial<ExactJSONNumber>)[exactJSONNumberBrand] === true &&
    typeof (value as Partial<ExactJSONNumber>).source === "string"
  )
}

function createExactJSONNumber(source: string): ExactJSONNumber {
  if (!completeJSONNumberPattern.test(source)) {
    throw new SyntaxError("Invalid JSON number")
  }
  const value = Object.create(null) as ExactJSONNumber
  Object.defineProperties(value, {
    source: { value: source, enumerable: true },
    [exactJSONNumberBrand]: { value: true },
  })
  return Object.freeze(value)
}

function defineExactJSONMember(
  object: ExactJSONObject,
  key: string,
  value: ExactJSONValue,
) {
  Object.defineProperty(object, key, {
    value,
    enumerable: true,
    configurable: true,
    writable: true,
  })
}

function exactJSONLimits(overrides: Partial<ExactJSONLimits>): ExactJSONLimits {
  const limits = { ...defaultExactJSONLimits, ...overrides }
  for (const value of [
    limits.maximumBytes,
    limits.maximumDepth,
    limits.maximumNodes,
  ]) {
    if (!Number.isSafeInteger(value) || value < 0) {
      throw new TypeError("Invalid exact JSON limit")
    }
  }
  return limits
}

function isJSONWhitespace(value: string | undefined): boolean {
  return value === " " || value === "\t" || value === "\n" || value === "\r"
}

function hasUnpairedSurrogate(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index)
    if (code >= 0xd800 && code <= 0xdbff) {
      const following = value.charCodeAt(index + 1)
      if (!(following >= 0xdc00 && following <= 0xdfff)) return true
      index += 1
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return true
    }
  }
  return false
}
