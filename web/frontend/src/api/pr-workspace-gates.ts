import {
  type PRWorkspace,
  PRWorkspaceAPIError,
  type PRWorkspaceGateSummary,
  type PRWorkspaceMutationFence,
  projectPRWorkspaceGateSummary,
  requestPRWorkspaceAggregate,
  requestPRWorkspaceJSON,
} from "@/api/pr-workspaces"

export interface PRWorkspaceGatePage {
  gates: PRWorkspaceGateSummary[]
  workspace_version: number
}

export async function getPRWorkspaceGates(
  workspaceID: string,
  signal?: AbortSignal,
): Promise<PRWorkspaceGatePage> {
  const value = await requestPRWorkspaceJSON<unknown>(
    `/api/pr-workspaces/${encodeURIComponent(workspaceID)}/gates`,
    undefined,
    signal,
  )
  if (
    typeof value !== "object" ||
    value === null ||
    Array.isArray(value) ||
    Object.keys(value).some(
      (key) => key !== "gates" && key !== "workspace_version",
    ) ||
    !Array.isArray((value as Record<string, unknown>).gates) ||
    !Number.isSafeInteger(
      (value as Record<string, unknown>).workspace_version,
    ) ||
    ((value as Record<string, unknown>).workspace_version as number) < 0
  ) {
    throw new PRWorkspaceAPIError("malformed_response", 502)
  }
  const record = value as Record<string, unknown>
  return {
    gates: (record.gates as unknown[]).map(projectPRWorkspaceGateSummary),
    workspace_version: record.workspace_version as number,
  }
}

export async function respondPRWorkspaceGate(
  workspaceID: string,
  gateRunID: string,
  input: PRWorkspaceMutationFence & {
    fieldValues: Record<string, unknown>
  },
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return requestPRWorkspaceAggregate(
    `/api/pr-workspaces/${encodeURIComponent(workspaceID)}/gates/${encodeURIComponent(gateRunID)}/respond`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        expected_version: input.expected_version,
        request_id: input.request_id,
        "field-values": input.fieldValues,
      }),
    },
    signal,
  )
}
