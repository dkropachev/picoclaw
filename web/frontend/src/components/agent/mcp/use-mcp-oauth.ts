import { useCallback, useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type MCPOAuthFlow,
  getMCPOAuthFlow,
  startMCPServerOAuth,
} from "@/api/mcp"

const OAUTH_RESULT_MESSAGE = "picoclaw-mcp-oauth-result"
const POLL_INTERVAL_MS = 1500
const MAX_CONSECUTIVE_POLL_ERRORS = 3

export type MCPOAuthSuccessHandler = (
  flow: MCPOAuthFlow,
) => Promise<void> | void

export function useMCPOAuth(onSuccess: MCPOAuthSuccessHandler) {
  const { t } = useTranslation()
  const [watchFlowID, setWatchFlowID] = useState("")
  const [activeServerName, setActiveServerName] = useState("")
  const popupRef = useRef<Window | null>(null)
  const actionTokenRef = useRef(0)
  const onSuccessRef = useRef(onSuccess)

  useEffect(() => {
    onSuccessRef.current = onSuccess
  }, [onSuccess])

  const finish = useCallback(() => {
    setWatchFlowID("")
    setActiveServerName("")
    const popup = popupRef.current
    popupRef.current = null
    if (popup && !popup.closed) popup.close()
  }, [])

  useEffect(() => {
    if (!watchFlowID) return

    let canceled = false
    let timer: ReturnType<typeof setTimeout> | null = null
    let consecutiveErrors = 0

    const poll = async () => {
      try {
        const flow = await getMCPOAuthFlow(watchFlowID)
        if (canceled) return

        consecutiveErrors = 0
        setActiveServerName(flow.server_name)
        if (flow.status === "pending") {
          if (popupRef.current?.closed) {
            finish()
            toast.error(t("pages.agent.mcp.oauth.window_closed"))
            return
          }
          timer = setTimeout(poll, POLL_INTERVAL_MS)
          return
        }

        finish()
        if (flow.status === "success") {
          await onSuccessRef.current(flow)
          return
        }
        if (flow.status === "expired") {
          toast.error(t("pages.agent.mcp.oauth.expired"))
          return
        }
        toast.error(
          flow.error ||
            t("pages.agent.mcp.oauth.login_failed", {
              name: flow.server_name,
            }),
        )
      } catch (error) {
        if (canceled) return
        consecutiveErrors += 1
        if (consecutiveErrors < MAX_CONSECUTIVE_POLL_ERRORS) {
          timer = setTimeout(poll, POLL_INTERVAL_MS)
          return
        }
        finish()
        toast.error(
          error instanceof Error
            ? error.message
            : t("pages.agent.mcp.oauth.status_failed"),
        )
      }
    }

    void poll()
    return () => {
      canceled = true
      if (timer) clearTimeout(timer)
    }
  }, [finish, t, watchFlowID])

  useEffect(() => {
    const params = new URLSearchParams(window.location.search)
    const flowID = params.get("mcp_oauth_flow_id")
    if (!flowID) return

    setWatchFlowID(flowID)

    const url = new URL(window.location.href)
    url.searchParams.delete("mcp_oauth_flow_id")
    url.searchParams.delete("mcp_oauth_status")
    window.history.replaceState(
      {},
      "",
      `${url.pathname}${url.search}${url.hash}`,
    )
  }, [])

  useEffect(() => {
    const onMessage = (event: MessageEvent) => {
      if (event.origin !== window.location.origin) return
      const data = event.data as
        | { type?: string; flowId?: string; status?: string; error?: string }
        | undefined
      if (
        !data ||
        data.type !== OAUTH_RESULT_MESSAGE ||
        typeof data.flowId !== "string" ||
        !data.flowId
      ) {
        return
      }
      setWatchFlowID(data.flowId)
    }

    window.addEventListener("message", onMessage)
    return () => window.removeEventListener("message", onMessage)
  }, [])

  const startLogin = useCallback(
    async (
      serverName: string,
      preparedPopup?: Window | null,
    ): Promise<boolean> => {
      actionTokenRef.current += 1
      const actionToken = actionTokenRef.current
      const popup =
        preparedPopup === undefined ? window.open("", "_blank") : preparedPopup

      popupRef.current?.close()
      popupRef.current = popup
      setActiveServerName(serverName)

      try {
        const response = await startMCPServerOAuth(serverName)
        if (actionToken !== actionTokenRef.current) {
          popup?.close()
          return false
        }
        if (!response.flow_id || !response.auth_url) {
          throw new Error(t("pages.agent.mcp.oauth.invalid_response"))
        }

        setWatchFlowID(response.flow_id)
        if (popup) {
          popup.location.href = response.auth_url
        } else {
          window.location.assign(response.auth_url)
        }
        return true
      } catch (error) {
        if (actionToken !== actionTokenRef.current) {
          popup?.close()
          return false
        }
        finish()
        toast.error(
          error instanceof Error
            ? error.message
            : t("pages.agent.mcp.oauth.start_failed", { name: serverName }),
        )
        return false
      }
    },
    [finish, t],
  )

  return {
    activeServerName,
    loggingIn: activeServerName !== "",
    startLogin,
  }
}
