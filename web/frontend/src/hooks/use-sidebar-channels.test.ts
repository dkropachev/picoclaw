import { describe, expect, it } from "vitest"

import type { SupportedChannel } from "@/api/channels"
import { buildChannelEnabledMap } from "@/hooks/use-sidebar-channels"

const deltaChat: SupportedChannel = {
  name: "deltachat",
  display_name: "Delta Chat",
  config_key: "deltachat",
}

const whatsAppBridge: SupportedChannel = {
  name: "whatsapp",
  display_name: "WhatsApp",
  config_key: "whatsapp",
  variant: "bridge",
}

const whatsAppNative: SupportedChannel = {
  name: "whatsapp_native",
  display_name: "WhatsApp Native",
  config_key: "whatsapp",
  variant: "native",
}

describe("buildChannelEnabledMap", () => {
  it("reads Delta Chat enablement from the current channel_list config key", () => {
    expect(
      buildChannelEnabledMap([deltaChat], {
        channel_list: {
          deltachat: {
            enabled: true,
            settings: { email: "operator@example.org" },
          },
        },
      }),
    ).toEqual({ deltachat: true })
  })

  it("retains compatibility with the legacy channels config key", () => {
    expect(
      buildChannelEnabledMap([deltaChat], {
        channels: {
          deltachat: { enabled: true },
        },
      }),
    ).toEqual({ deltachat: true })
  })

  it("selects WhatsApp Native from nested v3 channel settings", () => {
    expect(
      buildChannelEnabledMap([whatsAppBridge, whatsAppNative], {
        channel_list: {
          whatsapp: {
            enabled: true,
            use_native: false,
            settings: { use_native: true },
          },
        },
      }),
    ).toEqual({
      whatsapp: false,
      whatsapp_native: true,
    })
  })

  it("selects the WhatsApp bridge from nested v3 channel settings", () => {
    expect(
      buildChannelEnabledMap([whatsAppBridge, whatsAppNative], {
        channel_list: {
          whatsapp: {
            enabled: true,
            use_native: true,
            settings: { use_native: false },
          },
        },
      }),
    ).toEqual({
      whatsapp: true,
      whatsapp_native: false,
    })
  })

  it.each([
    {
      useNative: true,
      expected: { whatsapp: false, whatsapp_native: true },
    },
    {
      useNative: false,
      expected: { whatsapp: true, whatsapp_native: false },
    },
  ])(
    "retains flat legacy WhatsApp mode fallback when use_native is $useNative",
    ({ useNative, expected }) => {
      expect(
        buildChannelEnabledMap([whatsAppBridge, whatsAppNative], {
          channels: {
            whatsapp: {
              enabled: true,
              use_native: useNative,
            },
          },
        }),
      ).toEqual(expected)
    },
  )
})
