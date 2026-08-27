import AxeBuilder from "@axe-core/playwright"
import { type Page, expect, test } from "@playwright/test"

import {
  type CollectionVisualState,
  installCollectionVisualMocks,
  repositoryReviewVisualIDs,
} from "./fixtures/collection-api"

type VisualTheme = "light" | "dark"

const pilots = [
  { key: "accounts", route: "/accounts" },
  { key: "account-routers", route: "/accounts/routers" },
  { key: "model-aliases", route: "/models/aliases" },
  { key: "model-routers", route: "/models/routers" },
  { key: "mcp-servers", route: "/agent/mcp/servers" },
  { key: "agents", route: "/agent/agents" },
  { key: "model-evaluations", route: "/model-evaluations" },
  { key: "repository-reviews", route: "/repository-reviews" },
  {
    key: "repository-review-profiles",
    route: "/repository-reviews/profiles",
  },
  { key: "skills", route: "/agent/skills" },
  { key: "tools", route: "/agent/tools" },
] as const

for (const pilot of pilots) {
  for (const theme of ["light", "dark"] as const) {
    test(`${pilot.key} collection is stable in ${theme} theme`, async ({
      page,
    }) => {
      const errors = collectPageErrors(page)
      await openCollection(page, pilot.route, theme)
      await expect(
        page.locator('[data-slot="collection-results"]'),
      ).toBeVisible()
      await assertVisualContract(page, errors)
      await expect(page.locator("#main-content")).toHaveScreenshot(
        `${pilot.key}-list-${theme}.png`,
      )
    })
  }
}

for (const view of ["table", "grid"] as const) {
  test(`shared ${view} collection view is stable`, async ({ page }) => {
    const errors = collectPageErrors(page)
    await openCollection(page, "/models/aliases", "dark")
    await page.getByRole("button", { name: `${capitalize(view)} view` }).click()
    await expect(page).toHaveURL(new RegExp(`view=${view}`))
    await assertVisualContract(page, errors)
    await expect(page.locator("#main-content")).toHaveScreenshot(
      `shared-${view}-view.png`,
    )
  })
}

test("shared query autocomplete is stable", async ({ page }) => {
  const errors = collectPageErrors(page)
  await openCollection(page, "/models/aliases", "dark")
  await page.getByRole("combobox", { name: "Collection query" }).fill("na")
  await expect(
    page.getByRole("listbox", { name: "Collection query suggestions" }),
  ).toBeVisible()
  await assertVisualContract(page, errors)
  await expect(page.locator("#main-content")).toHaveScreenshot(
    "shared-autocomplete.png",
  )
})

test("shared explicit selection is stable", async ({ page }) => {
  const errors = collectPageErrors(page)
  await openCollection(page, "/models/aliases", "light")
  await page.locator("[data-item-id]").first().click()
  await expect(page.getByText("1 selected", { exact: true })).toBeVisible()
  await assertVisualContract(page, errors)
  await expect(page.locator("#main-content")).toHaveScreenshot(
    "shared-selection.png",
  )
})

for (const state of ["empty", "error", "loading"] as const) {
  test(`shared ${state} collection state is stable`, async ({ page }) => {
    const errors = collectPageErrors(page)
    await openCollection(page, "/models/aliases", "dark", state)
    if (state === "loading") {
      await expect(page.getByLabel("Loading collection")).toBeVisible()
    } else if (state === "error") {
      await expect(
        page
          .getByRole("alert")
          .filter({ hasText: "Collection could not be loaded" }),
      ).toBeVisible()
    } else {
      await expect(page.getByRole("status")).toBeVisible()
    }
    await assertVisualContract(
      page,
      errors,
      state === "error" ? [/status of 400 \(Bad Request\)/] : [],
    )
    await expect(page.locator("#main-content")).toHaveScreenshot(
      `shared-${state}.png`,
    )
  })
}

for (const target of [
  { state: "detail", route: "/models/aliases/code" },
  { state: "editor", route: "/models/aliases/code/edit" },
] as const) {
  test(`shared ${target.state} shell is stable`, async ({ page }) => {
    const errors = collectPageErrors(page)
    await prepareVisualPage(page, "light")
    await installCollectionVisualMocks(page)
    await page.goto(target.route)
    await expect(
      page.locator('[data-slot="collection-detail-shell"]'),
    ).toBeVisible()
    await assertVisualContract(page, errors)
    await expect(page.locator("#main-content")).toHaveScreenshot(
      `shared-${target.state}.png`,
    )
  })
}

