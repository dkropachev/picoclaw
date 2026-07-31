import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"
import { updateModel, updateModelAlias } from "@/api/models"

vi.mock("@/api/http", () => ({
  launcherFetch: vi.fn(),
}))

const mockedLauncherFetch = vi.mocked(launcherFetch)

describe("models API", () => {
  beforeEach(() => {
    mockedLauncherFetch.mockReset()
  })

  it("sends the GET-provided revision with indexed model updates", async () => {
    mockedLauncherFetch.mockResolvedValue(
      new Response(JSON.stringify({ status: "ok" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    )

    await updateModel(4, "revision / two", {
      model_name: "openai-work",
      provider: "openai",
      model: "gpt-5.4",
    })

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/accounts/models/4?revision=revision%20%2F%20two",
      {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          model_name: "openai-work",
          provider: "openai",
          model: "gpt-5.4",
        }),
      },
    )
  })

  it("sends the GET-provided revision with indexed alias updates", async () => {
    mockedLauncherFetch.mockResolvedValue(
      new Response(JSON.stringify({ status: "ok" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    )

    await updateModelAlias(2, "alias revision/3", {
      name: "coding",
      model: "openai/gpt-5.4",
    })

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/accounts/model-aliases/2?revision=alias%20revision%2F3",
      {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: "coding",
          model: "openai/gpt-5.4",
        }),
      },
    )
  })
})
