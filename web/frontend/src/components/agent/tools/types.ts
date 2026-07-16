import type {
  ThreadPolicyConfig,
  ToolSupportItem,
  WebSearchConfigResponse,
} from "@/api/tools"

export type ToolsPageTab = "library" | "web-search" | "thread-policy"
export type ToolStatusFilter = "all" | ToolSupportItem["status"]
export type GroupedTools = Array<[string, ToolSupportItem[]]>

export type WebSearchDraftUpdater = (
  updater: (current: WebSearchConfigResponse) => WebSearchConfigResponse,
) => void

export type ThreadPolicyDraftUpdater = (
  updater: (current: ThreadPolicyConfig) => ThreadPolicyConfig,
) => void
