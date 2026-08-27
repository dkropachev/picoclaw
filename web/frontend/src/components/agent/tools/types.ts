import type {
  ThreadPolicyConfig,
  ToolAdaptationConfig,
  WebSearchConfigResponse,
} from "@/api/tools"

export type WebSearchDraftUpdater = (
  updater: (current: WebSearchConfigResponse) => WebSearchConfigResponse,
) => void

export type ThreadPolicyDraftUpdater = (
  updater: (current: ThreadPolicyConfig) => ThreadPolicyConfig,
) => void

export type ToolAdaptationDraftUpdater = (
  updater: (current: ToolAdaptationConfig) => ToolAdaptationConfig,
) => void
