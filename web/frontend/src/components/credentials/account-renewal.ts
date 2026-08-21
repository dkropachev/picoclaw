import type { OAuthMethod, OAuthProviderStatus } from "@/api/oauth"

export function getAccountRenewalMethod(
  account: OAuthProviderStatus | undefined,
): OAuthMethod {
  if (!account) return "browser"
  if (account.auth_method === "token" && account.methods.includes("token")) {
    return "token"
  }
  if (
    account.provider === "openai" &&
    account.methods.includes("device_code")
  ) {
    return "device_code"
  }
  if (account.methods.includes("browser")) return "browser"
  if (account.methods.includes("device_code")) return "device_code"
  return account.methods[0] ?? "token"
}
