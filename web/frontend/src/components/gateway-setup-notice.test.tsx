import { render, screen } from "@testing-library/react"
import { getDefaultStore } from "jotai"
import type { AnchorHTMLAttributes, ReactNode } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { GatewaySetupNotice } from "@/components/gateway-setup-notice"
import { gatewayAtom } from "@/store/gateway"

vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    to,
    ...props
  }: {
    children: ReactNode
    to: string
  } & AnchorHTMLAttributes<HTMLAnchorElement>) => (
    <a {...props} href={to}>
      {children}
    </a>
  ),
}))

describe("GatewaySetupNotice", () => {
  beforeEach(() => {
    getDefaultStore().set(gatewayAtom, {
      status: "stopped",
      canStart: true,
      restartRequired: false,
    })
  })

  it("directs the user to model configuration while the gateway is running", () => {
    getDefaultStore().set(gatewayAtom, {
      status: "running",
      canStart: true,
      modelSetupRequired: true,
      modelSetupReason: 'model alias "chat" is not configured',
      restartRequired: false,
    })

    render(<GatewaySetupNotice />)

    expect(screen.getByRole("status")).toHaveTextContent(
      'Model setup required: model alias "chat" is not configured.',
    )
    expect(
      screen.getByRole("link", { name: "Configure models" }),
    ).toHaveAttribute("href", "/models/aliases")
  })

  it("stays hidden when model setup is complete", () => {
    render(<GatewaySetupNotice />)

    expect(screen.queryByRole("status")).not.toBeInTheDocument()
  })
})
