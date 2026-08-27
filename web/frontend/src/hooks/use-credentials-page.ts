import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type OAuthFlowState,
  type OAuthProvider,
  type OAuthProviderStatus,
  getOAuthFlow,
  getOAuthProviders,
  loginOAuth,
  logoutOAuth,
  pollOAuthFlow,
} from "@/api/oauth"
import { safeOAuthFlowMessage } from "@/lib/safe-oauth-flow-message"

type FlowWatchMode = "" | "status" | "poll"

interface FlowWatchTarget {
  flowID: string
  mode: Exclude<FlowWatchMode, "">
  intervalMs: number
  actionGeneration: number
}

interface StartDeviceCodeOptions {
  openImmediately?: boolean
}

export interface UseCredentialsPageOptions {
  oauthCallbackFlowID?: string | null
  onOAuthCallbackConsumed?: () => void
}

function getProviderLabel(provider: OAuthProvider | ""): string {
  if (provider === "openai") return "OpenAI"
  if (provider === "anthropic") return "Anthropic"
  if (provider === "google-antigravity") return "Google Antigravity"
  if (provider === "github-copilot") return "GitHub Copilot"
  return ""
}

function credentialPayload(value: string): string | undefined {
  const trimmed = value.trim()
  return trimmed ? trimmed : undefined
}

