import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import type { MCPServer } from "@/api/mcp"

import { MCPServerCard } from "./mcp-server-card"

const oauthServer: MCPServer = {
  name: "github",
  enabled: true,
  deferred: null,
  type: "http",
  url: "https://example.com/mcp",
  command: "",
  args: [],
  env_file: "",
  env_keys: [],
  header_keys: [],
  auth: { type: "oauth", configured: true },
}

describe("MCPServerCard", () => {
  it("offers reconnect for OAuth and login when a remote probe needs auth", async () => {
    const user = userEvent.setup()
    const onLogin = vi.fn()
    const { rerender } = renderCard(oauthServer, onLogin)

    await user.click(screen.getByRole("button", { name: "Reconnect" }))
    expect(onLogin).toHaveBeenCalledOnce()

    rerender(
      card(
        {
          ...oauthServer,
          auth: { type: "none", configured: false },
        },
        onLogin,
        {
          ok: false,
          tool_count: 0,
          tools: [],
          auth_required: true,
        },
      ),
    )

    expect(screen.getByRole("button", { name: "Log in" })).toBeVisible()
  })

  it("offers header editing instead of OAuth for custom authentication", () => {
    const onEdit = vi.fn()
    render(
      <MCPServerCard
        server={{
          ...oauthServer,
          header_keys: ["X-API-Key"],
          auth: { type: "custom", configured: true },
        }}
        probe={{
          ok: false,
          tool_count: 0,
          tools: [],
          auth_required: true,
        }}
        testing={false}
        toggling={false}
        loggingIn={false}
        onTest={vi.fn()}
        onLogin={vi.fn()}
        onToggle={vi.fn()}
        onEdit={onEdit}
        onDelete={vi.fn()}
      />,
    )

    expect(screen.getByRole("button", { name: "Update headers" })).toBeVisible()
    expect(
      screen.queryByRole("button", { name: "Log in" }),
    ).not.toBeInTheDocument()
  })
})

function renderCard(server: MCPServer, onLogin: () => void) {
  return render(card(server, onLogin))
}

function card(
  server: MCPServer,
  onLogin: () => void,
  probe?: {
    ok: boolean
    tool_count: number
    tools: []
    auth_required: boolean
  },
) {
  return (
    <MCPServerCard
      server={server}
      probe={probe}
      testing={false}
      toggling={false}
      loggingIn={false}
      onTest={vi.fn()}
      onLogin={onLogin}
      onToggle={vi.fn()}
      onEdit={vi.fn()}
      onDelete={vi.fn()}
    />
  )
}
