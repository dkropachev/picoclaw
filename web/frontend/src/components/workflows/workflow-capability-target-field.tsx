import {
  IconAlertTriangle,
  IconChevronDown,
  IconRefresh,
  IconSearch,
} from "@tabler/icons-react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect, useMemo, useState } from "react"

import {
  type WorkflowAuthoringCapabilities,
  type WorkflowCapabilityReadiness,
  getWorkflowAuthoringCapabilities,
  workflowAuthoringCapabilitiesQueryKey,
} from "@/api/workflows"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"

interface CapabilityTarget {
  target: string
  label: string
  category: "Agent" | "Tool" | "MCP" | "Function"
  readiness: WorkflowCapabilityReadiness
}

interface ResolvedCapabilityTargets {
  identity: number
  catalog: WorkflowAuthoringCapabilities
}

const capabilitySearchMaximumBytes = 256

export function WorkflowCapabilityTargetField({
  id,
  label = "Action target",
  value,
  disabled,
  onChange,
}: {
  id: string
  label?: string
  value: string
  disabled?: boolean
  onChange: (value: string) => void
}) {
  const [open, setOpen] = useState(false)
  const [openIdentity, setOpenIdentity] = useState(0)
  const [search, setSearch] = useState("")
  const [resolved, setResolved] = useState<ResolvedCapabilityTargets | null>(
    null,
  )
  const queryClient = useQueryClient()
  const scopedQueryKey = useMemo(
    () => [...workflowAuthoringCapabilitiesQueryKey, "target-picker", id],
    [id],
  )
  const query = useQuery({
    queryKey: scopedQueryKey,
    queryFn: ({ signal }) => getWorkflowAuthoringCapabilities(signal),
    enabled: open,
    retry: false,
  })
  const catalog =
    resolved?.identity === openIdentity ? resolved.catalog : undefined

  useEffect(() => {
    if (!open || query.isFetching || query.data == null) {
      return
    }
    setResolved({ identity: openIdentity, catalog: query.data })
  }, [open, openIdentity, query.data, query.dataUpdatedAt, query.isFetching])

  const targets = useMemo(
    () =>
      catalog == null
        ? []
        : capabilityTargets(catalog).filter((target) =>
            `${target.category} ${target.label} ${target.target}`
              .toLocaleLowerCase()
              .includes(search.trim().toLocaleLowerCase()),
          ),
    [catalog, search],
  )
  const loading =
    open &&
    (query.isFetching ||
      (query.isPending && query.data === undefined) ||
      catalog == null)

  const changeOpen = (nextOpen: boolean) => {
    if (nextOpen) {
      setResolved(null)
      setOpenIdentity((identity) => identity + 1)
    } else {
      setResolved(null)
      void queryClient.cancelQueries({
        queryKey: scopedQueryKey,
        exact: true,
      })
    }
    setOpen(nextOpen)
  }

  return (
    <div className="grid min-w-0 gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <div className="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] gap-2">
        <Input
          id={id}
          value={value}
          disabled={disabled}
          onChange={(event) => onChange(event.target.value)}
          placeholder="agent/main, tool/name, mcp/server/tool, or function/name"
          className="min-w-0 font-mono text-xs"
        />
        <Popover open={open} onOpenChange={changeOpen}>
          <PopoverTrigger asChild>
            <Button
              type="button"
              variant="outline"
              disabled={disabled}
              aria-label="Choose action capability"
              title="Choose a ready capability"
            >
              Choose
              <IconChevronDown className="size-4" />
            </Button>
          </PopoverTrigger>
          <PopoverContent
            align="end"
            className="grid max-h-[min(28rem,calc(100dvh-2rem))] w-[min(32rem,calc(100vw-2rem))] gap-3 overflow-hidden p-3"
          >
            <div className="grid gap-1.5">
              <Label htmlFor={`${id}-capability-search`}>
                Search capabilities
              </Label>
              <div className="relative">
                <IconSearch className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
                <Input
                  id={`${id}-capability-search`}
                  type="search"
                  value={search}
                  onChange={(event) =>
                    setSearch(boundedSearch(event.target.value))
                  }
                  placeholder="Search target or name"
                  className="pl-8"
                />
              </div>
            </div>
            {/* Keyboard focus is required for this independently scrollable region. */}
            {/* eslint-disable jsx-a11y/no-noninteractive-tabindex */}
            <div
              role="region"
              aria-label="Action capability choices"
              tabIndex={0}
              className="grid min-h-24 content-start gap-2 overflow-y-auto"
            >
              {catalog != null && !catalog.complete ? (
                <div
                  role="note"
                  className="grid gap-1 rounded-md border border-amber-500/35 bg-amber-500/5 p-3 text-xs"
                >
                  <div className="flex items-start gap-2">
                    <IconAlertTriangle className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-amber-300" />
                    <span>
                      This capability catalog is partial. A ready capability
                      that is not listed may still be available; enter its exact
                      target manually.
                    </span>
                  </div>
                  {catalog.limits.length > 0 ? (
                    <span className="text-muted-foreground pl-6">
                      Limited:{" "}
                      {catalog.limits
                        .map(capabilityLimitLabel)
                        .slice(0, 6)
                        .join(", ")}
                      .
                    </span>
                  ) : null}
                </div>
              ) : null}
              {query.isError ? (
                <div
                  role="alert"
                  className="border-destructive/30 bg-destructive/5 grid gap-2 rounded-md border p-3 text-xs"
                >
                  <div className="flex items-start gap-2">
                    <IconAlertTriangle className="text-destructive mt-0.5 size-4 shrink-0" />
                    <span>
                      Capabilities are unavailable. You can still enter an exact
                      target manually.
                    </span>
                  </div>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => void query.refetch()}
                  >
                    <IconRefresh className="size-4" />
                    Retry
                  </Button>
                </div>
              ) : loading ? (
                <div
                  role="status"
                  className="text-muted-foreground rounded-md border border-dashed px-3 py-6 text-center text-xs"
                >
                  Loading workflow capabilities…
                </div>
              ) : targets.length === 0 ? (
                <div className="text-muted-foreground rounded-md border border-dashed px-3 py-6 text-center text-xs">
                  No capabilities match. Enter an advanced target manually.
                </div>
              ) : (
                <div className="grid gap-1">
                  {targets.map((target) => (
                    <Button
                      key={target.target}
                      type="button"
                      variant="ghost"
                      disabled={target.readiness !== "ready"}
                      className="h-auto min-w-0 justify-start px-2 py-2 text-left"
                      title={
                        target.readiness === "ready"
                          ? `Use ${target.target}`
                          : `${target.target} is ${readinessLabel(target.readiness)}`
                      }
                      onClick={() => {
                        onChange(target.target)
                        changeOpen(false)
                      }}
                    >
                      <span className="grid min-w-0 flex-1 gap-0.5">
                        <span className="flex min-w-0 items-center gap-2">
                          <span className="truncate">{target.label}</span>
                          <Badge variant="outline" className="shrink-0">
                            {target.category}
                          </Badge>
                        </span>
                        <span className="text-muted-foreground font-mono text-[11px] break-all">
                          {target.target}
                        </span>
                      </span>
                    </Button>
                  ))}
                </div>
              )}
            </div>
            {/* eslint-enable jsx-a11y/no-noninteractive-tabindex */}
            <p className="text-muted-foreground text-xs">
              Choosing a capability only fills the target field. Apply changes
              to update YAML; no workflow runs from this picker.
            </p>
          </PopoverContent>
        </Popover>
      </div>
      <p className="text-muted-foreground text-xs">
        Select a ready capability or keep an exact manual target for advanced
        and temporarily unavailable integrations.
      </p>
    </div>
  )
}