export function useCredentialsPage({
  oauthCallbackFlowID,
  onOAuthCallbackConsumed,
}: UseCredentialsPageOptions = {}) {
  const { t } = useTranslation()
  const [providers, setProviders] = useState<OAuthProviderStatus[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")
  const [credentialsRevision, setCredentialsRevision] = useState(0)

  const [activeAction, setActiveAction] = useState("")
  const [activeFlow, setActiveFlow] = useState<OAuthFlowState | null>(null)
  const actionTokenRef = useRef(0)
  const expectedFlowRef = useRef<FlowWatchTarget | null>(null)
  const providersRequestRef = useRef(0)

  const [flowWatch, setFlowWatch] = useState<FlowWatchTarget | null>(null)

  const [logoutDialogOpen, setLogoutDialogOpen] = useState(false)
  const [logoutConfirmProvider, setLogoutConfirmProvider] = useState<
    OAuthProvider | ""
  >("")
  const [logoutConfirmCredentialID, setLogoutConfirmCredentialID] = useState("")

  const [deviceSheetOpen, setDeviceSheetOpen] = useState(false)
  const [deviceFlow, setDeviceFlow] = useState<OAuthFlowState | null>(null)

  const bumpActionToken = useCallback(() => {
    actionTokenRef.current += 1
    expectedFlowRef.current = null
    setFlowWatch(null)
    return actionTokenRef.current
  }, [])

  const isActionTokenCurrent = useCallback((token: number) => {
    return actionTokenRef.current === token
  }, [])

  const watchExpectedFlow = useCallback((target: FlowWatchTarget) => {
    if (actionTokenRef.current !== target.actionGeneration) {
      return false
    }
    expectedFlowRef.current = target
    setFlowWatch(target)
    return true
  }, [])

  const loadProviders = useCallback(async (): Promise<boolean> => {
    const request = ++providersRequestRef.current
    try {
      const data = await getOAuthProviders()
      if (request !== providersRequestRef.current) {
        return false
      }
      setProviders(data.providers)
      setError("")
      return true
    } catch (err) {
      if (request !== providersRequestRef.current) {
        return false
      }
      setError(
        err instanceof Error ? err.message : t("credentials.errors.loadFailed"),
      )
      return false
    } finally {
      if (request === providersRequestRef.current) {
        setLoading(false)
      }
    }
  }, [t])

  useEffect(() => {
    void loadProviders()
  }, [loadProviders])

  useEffect(() => {
    if (!flowWatch) {
      return
    }

    let canceled = false
    let timer: ReturnType<typeof setTimeout> | null = null
    const isExpectedFlow = () => {
      const expected = expectedFlowRef.current
      return (
        !canceled &&
        actionTokenRef.current === flowWatch.actionGeneration &&
        expected?.actionGeneration === flowWatch.actionGeneration &&
        expected.flowID === flowWatch.flowID &&
        expected.mode === flowWatch.mode
      )
    }

    const step = async () => {
      try {
        const flow =
          flowWatch.mode === "poll"
            ? await pollOAuthFlow(flowWatch.flowID)
            : await getOAuthFlow(flowWatch.flowID)

        if (!isExpectedFlow() || flow.flow_id !== flowWatch.flowID) {
          return
        }

        setActiveFlow(flow)
        setDeviceFlow((prev) =>
          prev?.flow_id === flow.flow_id ? { ...prev, ...flow } : prev,
        )

        if (flow.status === "pending") {
          timer = setTimeout(step, flowWatch.intervalMs)
          return
        }

        expectedFlowRef.current = null
        setFlowWatch((current) =>
          current?.actionGeneration === flowWatch.actionGeneration &&
          current.flowID === flowWatch.flowID
            ? null
            : current,
        )

        if (flowWatch.mode === "poll") {
          setDeviceSheetOpen(false)
        }

        setActiveAction("")
        await loadProviders()
        if (!isActionTokenCurrent(flowWatch.actionGeneration)) {
          return
        }
        if (flow.status === "success") {
          setCredentialsRevision((revision) => revision + 1)
        } else if (flow.status === "error") {
          setError(
            safeOAuthFlowMessage(flow.error, t("credentials.flow.error")),
          )
        } else if (flow.status === "expired") {
          setError(
            safeOAuthFlowMessage(flow.error, t("credentials.flow.expired")),
          )
        }
      } catch (err) {
        if (!isExpectedFlow()) {
          return
        }
        expectedFlowRef.current = null
        setFlowWatch((current) =>
          current?.actionGeneration === flowWatch.actionGeneration &&
          current.flowID === flowWatch.flowID
            ? null
            : current,
        )
        setActiveAction("")
        setError(
          err instanceof Error
            ? err.message
            : t("credentials.errors.flowFailed"),
        )
      }
    }

    void step()

    return () => {
      canceled = true
      if (timer) {
        clearTimeout(timer)
      }
    }
  }, [flowWatch, isActionTokenCurrent, loadProviders, t])

  useEffect(() => {
    const params = new URLSearchParams(window.location.search)
    const flowID =
      oauthCallbackFlowID === undefined
        ? params.get("oauth_flow_id")
        : oauthCallbackFlowID
    if (!flowID) {
      return
    }

    const actionGeneration = bumpActionToken()
    watchExpectedFlow({
      flowID,
      mode: "status",
      intervalMs: 700,
      actionGeneration,
    })

    if (oauthCallbackFlowID !== undefined) {
      onOAuthCallbackConsumed?.()
      return
    }

    params.delete("oauth_flow_id")
    const search = params.toString()
    window.history.replaceState(
      {},
      "",
      `${window.location.pathname}${search ? `?${search}` : ""}`,
    )
  }, [
    bumpActionToken,
    oauthCallbackFlowID,
    onOAuthCallbackConsumed,
    watchExpectedFlow,
  ])

  useEffect(() => {
    const onMessage = (event: MessageEvent) => {
      const data = event.data as
        | { type?: string; flowId?: string; status?: string }
        | undefined
      if (!data || data.type !== "picoclaw-oauth-result" || !data.flowId) {
        return
      }

      const expected = expectedFlowRef.current
      if (
        !expected ||
        expected.mode !== "status" ||
        expected.flowID !== data.flowId ||
        expected.actionGeneration !== actionTokenRef.current
      ) {
        return
      }

      watchExpectedFlow({ ...expected, intervalMs: 700 })
    }

    window.addEventListener("message", onMessage)
    return () => window.removeEventListener("message", onMessage)
  }, [watchExpectedFlow])

  const startBrowserOAuth = useCallback(
    async (
      provider: OAuthProvider,
      credentialID?: string,
    ): Promise<boolean> => {
      const actionToken = bumpActionToken()
      setActiveAction(`${provider}:browser`)
      setError("")

      const authTab = window.open("", "_blank")
      if (!authTab) {
        if (!isActionTokenCurrent(actionToken)) {
          return false
        }
        setActiveAction("")
        setError(t("credentials.errors.popupBlocked"))
        return false
      }

      try {
        const resp = await loginOAuth({
          provider,
          credential_id: credentialID,
          method: "browser",
        })
        if (!isActionTokenCurrent(actionToken)) {
          authTab.close()
          return false
        }
        if (!resp.auth_url || !resp.flow_id) {
          throw new Error(t("credentials.errors.invalidBrowserResponse"))
        }

        authTab.location.href = resp.auth_url

        setActiveFlow({
          flow_id: resp.flow_id,
          provider,
          credential_id: resp.credential_id,
          method: "browser",
          status: "pending",
          expires_at: resp.expires_at,
        })
        watchExpectedFlow({
          flowID: resp.flow_id,
          mode: "status",
          intervalMs: 2000,
          actionGeneration: actionToken,
        })
        return true
      } catch (err) {
        if (!isActionTokenCurrent(actionToken)) {
          authTab.close()
          return false
        }
        authTab.close()
        setActiveAction("")
        setError(
          err instanceof Error
            ? err.message
            : t("credentials.errors.loginFailed"),
        )
        return false
      }
    },
    [bumpActionToken, isActionTokenCurrent, t, watchExpectedFlow],
  )

  const startOpenAIDeviceCode = useCallback(
    async (
      credentialID?: string,
      options: StartDeviceCodeOptions = {},
    ): Promise<boolean> => {
      const openImmediately = options.openImmediately === true
      const actionToken = bumpActionToken()
      setActiveAction("openai:device")
      setError("")
      if (openImmediately) {
        setDeviceFlow(null)
        setDeviceSheetOpen(true)
      }

      try {
        const resp = await loginOAuth({
          provider: "openai",
          credential_id: credentialID,
          method: "device_code",
        })
        if (!isActionTokenCurrent(actionToken)) {
          return false
        }
        if (!resp.flow_id || !resp.user_code || !resp.verify_url) {
          throw new Error(t("credentials.errors.invalidDeviceResponse"))
        }

        const flow: OAuthFlowState = {
          flow_id: resp.flow_id,
          provider: "openai",
          credential_id: resp.credential_id,
          method: "device_code",
          status: "pending",
          user_code: resp.user_code,
          verify_url: resp.verify_url,
          interval: resp.interval,
          expires_at: resp.expires_at,
        }

        setDeviceFlow(flow)
        setDeviceSheetOpen(true)
        setActiveFlow(flow)
        watchExpectedFlow({
          flowID: resp.flow_id,
          mode: "poll",
          intervalMs: Math.max(1000, (resp.interval ?? 5) * 1000),
          actionGeneration: actionToken,
        })
        return true
      } catch (err) {
        if (!isActionTokenCurrent(actionToken)) {
          return false
        }
        if (openImmediately) {
          setDeviceSheetOpen(false)
        }
        setActiveAction("")
        setError(
          err instanceof Error
            ? err.message
            : t("credentials.errors.loginFailed"),
        )
        return false
      }
    },
    [bumpActionToken, isActionTokenCurrent, t, watchExpectedFlow],
  )

  const saveToken = useCallback(
    async (
      provider: OAuthProvider,
      token: string,
      credentialID?: string,
    ): Promise<boolean> => {
      const actionToken = bumpActionToken()
      const actionID = `${provider}:token`
      setActiveAction(actionID)
      setError("")

      try {
        await loginOAuth({
          provider,
          credential_id: credentialID,
          method: "token",
          token,
        })
        if (!isActionTokenCurrent(actionToken)) {
          return false
        }
        const providersLoaded = await loadProviders()
        if (!isActionTokenCurrent(actionToken)) {
          return false
        }
        if (!providersLoaded) {
          return false
        }
        setCredentialsRevision((revision) => revision + 1)
        return true
      } catch (err) {
        if (!isActionTokenCurrent(actionToken)) {
          return false
        }
        setError(
          err instanceof Error
            ? err.message
            : t("credentials.errors.loginFailed"),
        )
        return false
      } finally {
        if (isActionTokenCurrent(actionToken)) {
          setActiveAction("")
        }
      }
    },
    [bumpActionToken, isActionTokenCurrent, loadProviders, t],
  )

  const doLogout = useCallback(
    async (provider: OAuthProvider, credentialID?: string) => {
      const actionID = `${provider}:logout`
      setActiveAction(actionID)
      setError("")

      try {
        await logoutOAuth(provider, credentialID)
        await loadProviders()
        setCredentialsRevision((revision) => revision + 1)
      } catch (err) {
        setError(
          err instanceof Error
            ? err.message
            : t("credentials.errors.logoutFailed"),
        )
      } finally {
        setActiveAction("")
      }
    },
    [loadProviders, t],
  )

  const askLogout = useCallback(
    (provider: OAuthProvider, credentialID?: string) => {
      setLogoutConfirmProvider(provider)
      setLogoutConfirmCredentialID(credentialID ?? "")
      setLogoutDialogOpen(true)
    },
    [],
  )

  const handleConfirmLogout = useCallback(async () => {
    if (!logoutConfirmProvider) {
      return
    }
    await doLogout(
      logoutConfirmProvider,
      credentialPayload(logoutConfirmCredentialID),
    )
    setLogoutDialogOpen(false)
    setLogoutConfirmProvider("")
    setLogoutConfirmCredentialID("")
  }, [doLogout, logoutConfirmCredentialID, logoutConfirmProvider])

  const handleLogoutDialogOpenChange = useCallback((open: boolean) => {
    setLogoutDialogOpen(open)
    if (!open) {
      setLogoutConfirmProvider("")
      setLogoutConfirmCredentialID("")
    }
  }, [])

  const handleDeviceSheetOpenChange = useCallback(
    (open: boolean) => {
      setDeviceSheetOpen(open)
      if (open) {
        return
      }

      if (activeAction === "openai:device" || flowWatch?.mode === "poll") {
        bumpActionToken()
        setActiveAction("")
      }

      setDeviceFlow(null)
      if (
        activeFlow?.method === "device_code" &&
        activeFlow.status === "pending"
      ) {
        setActiveFlow(null)
      }
    },
    [activeAction, activeFlow, bumpActionToken, flowWatch?.mode],
  )

  const stopLoading = useCallback(() => {
    bumpActionToken()
    setActiveAction("")
    setDeviceSheetOpen(false)
    setDeviceFlow(null)
    setActiveFlow((prev) => (prev?.status === "pending" ? null : prev))
  }, [bumpActionToken])

  const clearError = useCallback(() => {
    setError("")
  }, [])

  const logoutProviderLabel = logoutConfirmCredentialID
    ? `${getProviderLabel(logoutConfirmProvider)} (${logoutConfirmCredentialID})`
    : getProviderLabel(logoutConfirmProvider)

  const flowHint = useMemo(() => {
    if (!activeFlow) {
      return ""
    }
    if (activeFlow.status === "pending") {
      return t("credentials.flow.pending")
    }
    if (activeFlow.status === "success") {
      return t("credentials.flow.success")
    }
    if (activeFlow.status === "expired") {
      return t("credentials.flow.expired")
    }
    return activeFlow.error || t("credentials.flow.error")
  }, [activeFlow, t])

  return {
    providers,
    loading,
    error,
    credentialsRevision,
    activeAction,
    activeFlow,
    flowHint,
    logoutDialogOpen,
    logoutConfirmProvider,
    logoutProviderLabel,
    deviceSheetOpen,
    deviceFlow,
    startBrowserOAuth,
    startOpenAIDeviceCode,
    stopLoading,
    clearError,
    saveToken,
    askLogout,
    handleConfirmLogout,
    handleLogoutDialogOpenChange,
    handleDeviceSheetOpenChange,
  }
}