const repositoryReviewStates = [
  {
    key: "repository-review-detail",
    route: `/repository-reviews/${repositoryReviewVisualIDs.automation}`,
  },
  {
    key: "repository-review-findings",
    route: `/repository-reviews/${repositoryReviewVisualIDs.automation}/findings?scope=current`,
  },
  {
    key: "repository-review-finding",
    route: `/repository-reviews/${repositoryReviewVisualIDs.automation}/findings/${repositoryReviewVisualIDs.finding}`,
  },
  {
    key: "repository-review-issues",
    route: `/repository-reviews/${repositoryReviewVisualIDs.automation}/issues?generation_id=${repositoryReviewVisualIDs.generation}`,
  },
  {
    key: "repository-review-issue-detail",
    route: `/repository-reviews/${repositoryReviewVisualIDs.automation}/issues/${repositoryReviewVisualIDs.issue}`,
  },
] as const

for (const target of repositoryReviewStates) {
  for (const theme of ["light", "dark"] as const) {
    test(`${target.key} is stable in ${theme} theme`, async ({ page }) => {
      const errors = collectPageErrors(page)
      await prepareVisualPage(page, theme)
      await installCollectionVisualMocks(page)
      await page.goto(target.route)
      await expect(
        page.locator('[data-slot="collection-detail-shell"]'),
      ).toBeVisible()
      await assertVisualContract(page, errors)
      await expect(page.locator("#main-content")).toHaveScreenshot(
        `${target.key}-${theme}.png`,
      )
    })
  }
}

async function openCollection(
  page: Page,
  route: string,
  theme: VisualTheme,
  state: CollectionVisualState = "ready",
) {
  await prepareVisualPage(page, theme)
  await installCollectionVisualMocks(page, state)
  await page.goto(route)
  await expect(page.locator('[data-slot="collection-shell"]')).toBeVisible()
  await expect.poll(() => new URL(page.url()).searchParams.has("q")).toBe(true)
}

async function prepareVisualPage(page: Page, theme: VisualTheme) {
  await page.clock.setFixedTime(new Date("2026-08-25T14:30:00Z"))
  await page.emulateMedia({ colorScheme: theme, reducedMotion: "reduce" })
  await page.routeWebSocket(/\/pico\/ws/, () => undefined)
  await page.addInitScript((selectedTheme) => {
    window.localStorage.setItem("theme", selectedTheme)
    window.localStorage.setItem(
      "picoclaw-tour-state",
      JSON.stringify({ currentStep: "completed", isActive: false }),
    )
    Object.defineProperty(window, "EventSource", {
      configurable: true,
      value: undefined,
    })
  }, theme)
}

async function assertVisualContract(
  page: Page,
  errors: string[],
  allowedErrors: RegExp[] = [],
) {
  await page.addStyleTag({
    content: `
      *, *::before, *::after {
        animation-delay: 0s !important;
        animation-duration: 0s !important;
        transition-delay: 0s !important;
        transition-duration: 0s !important;
        caret-color: transparent !important;
      }
    `,
  })
  await page.evaluate(async () => document.fonts.ready)
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  const unexpectedErrors = errors.filter(
    (message) => !allowedErrors.some((pattern) => pattern.test(message)),
  )
  expect(unexpectedErrors).toEqual([])
}

function collectPageErrors(page: Page) {
  const errors: string[] = []
  page.on("console", (message) => {
    if (message.type() === "error") errors.push(message.text())
  })
  page.on("pageerror", (error) => errors.push(error.message))
  return errors
}

async function expectNoHorizontalOverflow(page: Page) {
  const overflow = await page.evaluate(() => {
    const width = Math.max(
      document.documentElement.scrollWidth,
      document.body.scrollWidth,
    )
    return (
      width >
      Math.max(window.innerWidth, document.documentElement.clientWidth) + 1
    )
  })
  expect(overflow).toBe(false)
}

async function expectNoSeriousA11yViolations(page: Page) {
  const results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    .analyze()
  const blocking = results.violations.filter(
    (violation) =>
      violation.impact === "serious" || violation.impact === "critical",
  )
  expect(
    blocking.map((violation) => ({
      id: violation.id,
      impact: violation.impact,
      targets: violation.nodes.map((node) => node.target.join(" ")),
    })),
  ).toEqual([])
}

function capitalize(value: string) {
  return `${value.charAt(0).toUpperCase()}${value.slice(1)}`
}