function capabilityTargets(
  catalog: WorkflowAuthoringCapabilities,
): CapabilityTarget[] {
  return [
    ...catalog.agents.map((capability) => ({
      target: capability.target,
      label: capability.id,
      category: "Agent" as const,
      readiness: capability.readiness,
    })),
    ...catalog.tools.map((capability) => ({
      target: capability.target,
      label: capability.name,
      category: "Tool" as const,
      readiness: capability.readiness,
    })),
    ...catalog.mcp_tools.map((capability) => ({
      target: capability.target,
      label: `${capability.server} / ${capability.tool}`,
      category: "MCP" as const,
      readiness: capability.readiness,
    })),
    ...catalog.functions.map((capability) => ({
      target: capability.target,
      label: capability.name,
      category: "Function" as const,
      readiness: capability.readiness,
    })),
  ]
}

function boundedSearch(value: string) {
  const encoder = new TextEncoder()
  let result = ""
  for (const character of value) {
    if (
      encoder.encode(result).byteLength + encoder.encode(character).byteLength >
      capabilitySearchMaximumBytes
    ) {
      break
    }
    result += character
  }
  return result
}

function readinessLabel(readiness: WorkflowCapabilityReadiness) {
  return readiness.replaceAll("_", " ")
}

function capabilityLimitLabel(
  limit: WorkflowAuthoringCapabilities["limits"][number],
) {
  switch (limit) {
    case "agents_truncated":
      return "agents omitted"
    case "tools_truncated":
      return "tools omitted"
    case "mcp_tools_truncated":
      return "MCP tools omitted"
    case "functions_truncated":
      return "functions omitted"
    case "parameter_shapes_omitted":
      return "parameter details omitted"
    case "unsafe_fields_omitted":
      return "unsafe details omitted"
  }
}
