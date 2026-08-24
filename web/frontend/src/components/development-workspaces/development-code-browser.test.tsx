import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { useState } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  getDevelopmentCodeBlob,
  getDevelopmentCodeDiff,
  getDevelopmentCodeTree,
} from "@/api/development-workspaces"
import { DevelopmentCodeBrowser } from "@/components/development-workspaces/development-code-browser"

vi.mock("@/api/development-workspaces", async (importOriginal) => {
  const original =
    await importOriginal<typeof import("@/api/development-workspaces")>()
  return {
    ...original,
    getDevelopmentCodeBlob: vi.fn(),
    getDevelopmentCodeDiff: vi.fn(),
    getDevelopmentCodeTree: vi.fn(),
  }
})
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("@/hooks/use-theme", () => ({
  useTheme: () => ({ theme: "dark", toggleTheme: vi.fn() }),
}))
vi.mock("@/components/development-workspaces/monaco-read-only-viewer", () => ({
  default: ({ path }: { path: string }) => (
    <div data-testid="mock-monaco">Monaco: {path}</div>
  ),
}))

const mockedBlob = vi.mocked(getDevelopmentCodeBlob)
const mockedDiff = vi.mocked(getDevelopmentCodeDiff)
const mockedTree = vi.mocked(getDevelopmentCodeTree)
const workspaceID = `devw_${"1".repeat(32)}`

function Harness() {
  const [path, setPath] = useState<string>()
  return (
    <DevelopmentCodeBrowser
      workspaceID={workspaceID}
      candidateRevision="candidate:2"
      selectedPath={path}
      onSelectPath={setPath}
      changedFiles={["src/retry.ts"]}
    />
  )
}

function renderBrowser() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={queryClient}>
      <Harness />
    </QueryClientProvider>,
  )
}

describe("development code browser", () => {
  beforeEach(() => {
    mockedBlob.mockReset()
    mockedDiff.mockReset()
    mockedTree.mockReset()
    mockedBlob.mockResolvedValue({
      revision: "candidate:2",
      path: "src/retry.ts",
      content: "const retry = true\n",
      language: "typescript",
      truncated: false,
    })
    mockedDiff.mockResolvedValue({
      base_revision: "base:1",
      candidate_revision: "candidate:2",
      path: "src/retry.ts",
      original: "const retry = false\n",
      modified: "const retry = true\n",
      language: "typescript",
      unified_diff: "@@ -1 +1 @@\n-const retry = false\n+const retry = true",
    })
  })

  it("opens changed files in lazy read-only Monaco and offers text fallback", async () => {
    const user = userEvent.setup()
    renderBrowser()

    await user.click(screen.getByRole("button", { name: "src/retry.ts" }))
    expect(await screen.findByTestId("mock-monaco")).toHaveTextContent(
      "Monaco: src/retry.ts",
    )
    expect(mockedDiff).toHaveBeenCalledWith(
      workspaceID,
      { revision: "candidate:2", path: "src/retry.ts" },
      expect.any(AbortSignal),
    )

    await user.click(
      screen.getByRole("button", { name: "Accessible text view" }),
    )
    expect(
      (
        screen.getByLabelText(
          "Read-only text view for src/retry.ts",
        ) as HTMLTextAreaElement
      ).value,
    ).toContain("-const retry = false")
  })

  it("does not request repository content before a file is selected", () => {
    renderBrowser()
    expect(mockedTree).not.toHaveBeenCalled()
    expect(mockedBlob).not.toHaveBeenCalled()
    expect(mockedDiff).not.toHaveBeenCalled()
  })
})
