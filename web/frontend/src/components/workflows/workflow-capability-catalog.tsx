import {
  IconAlertTriangle,
  IconCheck,
  IconCode,
  IconCopy,
  IconRefresh,
  IconSearch,
} from "@tabler/icons-react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect, useMemo, useState } from "react"

import {
  type WorkflowAuthoringCapabilities,
  type WorkflowCapabilityParameterShape,
  type WorkflowCapabilityReadiness,
  getWorkflowAuthoringCapabilities,
  workflowAuthoringCapabilitiesQueryKey,
} from "@/api/workflows"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet"
import { useCopyToClipboard } from "@/hooks/use-copy-to-clipboard"

const capabilityCategories = ["agents", "tools", "mcp", "functions"] as const
type CapabilityCategory = (typeof capabilityCategories)[number]

const capabilityCategoryLabels: Record<CapabilityCategory, string> = {
  agents: "Agents",
  tools: "Tools",
  mcp: "MCP tools",
  functions: "Functions",
}

const searchMaximumBytes = 256

interface ResolvedCapabilityCatalog {
  identity: number
  catalog: WorkflowAuthoringCapabilities
}

export function WorkflowCapabilityCatalog() {
  const [open, setOpen] = useState(false)
  const [openIdentity, setOpenIdentity] = useState(0)
  const [retryErrorMessage, setRetryErrorMessage] = useState<string | null>(
    null,
  )
  const [resolvedCatalog, setResolvedCatalog] =
    useState<ResolvedCapabilityCatalog | null>(null)
  const [search, setSearch] = useState("")
  const [categories, setCategories] = useState<
    Record<CapabilityCategory, boolean>
  >({
    agents: true,
    tools: true,
    mcp: true,
    functions: true,
  })
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: workflowAuthoringCapabilitiesQueryKey,
    queryFn: ({ signal }) => getWorkflowAuthoringCapabilities(signal),
    enabled: open,
    retry: false,
  })
  const currentCatalog =
    resolvedCatalog?.identity === openIdentity
      ? resolvedCatalog.catalog
      : undefined
  const loading =
    open &&
    (query.isFetching ||
      (query.isPending && query.data === undefined) ||
      currentCatalog == null)

  useEffect(() => {
    if (!open || query.isFetching || query.data == null) {
      return
    }
    setResolvedCatalog({
      identity: openIdentity,
      catalog: query.data,
    })
  }, [open, openIdentity, query.data, query.dataUpdatedAt, query.isFetching])

  const onOpenChange = (nextOpen: boolean) => {
    if (nextOpen) {
      setResolvedCatalog(null)
      setOpenIdentity((identity) => identity + 1)
    } else {
      setRetryErrorMessage(null)
      setResolvedCatalog(null)
      void queryClient.cancelQueries({
        queryKey: workflowAuthoringCapabilitiesQueryKey,
        exact: true,
      })
    }
    setOpen(nextOpen)
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetTrigger asChild>
        <Button
          type="button"
          variant="outline"
          size="sm"
          aria-label="Workflow capabilities"
          title="Workflow capabilities"
        >
          <IconCode className="size-4" />
          <span className="hidden lg:inline">Capabilities</span>
        </Button>
      </SheetTrigger>
      <SheetContent className="flex w-full flex-col gap-0 overflow-hidden p-0 data-[side=right]:w-full data-[side=right]:sm:max-w-2xl">
        <SheetHeader className="border-border/70 border-b px-4 py-4 pr-12 sm:px-6">
          <SheetTitle>Workflow capabilities</SheetTitle>
          <SheetDescription>
            Browse safe workflow step targets and their projected parameter
            shapes. Copying a target does not change your draft.
          </SheetDescription>
        </SheetHeader>

        <div className="border-border/70 grid gap-3 border-b px-4 py-3 sm:px-6">
          <div className="grid gap-1.5">
            <Label htmlFor="workflow-capability-search">
              Search capabilities
            </Label>
            <div className="relative">
              <IconSearch className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
              <Input
                id="workflow-capability-search"
                type="search"
                value={search}
                maxLength={searchMaximumBytes}
                onChange={(event) =>
                  setSearch(boundedCapabilitySearch(event.target.value))
                }
                placeholder="Search names or exact targets"
                className="pl-8"
              />
            </div>
          </div>
          <div
            role="group"
            aria-label="Capability categories"
            className="flex flex-wrap gap-1.5"
          >
            {capabilityCategories.map((category) => (
              <Button
                key={category}
                type="button"
                size="sm"
                variant={categories[category] ? "secondary" : "outline"}
                aria-pressed={categories[category]}
                onClick={() =>
                  setCategories((current) => ({
                    ...current,
                    [category]: !current[category],
                  }))
                }
              >
                {capabilityCategoryLabels[category]}
              </Button>
            ))}
          </div>
        </div>

        {/* Keyboard focus is required for this independently scrollable region. */}
        {/* eslint-disable jsx-a11y/no-noninteractive-tabindex */}
        <div
          role="region"
          tabIndex={0}
          aria-label="Workflow capability results"
          className="min-h-0 flex-1 overflow-y-auto px-4 py-4 sm:px-6"
        >
          {retryErrorMessage != null ? (
            <CapabilityError
              message={retryErrorMessage}
              retrying
              onRetry={() => undefined}
            />
          ) : query.isError ? (
            <CapabilityError
              message={capabilityErrorMessage(query.error)}
              retrying={false}
              onRetry={() => {
                setRetryErrorMessage(capabilityErrorMessage(query.error))
                void query.refetch().finally(() => {
                  setRetryErrorMessage(null)
                })
              }}
            />
          ) : loading ? (
            <CapabilityLoading />
          ) : currentCatalog == null ? (
            <CapabilityLoading />
          ) : (
            <CapabilityResults
              catalog={currentCatalog}
              search={search}
              categories={categories}
            />
          )}
        </div>
        {/* eslint-enable jsx-a11y/no-noninteractive-tabindex */}
      </SheetContent>
    </Sheet>
  )
}

