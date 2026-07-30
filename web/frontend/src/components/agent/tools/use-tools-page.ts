import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useDeferredValue, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type ThreadPolicyConfig,
  type ToolAdaptationConfig,
  type ToolAdaptationProbeTarget,
  type WebSearchConfigResponse,
  getThreadPolicy,
  getToolAdaptation,
  getTools,
  getWebSearchConfig,
  runToolAdaptationProbe,
  setToolEnabled,
  updateThreadPolicy,
  updateToolAdaptation,
  updateWebSearchConfig,
} from "@/api/tools"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

import type { GroupedTools, ToolStatusFilter, ToolsPageTab } from "./types"

export function useToolsPage({
  activeTab,
  onTabChange,
}: {
  activeTab: ToolsPageTab
  onTabChange: (tab: ToolsPageTab) => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [searchQuery, setSearchQuery] = useState("")
  const deferredSearchQuery = useDeferredValue(searchQuery)
  const [statusFilter, setStatusFilter] = useState<ToolStatusFilter>("all")
  const [expandedProvider, setExpandedProvider] = useState<string | null>(null)
  const [webSearchDraftOverride, setWebSearchDraftOverride] =
    useState<WebSearchConfigResponse | null>(null)
  const [threadPolicyDraftOverride, setThreadPolicyDraftOverride] =
    useState<ThreadPolicyConfig | null>(null)
  const [toolAdaptationDraftOverride, setToolAdaptationDraftOverride] =
    useState<ToolAdaptationConfig | null>(null)

  const toolsQuery = useQuery({
    queryKey: ["tools"],
    queryFn: getTools,
  })
  const webSearchQuery = useQuery({
    queryKey: ["tools", "web-search-config"],
    queryFn: getWebSearchConfig,
  })
  const threadPolicyQuery = useQuery({
    queryKey: ["tools", "thread-policy"],
    queryFn: getThreadPolicy,
  })
  const toolAdaptationQuery = useQuery({
    queryKey: ["tools", "adaptation"],
    queryFn: getToolAdaptation,
  })

  const tools = useMemo(
    () => toolsQuery.data?.tools ?? [],
    [toolsQuery.data?.tools],
  )
  const normalizedSearchQuery = deferredSearchQuery.trim().toLowerCase()
  const webSearchDraft = webSearchDraftOverride ?? webSearchQuery.data ?? null
  const threadPolicyDraft =
    threadPolicyDraftOverride ?? threadPolicyQuery.data ?? null
  const toolAdaptationDraft =
    toolAdaptationDraftOverride ?? toolAdaptationQuery.data ?? null
  const isWebSearchDirty = useMemo(() => {
    if (!webSearchDraft || !webSearchQuery.data) {
      return false
    }
    return (
      JSON.stringify(webSearchDraft) !== JSON.stringify(webSearchQuery.data)
    )
  }, [webSearchDraft, webSearchQuery.data])
  const isThreadPolicyDirty = useMemo(() => {
    if (!threadPolicyDraft || !threadPolicyQuery.data) {
      return false
    }
    return (
      JSON.stringify(threadPolicyDraft) !==
      JSON.stringify(threadPolicyQuery.data)
    )
  }, [threadPolicyDraft, threadPolicyQuery.data])
  const isToolAdaptationDirty = useMemo(() => {
    if (!toolAdaptationDraft || !toolAdaptationQuery.data) {
      return false
    }
    return (
      JSON.stringify(toolAdaptationDraft) !==
      JSON.stringify(toolAdaptationQuery.data)
    )
  }, [toolAdaptationDraft, toolAdaptationQuery.data])

  const toggleToolMutation = useMutation({
    mutationFn: async ({ name, enabled }: { name: string; enabled: boolean }) =>
      setToolEnabled(name, enabled),
    onSuccess: async () => {
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        t("pages.agent.tools.toggle_success", "Tool setting saved"),
        t("navigation.tools", "Tools"),
        gateway?.restartRequired === true,
      )
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : t("pages.agent.tools.toggle_error", "Failed to toggle tool"),
      )
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["tools"] })
      void queryClient.invalidateQueries({
        queryKey: ["workflows", "settings"],
      })
      void queryClient.invalidateQueries({
        queryKey: ["workflows", "dependencies"],
      })
    },
  })

  const saveWebSearchMutation = useMutation({
    mutationFn: updateWebSearchConfig,
    onSuccess: async (updatedConfig) => {
      queryClient.setQueryData(["tools", "web-search-config"], updatedConfig)
      setWebSearchDraftOverride(null)
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        t(
          "pages.agent.tools.web_search.save_success",
          "Settings saved successfully",
        ),
        t("pages.agent.tools.web_search.title", "Web Search Configuration"),
        gateway?.restartRequired === true,
      )
      void queryClient.invalidateQueries({
        queryKey: ["tools", "web-search-config"],
      })
      void queryClient.invalidateQueries({ queryKey: ["tools"] })
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : t(
              "pages.agent.tools.web_search.save_error",
              "Failed to save settings",
            ),
      )
    },
  })

  const saveThreadPolicyMutation = useMutation({
    mutationFn: updateThreadPolicy,
    onSuccess: async (updatedConfig) => {
      queryClient.setQueryData(["tools", "thread-policy"], updatedConfig)
      setThreadPolicyDraftOverride(null)
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        t(
          "pages.agent.tools.thread_policy.save_success",
          "Thread policy saved successfully",
        ),
        t("pages.agent.tools.thread_policy.title", "Thread Policy"),
        gateway?.restartRequired === true,
      )
      void queryClient.invalidateQueries({
        queryKey: ["tools", "thread-policy"],
      })
      void queryClient.invalidateQueries({ queryKey: ["tools"] })
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : t(
              "pages.agent.tools.thread_policy.save_error",
              "Failed to save thread policy",
            ),
      )
    },
  })

  const saveToolAdaptationMutation = useMutation({
    mutationFn: updateToolAdaptation,
    onSuccess: async (updatedConfig) => {
      queryClient.setQueryData(["tools", "adaptation"], updatedConfig)
      setToolAdaptationDraftOverride(null)
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        t(
          "pages.agent.tools.adaptation.save_success",
          "Tool adaptation saved successfully",
        ),
        t("pages.agent.tools.adaptation.title", "Adaptation"),
        gateway?.restartRequired === true,
      )
      void queryClient.invalidateQueries({
        queryKey: ["tools", "adaptation"],
      })
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : t(
              "pages.agent.tools.adaptation.save_error",
              "Failed to save tool adaptation",
            ),
      )
    },
  })

  const runToolAdaptationProbeMutation = useMutation({
    mutationFn: runToolAdaptationProbe,
    onSuccess: (result) => {
      toast.success(
        t(
          "pages.agent.tools.adaptation.probe_success",
          "Tool adaptation probe completed",
        ),
        {
          description: `${result.tool_name} on ${result.visible_tool_surface}`,
        },
      )
      void queryClient.invalidateQueries({
        queryKey: ["tools", "adaptation"],
      })
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : t(
              "pages.agent.tools.adaptation.probe_error",
              "Tool adaptation probe failed",
            ),
      )
      void queryClient.invalidateQueries({
        queryKey: ["tools", "adaptation"],
      })
    },
  })

  const groupedTools = useMemo<{
    groupedTools: GroupedTools
    totalFilteredCount: number
  }>(() => {
    let totalFilteredCount = 0
    const grouped = new Map<string, typeof tools>()

    for (const tool of tools) {
      if (statusFilter !== "all" && tool.status !== statusFilter) {
        continue
      }

      if (normalizedSearchQuery) {
        const matchesName = tool.name
          .toLowerCase()
          .includes(normalizedSearchQuery)
        const matchesDescription = (tool.description || "")
          .toLowerCase()
          .includes(normalizedSearchQuery)

        if (!matchesName && !matchesDescription) {
          continue
        }
      }

      totalFilteredCount += 1
      const items = grouped.get(tool.category) ?? []
      items.push(tool)
      grouped.set(tool.category, items)
    }

    return {
      groupedTools: Array.from(grouped.entries()),
      totalFilteredCount,
    }
  }, [normalizedSearchQuery, statusFilter, tools])

  const providerLabelMap = useMemo(() => {
    const providers = webSearchDraft?.providers ?? []
    return new Map(providers.map((provider) => [provider.id, provider.label]))
  }, [webSearchDraft])

  const pendingToolName = toggleToolMutation.isPending
    ? (toggleToolMutation.variables?.name ?? null)
    : null

  const updateWebSearchDraft = (
    updater: (current: WebSearchConfigResponse) => WebSearchConfigResponse,
  ) => {
    setWebSearchDraftOverride((current) => {
      const draft = current ?? webSearchQuery.data
      return draft ? updater(draft) : current
    })
  }

  const updateThreadPolicyDraft = (
    updater: (current: ThreadPolicyConfig) => ThreadPolicyConfig,
  ) => {
    setThreadPolicyDraftOverride((current) => {
      const draft = current ?? threadPolicyQuery.data
      return draft ? updater(draft) : current
    })
  }

  const updateToolAdaptationDraft = (
    updater: (current: ToolAdaptationConfig) => ToolAdaptationConfig,
  ) => {
    setToolAdaptationDraftOverride((current) => {
      const draft = current ?? toolAdaptationQuery.data
      return draft ? updater(draft) : current
    })
  }

  const toggleTool = (name: string, enabled: boolean) => {
    toggleToolMutation.mutate({ name, enabled })
  }

  const saveWebSearchConfig = () => {
    if (webSearchDraft) {
      saveWebSearchMutation.mutate(webSearchDraft)
    }
  }

  const saveThreadPolicy = () => {
    if (threadPolicyDraft) {
      saveThreadPolicyMutation.mutate(threadPolicyDraft)
    }
  }

  const saveToolAdaptation = () => {
    if (toolAdaptationDraft) {
      saveToolAdaptationMutation.mutate(toolAdaptationDraft)
    }
  }

  const runToolAdaptationProbeAction = (profile: ToolAdaptationProbeTarget) => {
    runToolAdaptationProbeMutation.mutate(profile)
  }

  const toggleExpandedProvider = (providerId: string) => {
    setExpandedProvider((current) =>
      current === providerId ? null : providerId,
    )
  }

  return {
    activeTab,
    expandedProvider,
    groupedTools: groupedTools.groupedTools,
    pendingToolName,
    providerLabelMap,
    searchQuery,
    statusFilter,
    tools,
    totalFilteredCount: groupedTools.totalFilteredCount,
    threadPolicyDraft,
    toolAdaptationDraft,
    webSearchDraft,
    hasToolsError: toolsQuery.error != null,
    hasThreadPolicyError: threadPolicyQuery.error != null,
    hasToolAdaptationError: toolAdaptationQuery.error != null,
    hasWebSearchError: webSearchQuery.error != null,
    isToolsLoading: toolsQuery.isLoading,
    isThreadPolicyLoading: threadPolicyQuery.isLoading,
    isThreadPolicySaving: saveThreadPolicyMutation.isPending,
    isThreadPolicyDirty,
    isToolAdaptationLoading: toolAdaptationQuery.isLoading,
    isToolAdaptationSaving: saveToolAdaptationMutation.isPending,
    isToolAdaptationProbing: runToolAdaptationProbeMutation.isPending,
    probingToolAdaptationProfile:
      runToolAdaptationProbeMutation.variables ?? null,
    isToolAdaptationDirty,
    isWebSearchLoading: webSearchQuery.isLoading,
    isWebSearchSaving: saveWebSearchMutation.isPending,
    isWebSearchDirty,
    setActiveTab: onTabChange,
    setSearchQuery,
    setStatusFilter,
    saveThreadPolicy,
    saveToolAdaptation,
    runToolAdaptationProbe: runToolAdaptationProbeAction,
    saveWebSearchConfig,
    toggleExpandedProvider,
    toggleTool,
    updateThreadPolicyDraft,
    updateToolAdaptationDraft,
    updateWebSearchDraft,
  }
}
