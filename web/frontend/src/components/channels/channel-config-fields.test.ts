import { describe, expect, it } from "vitest"

import {
  buildEditConfig,
  getFieldValueForValidation,
  getRequiredFieldKeys,
  getSecretInputPlaceholder,
} from "@/components/channels/channel-config-fields"

describe("Delta Chat channel configuration fields", () => {
  it("requires email and treats the legacy password as presence-only", () => {
    const editConfig = buildEditConfig("deltachat", {
      email: "operator@example.org",
      display_name: "Operations",
    })

    expect(getRequiredFieldKeys("deltachat")).toEqual(["email"])
    expect(editConfig).toMatchObject({
      allow_from: [],
      group_trigger: {
        mention_only: true,
        prefixes: [],
      },
      typing: {
        enabled: false,
      },
      placeholder: {
        enabled: false,
        text: [],
      },
      email: "operator@example.org",
      display_name: "Operations",
      avatar_image: "",
      data_dir: "",
      rpc_server_path: "",
      invite_link: "",
      allow_crosspost: false,
      imap_server: "",
      imap_port: 0,
      smtp_server: "",
      smtp_port: 0,
      password: "",
      _password: "",
    })
    expect(
      getFieldValueForValidation(editConfig, ["password"], "password"),
    ).toBe(true)
    expect(
      getSecretInputPlaceholder(
        ["password"],
        "password",
        "Configured",
        "Not configured",
      ),
    ).toBe("Configured")
  })

  it("uses a replacement password without exposing the configured value", () => {
    const editConfig = buildEditConfig("deltachat", {
      email: "operator@example.org",
    })
    editConfig._password = "replacement-password"

    expect(
      getFieldValueForValidation(editConfig, ["password"], "password"),
    ).toBe("replacement-password")
    expect(editConfig.password).toBe("")
  })
})
