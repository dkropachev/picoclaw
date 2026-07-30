const jsonNumberTokenPattern = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?$/

const maximumNumberTextBytes = 256
const maximumDecimalExponent = 400

interface ExactDecimal {
  sign: -1 | 0 | 1
  coefficient: bigint
  exponent: number
}

/**
 * Matches the workflow service's browser-safety boundary for JSON numbers.
 * The token must represent exactly the same decimal value after conversion
 * through a JavaScript number, and exact integers must remain safe integers.
 */
export function workflowJSONNumberIsBrowserSafe(token: string) {
  if (
    !jsonNumberTokenPattern.test(token) ||
    token.length === 0 ||
    token.length > maximumNumberTextBytes
  ) {
    return false
  }
  const exponentIndex = token.search(/[eE]/)
  if (exponentIndex >= 0) {
    const exponentText = token.slice(exponentIndex + 1)
    const unsignedExponent =
      exponentText[0] === "+" || exponentText[0] === "-"
        ? exponentText.slice(1)
        : exponentText
    if (unsignedExponent.length === 0 || unsignedExponent.length > 4) {
      return false
    }
    const exponent = Number(exponentText)
    if (
      !Number.isInteger(exponent) ||
      exponent < -maximumDecimalExponent ||
      exponent > maximumDecimalExponent
    ) {
      return false
    }
  }

  const number = Number(token)
  if (!Number.isFinite(number)) {
    return false
  }
  const exact = exactDecimal(token)
  const roundTrip = exactDecimal(String(number))
  if (
    exact == null ||
    roundTrip == null ||
    exact.sign !== roundTrip.sign ||
    exact.coefficient !== roundTrip.coefficient ||
    exact.exponent !== roundTrip.exponent
  ) {
    return false
  }
  if (exact.sign === 0 || exact.exponent < 0) {
    return true
  }
  const digits = exact.coefficient.toString()
  if (digits.length + exact.exponent > 16) {
    return false
  }
  const integer = exact.coefficient * 10n ** BigInt(exact.exponent)
  return integer <= BigInt(Number.MAX_SAFE_INTEGER)
}

function exactDecimal(token: string): ExactDecimal | null {
  const match = token.match(/^(-?)(0|[1-9]\d*)(?:\.(\d+))?(?:[eE]([+-]?\d+))?$/)
  if (match == null) {
    return null
  }
  const fraction = match[3] ?? ""
  const parsedExponent = Number(match[4] ?? "0")
  if (!Number.isSafeInteger(parsedExponent)) {
    return null
  }
  let digits = `${match[2]}${fraction}`.replace(/^0+/, "")
  if (digits === "") {
    return { sign: 0, coefficient: 0n, exponent: 0 }
  }
  let trailingZeros = 0
  while (digits.endsWith("0")) {
    digits = digits.slice(0, -1)
    trailingZeros += 1
  }
  const exponent = parsedExponent - fraction.length + trailingZeros
  if (!Number.isSafeInteger(exponent)) {
    return null
  }
  return {
    sign: match[1] === "-" ? -1 : 1,
    coefficient: BigInt(digits),
    exponent,
  }
}
