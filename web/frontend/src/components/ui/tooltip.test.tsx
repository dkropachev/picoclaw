import { render, screen } from "@testing-library/react"
import { describe, expect, it } from "vitest"

import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip"

describe("TooltipContent", () => {
  it("uses an explicit high-contrast color pair inside its portal", async () => {
    render(
      <TooltipProvider>
        <Tooltip defaultOpen>
          <TooltipTrigger>Details</TooltipTrigger>
          <TooltipContent>Helpful context</TooltipContent>
        </Tooltip>
      </TooltipProvider>,
    )

    await screen.findByRole("tooltip")
    const content = document.querySelector('[data-slot="tooltip-content"]')
    expect(content).toHaveClass("bg-neutral-950", "text-neutral-50")
    expect(content).not.toHaveClass("text-background")
  })
})
