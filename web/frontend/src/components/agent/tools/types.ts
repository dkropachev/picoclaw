import type {
  ThreadPolicyConfig,
  ToolAdaptationConfig,
  ToolSupportItem,
  WebSearchConfigResponse,
} from "@/api/tools"

export type ToolsPageTab =
  | "library"
  | "web-search"
  | "thread-policy"
  | "adaptation"
export type ToolStatusFilter = "all" | ToolSupportItem["status"]
export type GroupedTools = Array<[string, ToolSupportItem[]]>

export type WebSearchDraftUpdater = (
  updater: (current: WebSearchConfigResponse) => WebSearchConfigResponse,
) => void

export type ThreadPolicyDraftUpdater = (
  updater: (current: ThreadPolicyConfig) => ThreadPolicyConfig,
) => void

export type ToolAdaptationDraftUpdater = (
  updater: (current: ToolAdaptationConfig) => ToolAdaptationConfig,
) => void