function CapabilityLoading() {
  return (
    <div
      role="status"
      className="text-muted-foreground rounded-md border border-dashed px-4 py-8 text-center text-sm"
    >
      Loading workflow capabilities…
    </div>
  )
}

function CapabilityError({
  message,
  retrying,
  onRetry,
}: {
  message: string
  retrying: boolean
  onRetry: () => void
}) {
  return (
    <div
      role="alert"
      className="border-destructive/30 bg-destructive/5 grid gap-3 rounded-md border p-4"
    >
      <div className="flex items-start gap-2">
        <IconAlertTriangle className="text-destructive mt-0.5 size-4 shrink-0" />
        <div>
          <div className="font-medium">Capabilities are unavailable</div>
          <div className="text-muted-foreground mt-1 text-xs break-words">
            {message}
          </div>
        </div>
      </div>
      <Button
        type="button"
        size="sm"
        variant="outline"
        disabled={retrying}
        aria-label={retrying ? "Retrying capabilities" : "Retry capabilities"}
        onClick={onRetry}
      >
        <IconRefresh className="size-4" />
        {retrying ? "Retrying…" : "Retry"}
      </Button>
    </div>
  )
}

function CapabilityResults({
  catalog,
  search,
  categories,
}: {
  catalog: WorkflowAuthoringCapabilities
  search: string
  categories: Record<CapabilityCategory, boolean>
}) {
  const normalizedSearch = search.trim().toLowerCase()
  const visible = useMemo(
    () => ({
      agents: categories.agents
        ? catalog.agents.filter((capability) =>
            capabilityMatches(
              normalizedSearch,
              capability.id,
              capability.target,
            ),
          )
        : [],
      tools: categories.tools
        ? catalog.tools.filter((capability) =>
            capabilityMatches(
              normalizedSearch,
              capability.name,
              capability.target,
            ),
          )
        : [],
      mcp: categories.mcp
        ? catalog.mcp_tools.filter((capability) =>
            capabilityMatches(
              normalizedSearch,
              capability.server,
              capability.tool,
              capability.target,
            ),
          )
        : [],
      functions: categories.functions
        ? catalog.functions.filter((capability) =>
            capabilityMatches(
              normalizedSearch,
              capability.name,
              capability.target,
            ),
          )
        : [],
    }),
    [catalog, categories, normalizedSearch],
  )
  const totalVisible =
    visible.agents.length +
    visible.tools.length +
    visible.mcp.length +
    visible.functions.length
  const totalCapabilities =
    catalog.agents.length +
    catalog.tools.length +
    catalog.mcp_tools.length +
    catalog.functions.length

  return (
    <div className="grid gap-4">
      {!catalog.complete ? (
        <PartialCapabilityNotice catalog={catalog} />
      ) : catalog.mcp_status === "disabled" ? (
        <div className="border-border bg-muted/30 rounded-md border px-3 py-2 text-xs">
          MCP is disabled. Other ready capabilities remain available.
        </div>
      ) : null}

      {totalVisible === 0 ? (
        <div className="text-muted-foreground rounded-md border border-dashed px-4 py-8 text-center text-sm">
          {totalCapabilities === 0
            ? "No workflow capabilities are currently available."
            : "No capabilities match the current search and category filters."}
        </div>
      ) : (
        <>
          <CapabilitySection
            title="Agents"
            capabilities={visible.agents.map((capability) => ({
              key: capability.target,
              title: capability.id,
              target: capability.target,
              readiness: capability.readiness,
              detail: capability.is_default ? "Default agent" : undefined,
            }))}
          />
          <CapabilitySection
            title="Tools"
            capabilities={visible.tools.map((capability) => ({
              key: capability.target,
              title: capability.name,
              target: capability.target,
              readiness: capability.readiness,
              shape: capability.parameter_shape,
              shapeProjected: capability.parameter_shape_projected,
            }))}
          />
          <CapabilitySection
            title="MCP tools"
            capabilities={visible.mcp.map((capability) => ({
              key: capability.target,
              title: capability.tool,
              target: capability.target,
              readiness: capability.readiness,
              detail: `Server: ${capability.server}`,
              shape: capability.parameter_shape,
              shapeProjected: capability.parameter_shape_projected,
            }))}
          />
          <CapabilitySection
            title="Functions"
            capabilities={visible.functions.map((capability) => ({
              key: capability.target,
              title: capability.name,
              target: capability.target,
              readiness: capability.readiness,
            }))}
          />
        </>
      )}
    </div>
  )
}

