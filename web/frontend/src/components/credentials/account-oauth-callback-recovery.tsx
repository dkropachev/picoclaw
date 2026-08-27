import { useQueryClient } from "@tanstack/react-query"
import { useCallback, useEffect, useRef } from "react"
import { toast } from "sonner"

import { useCredentialsPage } from "@/hooks/use-credentials-page"
import { safeOAuthFlowMessage } from "@/lib/safe-oauth-flow-message"

export function AccountOAuthCallbackRecovery({
  flowID,
  onConsumed,
}: {
  flowID: string
  onConsumed: () => void
}) {
  const queryClient = useQueryClient()
  const completedRevision = useRef(0)
  const reportedError = useRef("")
  const onConsumedRef = useRef(onConsumed)
  useEffect(() => {
    onConsumedRef.current = onConsumed
  }, [onConsumed])
  const consumeCallback = useCallback(() => onConsumedRef.current(), [])
  const credentials = useCredentialsPage({
    oauthCallbackFlowID: flowID,
    onOAuthCallbackConsumed: consumeCallback,
  })

  useEffect(() => {
    if (
      credentials.credentialsRevision === 0 ||
      credentials.credentialsRevision === completedRevision.current
    ) {
      return
    }
    completedRevision.current = credentials.credentialsRevision
    void queryClient.invalidateQueries({
      queryKey: ["accounts", "collection"],
    })
    toast.success("Account login completed.")
  }, [credentials.credentialsRevision, queryClient])

  useEffect(() => {
    if (!credentials.error) return
    const message = safeOAuthFlowMessage(
      credentials.error,
      "Account login could not be completed.",
    )
    if (message === reportedError.current) return
    reportedError.current = message
    toast.error(message)
  }, [credentials.error])

  return null
}
