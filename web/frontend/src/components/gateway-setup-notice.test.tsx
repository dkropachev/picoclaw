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

  it("directs the user to model configuration when startup is blocked", () => {
    getDefaultStore().set(gatewayAtom, {
      status: "stopped",
      canStart: false,
      startReason: "no model configured",
      restartRequired: false,
    })

    render(<GatewaySetupNotice />)

    expect(screen.getByRole("status")).toHaveTextContent(
      "Gateway setup required: no model configured.",
    )
    expect(
      screen.getByRole("link", { name: "Configure models" }),
    ).toHaveAttribute("href", "/models")
  })

  it("stays hidden when the gateway can start", () => {
    render(<GatewaySetupNotice />)

    expect(screen.queryByRole("status")).not.toBeInTheDocument()
  })
})