function PartialCapabilityNotice({
  catalog,
}: {
  catalog: WorkflowAuthoringCapabilities
}) {
  const reasons = [
    ...(catalog.mcp_status === "unavailable" ? ["MCP unavailable"] : []),
    ...(catalog.mcp_status === "disabled" ? ["MCP disabled"] : []),
    ...catalog.limits.map((limit) => capabilityLimitLabel(limit)),
  ]
  return (
    <div
      role="status"
      className="rounded-md border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-xs"
    >
      <div className="font-medium">Partial capability catalog</div>
      <div className="text-muted-foreground mt-1">
        Only the safely projected entries below are shown
        {reasons.length > 0 ? `: ${reasons.join(", ")}.` : "."}
      </div>
    </div>
  )
}

interface CapabilitySectionEntry {
  key: string
  title: string
  target: string
  readiness: WorkflowCapabilityReadiness
  detail?: string
  shape?: WorkflowCapabilityParameterShape
  shapeProjected?: boolean
}

function CapabilitySection({
  title,
  capabilities,
}: {
  title: string
  capabilities: CapabilitySectionEntry[]
}) {
  if (capabilities.length === 0) {
    return null
  }
  return (
    <section aria-labelledby={`capability-${sectionID(title)}`}>
      <div className="mb-2 flex items-center gap-2">
        <h3
          id={`capability-${sectionID(title)}`}
          className="text-sm font-medium"
        >
          {title}
        </h3>
        <Badge variant="outline">{capabilities.length}</Badge>
      </div>
      <ul className="grid gap-2">
        {capabilities.map((capability) => (
          <CapabilityRow key={capability.key} capability={capability} />
        ))}
      </ul>
    </section>
  )
}

function CapabilityRow({ capability }: { capability: CapabilitySectionEntry }) {
  const clipboard = useCopyToClipboard()
  const [copyFailed, setCopyFailed] = useState(false)
  const ready = capability.readiness === "ready"

  const copyTarget = async () => {
    setCopyFailed(false)
    try {
      const copied = await clipboard.copy(capability.target)
      setCopyFailed(!copied)
    } catch {
      setCopyFailed(true)
    }
  }

  return (
    <li className="border-border/70 min-w-0 rounded-md border p-3">
      <div className="flex min-w-0 items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="text-sm font-medium break-all">
              {capability.title}
            </span>
            <ReadinessBadge readiness={capability.readiness} />
          </div>
          {capability.detail != null ? (
            <div className="text-muted-foreground mt-0.5 text-xs break-all">
              {capability.detail}
            </div>
          ) : null}
        </div>
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={!ready}
          onClick={() => void copyTarget()}
          aria-label={
            clipboard.isCopied
              ? `Copied ${capability.target}`
              : `Copy ${capability.target}`
          }
          title={
            clipboard.isCopied
              ? `Copied ${capability.target}`
              : ready
                ? `Copy ${capability.target}`
                : "Only ready capability targets can be copied"
          }
        >
          {clipboard.isCopied ? (
            <IconCheck className="size-4" />
          ) : (
            <IconCopy className="size-4" />
          )}
          <span className="hidden sm:inline">
            {clipboard.isCopied ? "Copied" : "Copy"}
          </span>
        </Button>
      </div>
      <code className="bg-muted mt-2 block rounded px-2 py-1.5 text-xs break-all whitespace-pre-wrap">
        {capability.target}
      </code>
      <div aria-live="polite" className="mt-1 min-h-4 text-xs">
        {clipboard.isCopied ? (
          <span className="text-emerald-600">Target copied.</span>
        ) : copyFailed ? (
          <span className="text-destructive">
            Could not copy the target. Copy it from the text above.
          </span>
        ) : null}
      </div>
      {capability.shapeProjected === false ? (
        <div className="text-muted-foreground mt-2 text-xs">
          Parameter shape unavailable in the safe projection.
        </div>
      ) : capability.shape != null ? (
        <div className="mt-2">
          <div className="text-muted-foreground mb-1 text-xs font-medium">
            Parameters
          </div>
          <ParameterShape shape={capability.shape} />
        </div>
      ) : null}
    </li>
  )
}

function ReadinessBadge({
  readiness,
}: {
  readiness: WorkflowCapabilityReadiness
}) {
  return (
    <Badge
      variant={
        readiness === "ready"
          ? "secondary"
          : readiness === "unavailable" ||
              readiness === "invalid_configuration" ||
              readiness === "name_collision"
            ? "destructive"
            : "outline"
      }
    >
      {readiness.replaceAll("_", " ")}
    </Badge>
  )
}

function ParameterShape({
  shape,
}: {
  shape: WorkflowCapabilityParameterShape
}) {
  const hasDetails =
    shape.properties != null ||
    shape.items != null ||
    shape.enum != null ||
    shape.additional_properties != null
  return (
    <div className="border-border/70 grid gap-1.5 rounded border p-2 text-xs">
      <div className="flex flex-wrap gap-1.5">
        <Badge variant="outline">{shape.type ?? "unspecified"}</Badge>
        {shape.additional_properties != null ? (
          <span className="text-muted-foreground">
            Additional properties:{" "}
            {"allowed" in shape.additional_properties
              ? shape.additional_properties.allowed
                ? "allowed"
                : "not allowed"
              : "typed"}
          </span>
        ) : null}
      </div>
      {shape.enum != null ? (
        <div className="break-all">
          <span className="text-muted-foreground">Allowed values: </span>
          <span>{shape.enum.map(parameterEnumLabel).join(", ")}</span>
        </div>
      ) : null}
      {shape.properties != null && shape.properties.length > 0 ? (
        <ul className="grid gap-1.5">
          {shape.properties.map((property) => (
            <li
              key={property.name}
              className="border-border/60 border-l-2 pl-2"
            >
              <div className="font-mono break-all">
                {property.name}
                {property.required ? (
                  <span className="text-destructive ml-1 font-sans">
                    required
                  </span>
                ) : null}
              </div>
              <ParameterShape shape={property.shape} />
            </li>
          ))}
        </ul>
      ) : null}
      {shape.items != null ? (
        <div className="border-border/60 border-l-2 pl-2">
          <div className="text-muted-foreground mb-1">Array items</div>
          <ParameterShape shape={shape.items} />
        </div>
      ) : null}
      {shape.additional_properties != null &&
      "shape" in shape.additional_properties ? (
        <div className="border-border/60 border-l-2 pl-2">
          <div className="text-muted-foreground mb-1">
            Additional property values
          </div>
          <ParameterShape shape={shape.additional_properties.shape} />
        </div>
      ) : null}
      {!hasDetails && shape.type == null ? (
        <span className="text-muted-foreground">No projected constraints.</span>
      ) : null}
    </div>
  )
}

function boundedCapabilitySearch(value: string) {
  const encoder = new TextEncoder()
  let result = ""
  for (const character of value) {
    if (encoder.encode(result + character).byteLength > searchMaximumBytes) {
      break
    }
    result += character
  }
  return result
}

function capabilityMatches(search: string, ...values: string[]) {
  return (
    search === "" ||
    values.some((value) => value.toLowerCase().includes(search))
  )
}

function parameterEnumLabel(value: string | number | boolean | null) {
  return value === null
    ? "null"
    : typeof value === "string"
      ? JSON.stringify(value)
      : String(value)
}

function capabilityLimitLabel(limit: string) {
  return limit.replaceAll("_", " ")
}

function sectionID(title: string) {
  return title.toLowerCase().replaceAll(" ", "-")
}

function capabilityErrorMessage(error: unknown) {
  return error instanceof Error ? error.message : "Try again."
}
