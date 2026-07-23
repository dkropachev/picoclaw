import {
  IconArrowLeft,
  IconArrowsShuffle,
  IconBraces,
  IconCheck,
  IconGitBranch,
  IconHandMove,
  IconLayoutList,
  IconLoader2,
  IconPlus,
  IconRefresh,
  IconRoute,
  IconTrash,
  IconZoomIn,
  IconZoomOut,
  IconZoomReset,
} from "@tabler/icons-react"
import { useNavigate } from "@tanstack/react-router"
import {
  type PointerEvent as ReactPointerEvent,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"
import { useTranslation } from "react-i18next"

import {
  type ModelInfo,
  type ModelProviderOption,
  type ModelRouterBlock,
  type ModelRouterConfig,
  addModel,
  fetchUpstreamModels,
  getModels,
  updateModel,
} from "@/api/models"
import { type OAuthProviderStatus, getOAuthProviders } from "@/api/oauth"
import { ConfigChangeNotice } from "@/components/config-change-notice"
import { PageHeader } from "@/components/page-header"
import { Field } from "@/components/shared-form"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { cn } from "@/lib/utils"
import { refreshGatewayState } from "@/store/gateway"

import { getEffectiveAPIBase } from "../models/model-provider-form-shared"
import {
  getCanonicalProviderKey,
  getProviderCatalogEntry,
  providerSupportsFetch,
} from "../models/provider-registry"

type EditorMode = "visual" | "json"
type BlockType = "account" | "load_balance"
type RouterStrategy = "blind" | "tokens_spent" | "closest_limit"

interface RouterAccount {
  id: string
  label: string
  detail: string
  provider: OAuthProviderStatus["provider"]
  authMethod?: string
  credentialID: string
  status: OAuthProviderStatus["status"]
}

interface AccountRouterEditorPageProps {
  mode: "create" | "edit"
  modelIndex?: number
}

type Point = {
  x: number
  y: number
}

type BlockPositions = Record<string, Point>

type CanvasDragState =
  | {
      type: "node"
      pointerID: number
      blockID: string
      startPointer: Point
      startPosition: Point
    }
  | {
      type: "pan"
      pointerID: number
      startPointer: Point
      startPan: Point
    }

const CREDENTIAL_ACCOUNT_PREFIX = "credential:"
const DIAGRAM_NODE_WIDTH = 280
const DIAGRAM_NODE_HEIGHT = 120
const DIAGRAM_LANE_GAP = 160
const DIAGRAM_ROW_GAP = 72
const DIAGRAM_VIEWBOX_WIDTH = 1120
const DIAGRAM_VIEWBOX_HEIGHT = 620
const SCALE_OPTIONS = [50, 75, 100, 125, 150, 200]
const EMPTY_ROUTER: ModelRouterConfig = {
  enabled: true,
  entry: "",
  refresh_interval_seconds: 60,
  blocks: [],
}
const NONE_VALUE = "__none"

function cloneRouterConfig(config: ModelRouterConfig): ModelRouterConfig {
  const blocks = config.blocks && config.blocks.length > 0 ? config.blocks : []
  return {
    enabled: config.enabled !== false,
    entry: config.entry || blocks[0]?.id || "",
    refresh_interval_seconds: config.refresh_interval_seconds || 60,
    blocks: blocks.map((block) => ({
      ...block,
      accounts: block.accounts ? [...block.accounts] : undefined,
    })),
  }
}

function normalizeRouterConfig(config?: ModelRouterConfig): ModelRouterConfig {
  const router = cloneRouterConfig(config ?? EMPTY_ROUTER)
  const blocks = router.blocks ?? []
  if (!blocks.some((block) => block.id === router.entry)) {
    router.entry = blocks[0]?.id || ""
  }
  return router
}

function formatRouterConfig(config: ModelRouterConfig): string {
  return JSON.stringify(config, null, 2)
}

function safeParseRouterConfig(raw: string): ModelRouterConfig | null {
  try {
    const parsed = JSON.parse(raw) as ModelRouterConfig
    if (
      !parsed ||
      typeof parsed !== "object" ||
      !Array.isArray(parsed.blocks)
    ) {
      return null
    }
    return normalizeRouterConfig(parsed)
  } catch {
    return null
  }
}

function isRouterModel(model: ModelInfo): boolean {
  return model.provider === "router" || model.router != null
}

function accountRefForCredential(credentialID: string): string {
  return `${CREDENTIAL_ACCOUNT_PREFIX}${credentialID}`
}

function defaultCredentialAuthMethod(
  provider: OAuthProviderStatus["provider"],
): string {
  switch (provider) {
    case "anthropic":
      return "token"
    case "google-antigravity":
      return "oauth"
    default:
      return "oauth"
  }
}

function credentialDisplayID(status: OAuthProviderStatus): string {
  return status.credential_id || status.provider
}

function credentialLabel(status: OAuthProviderStatus): string {
  const credentialID = credentialDisplayID(status)
  if (status.email) return `${status.display_name}: ${status.email}`
  if (status.account_id) return `${status.display_name}: ${status.account_id}`
  if (credentialID === status.provider) return `${status.display_name}: default`
  return `${status.display_name}: ${credentialID}`
}

function credentialDetail(status: OAuthProviderStatus): string {
  const parts = [
    credentialDisplayID(status),
    status.project_id,
    status.auth_method,
    status.status,
  ].filter(Boolean)
  return parts.join(" / ")
}

function flattenCredentialAccounts(
  statuses: OAuthProviderStatus[],
): RouterAccount[] {
  const seen = new Set<string>()
  const accounts: RouterAccount[] = []
  for (const providerStatus of statuses) {
    const credentials =
      providerStatus.credentials && providerStatus.credentials.length > 0
        ? providerStatus.credentials
        : [providerStatus]
    for (const credential of credentials) {
      if (!credential.logged_in) continue
      const credentialID = credentialDisplayID(credential)
      const id = accountRefForCredential(credentialID)
      if (seen.has(id)) continue
      seen.add(id)
      accounts.push({
        id,
        label: credentialLabel(credential),
        detail: credentialDetail(credential),
        provider: credential.provider,
        authMethod:
          credential.auth_method ||
          defaultCredentialAuthMethod(credential.provider),
        credentialID,
        status: credential.status,
      })
    }
  }
  return accounts.sort((a, b) => a.label.localeCompare(b.label))
}

function accountByID(accounts: RouterAccount[]): Map<string, RouterAccount> {
  return new Map(accounts.map((account) => [account.id, account]))
}

function routerAccountNames(config: ModelRouterConfig | null): string[] {
  const seen = new Set<string>()
  const names: string[] = []
  const add = (name?: string) => {
    const trimmed = name?.trim()
    if (!trimmed || seen.has(trimmed)) return
    seen.add(trimmed)
    names.push(trimmed)
  }

  for (const block of config?.blocks ?? []) {
    if (block.type === "account") {
      add(block.account)
    } else if (block.type === "load_balance") {
      for (const account of block.accounts ?? []) add(account)
    }
  }
  return names
}

function normalizeModelID(modelID: string): string {
  return modelID.trim().toLowerCase()
}

function uniqueModelIDs(modelIDs: string[]): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const modelID of modelIDs) {
    const trimmed = modelID.trim()
    const normalized = normalizeModelID(trimmed)
    if (!trimmed || seen.has(normalized)) continue
    seen.add(normalized)
    out.push(trimmed)
  }
  return out
}

function intersectModelIDs(lists: string[][]): string[] {
  if (lists.length === 0) return []
  const normalizedSets = lists.map(
    (list) => new Set(list.map((modelID) => normalizeModelID(modelID))),
  )
  return uniqueModelIDs(lists[0]).filter((modelID) =>
    normalizedSets.every((set) => set.has(normalizeModelID(modelID))),
  )
}

async function fetchAccountModels(
  account: RouterAccount,
  providerOptions?: ModelProviderOption[],
): Promise<string[]> {
  const provider = getCanonicalProviderKey(account.provider, providerOptions)
  const catalogEntry = getProviderCatalogEntry(provider, providerOptions)
  if (!provider || !providerSupportsFetch(provider, providerOptions)) {
    return uniqueModelIDs(catalogEntry?.commonModels ?? [])
  }
  const apiBase = getEffectiveAPIBase(provider, "", providerOptions)
  const response = await fetchUpstreamModels({
    provider,
    api_base: apiBase,
    auth_method: account.authMethod || undefined,
    credential_id: account.credentialID,
  })
  return uniqueModelIDs(response.models.map((item) => item.id))
}

function nextBlockID(blocks: ModelRouterBlock[], type: BlockType): string {
  const prefix = type === "load_balance" ? "load-balancer" : "account"
  const seen = new Set(blocks.map((block) => block.id))
  for (let i = 1; i < 1000; i++) {
    const id = `${prefix}-${i}`
    if (!seen.has(id)) return id
  }
  return `${prefix}-${Date.now()}`
}

function newBlock(
  type: BlockType,
  blocks: ModelRouterBlock[],
): ModelRouterBlock {
  const id = nextBlockID(blocks, type)
  if (type === "load_balance") {
    return {
      id,
      type: "load_balance",
      accounts: [],
      strategy: "blind",
    }
  }
  return {
    id,
    type: "account",
    account: "",
  }
}

function nextScalePercent(current: number, direction: "in" | "out"): number {
  const nearestIndex = SCALE_OPTIONS.reduce((bestIndex, option, index) => {
    const bestDistance = Math.abs(SCALE_OPTIONS[bestIndex] - current)
    const distance = Math.abs(option - current)
    return distance < bestDistance ? index : bestIndex
  }, 0)
  const nextIndex =
    direction === "in"
      ? Math.min(SCALE_OPTIONS.length - 1, nearestIndex + 1)
      : Math.max(0, nearestIndex - 1)
  return SCALE_OPTIONS[nextIndex]
}

function fallbackLayoutLanes(config: ModelRouterConfig): string[][] {
  const blocks = config.blocks ?? []
  const byID = new Map(blocks.map((block) => [block.id, block]))
  const visited = new Set<string>()

  const collectLane = (startID: string): string[] => {
    const lane: string[] = []
    let nextID = startID
    const local = new Set<string>()
    while (nextID && byID.has(nextID) && !visited.has(nextID)) {
      if (local.has(nextID)) break
      local.add(nextID)
      visited.add(nextID)
      lane.push(nextID)
      nextID = byID.get(nextID)?.fallback ?? ""
    }
    return lane
  }

  const lanes: string[][] = []
  if (config.entry && byID.has(config.entry)) {
    const entryLane = collectLane(config.entry)
    if (entryLane.length > 0) lanes.push(entryLane)
  }

  for (const block of blocks) {
    if (visited.has(block.id)) continue
    const lane = collectLane(block.id)
    if (lane.length > 0) lanes.push(lane)
  }

  return lanes
}

function createFallbackPileLayout(config: ModelRouterConfig): BlockPositions {
  const positions: BlockPositions = {}
  const lanes = fallbackLayoutLanes(config)
  for (const [laneIndex, lane] of lanes.entries()) {
    for (const [rowIndex, blockID] of lane.entries()) {
      positions[blockID] = {
        x: 40 + laneIndex * (DIAGRAM_NODE_WIDTH + DIAGRAM_LANE_GAP),
        y: 40 + rowIndex * (DIAGRAM_NODE_HEIGHT + DIAGRAM_ROW_GAP),
      }
    }
  }
  return positions
}

function reconcileBlockPositions(
  config: ModelRouterConfig,
  current: BlockPositions,
): BlockPositions {
  const fallbackPositions = createFallbackPileLayout(config)
  const next: BlockPositions = {}
  for (const block of config.blocks ?? []) {
    next[block.id] = current[block.id] ??
      fallbackPositions[block.id] ?? {
        x: 40,
        y: 40,
      }
  }
  return next
}

function trimDiagramText(value: string, max = 26): string {
  if (value.length <= max) return value
  return `${value.slice(0, max - 1)}...`
}

function blockTitle(block: ModelRouterBlock): string {
  return block.type === "load_balance" ? "Load Balancer" : "Account"
}

function blockAccountSummary(
  block: ModelRouterBlock,
  accountsByID: Map<string, RouterAccount>,
): string {
  if (block.type === "account") {
    return accountsByID.get(block.account ?? "")?.label ?? block.account ?? ""
  }
  const accounts = block.accounts ?? []
  if (accounts.length === 0) return ""
  return accounts
    .map((account) => accountsByID.get(account)?.label ?? account)
    .join(", ")
}

function validateRouterConfig(
  config: ModelRouterConfig,
  t: ReturnType<typeof useTranslation>["t"],
): string {
  if (!config.enabled) return t("models.router.errorRawDisabled")
  const blocks = config.blocks ?? []
  if (blocks.length === 0) return t("models.router.errorNoBlocks")
  if (!config.entry?.trim()) return t("models.router.errorRawEntry")
  const ids = new Set<string>()
  for (const block of blocks) {
    const id = block.id.trim()
    if (!id) return t("models.router.errorRawBlocks")
    if (ids.has(id)) return t("models.router.errorDuplicateBlock")
    ids.add(id)
  }
  if (!ids.has(config.entry)) return t("models.router.errorRawEntry")
  for (const block of blocks) {
    if (block.type === "account") {
      if (!block.account?.trim()) {
        return t("models.router.errorPrimaryRequired")
      }
    } else if (block.type === "load_balance") {
      const accounts = uniqueModelIDs(block.accounts ?? [])
      if (accounts.length === 0) return t("models.router.errorPoolRequired")
      if (accounts.length !== (block.accounts ?? []).filter(Boolean).length) {
        return t("models.router.errorDuplicateAccounts")
      }
      const strategy = block.strategy || "blind"
      if (!["blind", "tokens_spent", "closest_limit"].includes(strategy)) {
        return t("models.router.errorStrategy")
      }
    } else {
      return t("models.router.errorRawBlocks")
    }
    if (block.fallback && !ids.has(block.fallback)) {
      return t("models.router.errorRawFallback")
    }
  }
  return validateFallbackAcyclic(config, t)
}

function validateFallbackAcyclic(
  config: ModelRouterConfig,
  t: ReturnType<typeof useTranslation>["t"],
): string {
  const byID = new Map((config.blocks ?? []).map((block) => [block.id, block]))
  const visiting = new Set<string>()
  const visited = new Set<string>()
  const walk = (id: string): boolean => {
    if (!id || visited.has(id)) return true
    if (visiting.has(id)) return false
    const block = byID.get(id)
    if (!block) return true
    visiting.add(id)
    const ok = walk(block.fallback ?? "")
    visiting.delete(id)
    visited.add(id)
    return ok
  }
  return walk(config.entry ?? "") ? "" : t("models.router.errorCycle")
}

export function AccountRouterEditorPage({
  mode,
  modelIndex,
}: AccountRouterEditorPageProps) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState("")
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState("")
  const [models, setModels] = useState<ModelInfo[]>([])
  const [providerOptions, setProviderOptions] = useState<ModelProviderOption[]>(
    [],
  )
  const [accounts, setAccounts] = useState<RouterAccount[]>([])
  const [modelName, setModelName] = useState("")
  const [sharedModel, setSharedModel] = useState("")
  const [routerConfig, setRouterConfig] = useState<ModelRouterConfig>(
    normalizeRouterConfig(),
  )
  const [editorMode, setEditorMode] = useState<EditorMode>("visual")
  const [rawJson, setRawJson] = useState(formatRouterConfig(EMPTY_ROUTER))
  const [rawError, setRawError] = useState("")
  const [selectedBlockID, setSelectedBlockID] = useState("account-1")
  const [blockPositions, setBlockPositions] = useState<BlockPositions>(() =>
    createFallbackPileLayout(EMPTY_ROUTER),
  )
  const [autoArrange, setAutoArrange] = useState(true)
  const [sharedModels, setSharedModels] = useState<string[]>([])
  const [sharedModelsLoading, setSharedModelsLoading] = useState(false)
  const [sharedModelsError, setSharedModelsError] = useState("")
  const [sharedModelsRefreshKey, setSharedModelsRefreshKey] = useState(0)

  const isEdit = mode === "edit"
  const editingModel = useMemo(
    () => models.find((item) => item.index === modelIndex) ?? null,
    [modelIndex, models],
  )
  const accountsByID = useMemo(() => accountByID(accounts), [accounts])
  const selectedBlock =
    routerConfig.blocks?.find((block) => block.id === selectedBlockID) ??
    routerConfig.blocks?.[0]
  const activeAccountNames = useMemo(
    () => routerAccountNames(routerConfig),
    [routerConfig],
  )
  const activeAccountsResolved =
    activeAccountNames.length > 0 &&
    activeAccountNames.every((name) => accountsByID.has(name))
  const activeAccountKey = activeAccountNames.join("\u0000")
  const sharedModelAllowed =
    !sharedModel ||
    sharedModels.some(
      (modelID) => normalizeModelID(modelID) === normalizeModelID(sharedModel),
    )
  const existingNames = useMemo(
    () =>
      new Set(
        models
          .filter((item) => item.index !== editingModel?.index)
          .map((item) => item.model_name),
      ),
    [editingModel?.index, models],
  )

  const loadData = useCallback(async () => {
    setLoading(true)
    setLoadError("")
    try {
      const [modelsData, oauthData] = await Promise.all([
        getModels(),
        getOAuthProviders(),
      ])
      setModels(modelsData.models)
      setProviderOptions(modelsData.provider_options || [])
      const nextAccounts = flattenCredentialAccounts(oauthData.providers)
      setAccounts(nextAccounts)

      const nextModel =
        mode === "edit"
          ? (modelsData.models.find((item) => item.index === modelIndex) ??
            null)
          : null
      if (mode === "edit" && (!nextModel || !isRouterModel(nextModel))) {
        setLoadError(t("models.router.errorNotFound"))
        return
      }

      const nextRouter = normalizeRouterConfig(nextModel?.router)
      setModelName(nextModel?.model_name ?? "")
      setSharedModel(
        nextModel?.model && nextModel.model !== nextModel.model_name
          ? nextModel.model
          : "",
      )
      setRouterConfig(nextRouter)
      setBlockPositions(createFallbackPileLayout(nextRouter))
      setAutoArrange(true)
      setRawJson(formatRouterConfig(nextRouter))
      setSelectedBlockID(nextRouter.entry || nextRouter.blocks?.[0]?.id || "")
      setError("")
      setRawError("")
    } catch (err) {
      setLoadError(err instanceof Error ? err.message : t("models.loadError"))
    } finally {
      setLoading(false)
    }
  }, [mode, modelIndex, t])

  useEffect(() => {
    void loadData()
  }, [loadData])

  useEffect(() => {
    if (editorMode !== "visual") return
    setRawJson(formatRouterConfig(routerConfig))
    setRawError("")
  }, [editorMode, routerConfig])

  useEffect(() => {
    setBlockPositions((current) =>
      autoArrange
        ? createFallbackPileLayout(routerConfig)
        : reconcileBlockPositions(routerConfig, current),
    )
  }, [autoArrange, routerConfig])

  useEffect(() => {
    const activeAccounts = activeAccountNames
      .map((name) => accountsByID.get(name))
      .filter((account): account is RouterAccount => account != null)

    if (
      activeAccounts.length === 0 ||
      activeAccounts.length !== activeAccountNames.length
    ) {
      setSharedModels([])
      setSharedModelsLoading(false)
      setSharedModelsError("")
      return
    }

    let cancelled = false
    setSharedModelsLoading(true)
    setSharedModelsError("")
    Promise.all(
      activeAccounts.map((account) =>
        fetchAccountModels(account, providerOptions),
      ),
    )
      .then((lists) => {
        if (cancelled) return
        setSharedModels(intersectModelIDs(lists))
      })
      .catch((err) => {
        if (cancelled) return
        setSharedModels([])
        setSharedModelsError(
          err instanceof Error
            ? err.message
            : t("models.router.sharedModelsError"),
        )
      })
      .finally(() => {
        if (!cancelled) setSharedModelsLoading(false)
      })

    return () => {
      cancelled = true
    }
  }, [
    activeAccountKey,
    activeAccountNames,
    accountsByID,
    providerOptions,
    sharedModelsRefreshKey,
    t,
  ])

  const updateRouter = (next: ModelRouterConfig) => {
    setRouterConfig(normalizeRouterConfig(next))
    if (error) setError("")
  }

  const switchEditorMode = (nextMode: EditorMode) => {
    if (nextMode === editorMode) return
    if (nextMode === "json") {
      setRawJson(formatRouterConfig(routerConfig))
      setRawError("")
      setEditorMode("json")
      return
    }
    const parsed = safeParseRouterConfig(rawJson)
    if (!parsed) {
      setRawError(t("models.router.rawInvalid"))
      return
    }
    setRouterConfig(parsed)
    setBlockPositions(createFallbackPileLayout(parsed))
    setAutoArrange(true)
    setSelectedBlockID(parsed.entry || parsed.blocks?.[0]?.id || "")
    setRawError("")
    setEditorMode("visual")
  }

  const addBlock = (type: BlockType) => {
    const block = newBlock(type, routerConfig.blocks ?? [])
    updateRouter({
      ...routerConfig,
      entry: routerConfig.entry || block.id,
      blocks: [...(routerConfig.blocks ?? []), block],
    })
    setSelectedBlockID(block.id)
  }

  const updateBlock = (
    blockID: string,
    updater: (block: ModelRouterBlock) => ModelRouterBlock,
  ) => {
    updateRouter({
      ...routerConfig,
      blocks: (routerConfig.blocks ?? []).map((block) =>
        block.id === blockID ? updater(block) : block,
      ),
    })
  }

  const updateBlockID = (oldID: string, nextID: string) => {
    setBlockPositions((current) => {
      const position = current[oldID]
      if (!position) return current
      const rest = { ...current }
      delete rest[oldID]
      return {
        ...rest,
        [nextID]: position,
      }
    })
    updateRouter({
      ...routerConfig,
      entry: routerConfig.entry === oldID ? nextID : routerConfig.entry,
      blocks: (routerConfig.blocks ?? []).map((block) => ({
        ...block,
        id: block.id === oldID ? nextID : block.id,
        fallback: block.fallback === oldID ? nextID : block.fallback,
      })),
    })
    setSelectedBlockID(nextID)
  }

  const handleMoveBlock = (blockID: string, position: Point) => {
    setAutoArrange(false)
    setBlockPositions((current) => ({
      ...current,
      [blockID]: position,
    }))
  }

  const handleAutoLayout = () => {
    setBlockPositions(createFallbackPileLayout(routerConfig))
    setAutoArrange(true)
  }

  const removeBlock = (blockID: string) => {
    const nextBlocks = (routerConfig.blocks ?? []).filter(
      (block) => block.id !== blockID,
    )
    const cleanedBlocks = nextBlocks.map((block) => ({
      ...block,
      fallback: block.fallback === blockID ? undefined : block.fallback,
    }))
    const nextEntry =
      routerConfig.entry === blockID
        ? cleanedBlocks[0]?.id || ""
        : routerConfig.entry
    updateRouter({
      ...routerConfig,
      entry: nextEntry,
      blocks: cleanedBlocks,
    })
    setSelectedBlockID(cleanedBlocks[0]?.id || "")
  }

  const toggleLoadBalanceAccount = (blockID: string, accountID: string) => {
    updateBlock(blockID, (block) => {
      const accounts = block.accounts ?? []
      const exists = accounts.includes(accountID)
      return {
        ...block,
        accounts: exists
          ? accounts.filter((item) => item !== accountID)
          : [...accounts, accountID],
      }
    })
  }

  const validate = () => {
    const trimmedName = modelName.trim()
    if (!trimmedName) return t("models.router.errorNameRequired")
    if (existingNames.has(trimmedName)) return t("models.router.errorDuplicate")
    if (!sharedModel.trim()) return t("models.router.errorModelRequired")
    const routerValidation = validateRouterConfig(routerConfig, t)
    if (routerValidation) return routerValidation
    if (activeAccountNames.length === 0) return t("models.router.noAccounts")
    if (editorMode === "visual" && !activeAccountsResolved) {
      return t("models.router.errorMissingAccounts")
    }
    if (activeAccountsResolved) {
      if (sharedModelsLoading) return t("models.router.sharedModelsLoading")
      if (sharedModelsError) return sharedModelsError
      if (sharedModels.length === 0) return t("models.router.noSharedModels")
      if (!sharedModelAllowed) return t("models.router.errorModelNotShared")
    }
    return ""
  }

  const save = async () => {
    if (editorMode === "json") {
      const parsed = safeParseRouterConfig(rawJson)
      if (!parsed) {
        setRawError(t("models.router.rawInvalid"))
        return
      }
      setRouterConfig(parsed)
    }

    const validationError = validate()
    if (validationError) {
      setError(validationError)
      return
    }
    setSaving(true)
    setError("")
    try {
      const payload = {
        model_name: modelName.trim(),
        provider: "router",
        model: sharedModel.trim(),
        router:
          editorMode === "json"
            ? (safeParseRouterConfig(rawJson) ?? routerConfig)
            : routerConfig,
      }
      if (isEdit && editingModel) {
        await updateModel(editingModel.index, payload)
      } else {
        await addModel(payload)
      }
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        isEdit ? t("models.router.saveSuccess") : t("models.router.addSuccess"),
        payload.model_name,
        gateway?.restartRequired === true,
      )
      void navigate({ to: "/accounts" })
    } catch (err) {
      setError(
        err instanceof Error ? err.message : t("models.router.saveError"),
      )
    } finally {
      setSaving(false)
    }
  }

  if (loading) {
    return (
      <div className="flex h-full flex-col">
        <PageHeader title={t("models.router.title")} />
        <div className="text-muted-foreground flex items-center gap-2 px-6 py-10 text-sm">
          <IconLoader2 className="size-4 animate-spin" />
          {t("credentials.loading")}
        </div>
      </div>
    )
  }

  return (
    <div className="flex h-full flex-col">
      <PageHeader
        title={
          isEdit
            ? t("models.router.editPageTitle", {
                name: editingModel?.model_name ?? modelName,
              })
            : t("models.router.title")
        }
      >
        <Button
          variant="outline"
          size="sm"
          onClick={() => void navigate({ to: "/accounts" })}
        >
          <IconArrowLeft className="size-4" />
          {t("navigation.accounts")}
        </Button>
        <Button size="sm" onClick={() => void save()} disabled={saving}>
          {saving ? (
            <IconLoader2 className="size-4 animate-spin" />
          ) : (
            <IconRoute className="size-4" />
          )}
          {t("common.save")}
        </Button>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-6 sm:px-6">
        {loadError ? (
          <div className="bg-destructive/10 mt-4 rounded-lg px-4 py-3 text-sm">
            <p className="text-destructive">{loadError}</p>
            <Button
              className="mt-3"
              size="sm"
              variant="outline"
              onClick={() => void loadData()}
            >
              {t("models.retry")}
            </Button>
          </div>
        ) : (
          <div className="space-y-5">
            <div className="grid grid-cols-1 gap-4 pt-2 lg:grid-cols-[minmax(0,1fr)_320px]">
              <Field
                label={t("models.router.routerName")}
                hint={t("models.router.nameHint")}
                required
              >
                <Input
                  value={modelName}
                  onChange={(event) => setModelName(event.target.value)}
                  placeholder="account-router-main"
                  disabled={isEdit}
                  aria-label={t("models.router.routerName")}
                />
              </Field>
              <SharedModelField
                value={sharedModel}
                models={sharedModels}
                loading={sharedModelsLoading}
                error={sharedModelsError}
                disabled={
                  activeAccountNames.length === 0 || !activeAccountsResolved
                }
                allowed={sharedModelAllowed}
                onChange={(value) => setSharedModel(value)}
                onRefresh={() =>
                  setSharedModelsRefreshKey((current) => current + 1)
                }
              />
            </div>

            <EditorModeTabs mode={editorMode} onChange={switchEditorMode} />

            {editorMode === "visual" ? (
              <div className="grid min-h-[620px] grid-cols-1 gap-4 xl:grid-cols-[minmax(0,1fr)_420px]">
                <section className="border-border/80 bg-background min-h-0 rounded-lg border">
                  <div className="border-border/70 flex flex-wrap items-center justify-between gap-2 border-b p-3">
                    <div className="min-w-0">
                      <h3 className="text-sm font-semibold">
                        {t("models.router.diagram")}
                      </h3>
                      <p className="text-muted-foreground text-xs">
                        {t("models.router.diagramHint")}
                      </p>
                    </div>
                    <div className="flex flex-wrap gap-2">
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        onClick={() => addBlock("account")}
                      >
                        <IconPlus className="size-4" />
                        {t("models.router.addAccountBlock")}
                      </Button>
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        onClick={() => addBlock("load_balance")}
                      >
                        <IconPlus className="size-4" />
                        {t("models.router.addLoadBalanceBlock")}
                      </Button>
                    </div>
                  </div>
                  <RouterDiagram
                    routerConfig={routerConfig}
                    accountsByID={accountsByID}
                    positions={blockPositions}
                    selectedBlockID={selectedBlockID}
                    onSelect={setSelectedBlockID}
                    onMoveBlock={handleMoveBlock}
                    onAutoLayout={handleAutoLayout}
                  />
                </section>

                <aside className="border-border/80 bg-background rounded-lg border">
                  <div className="border-border/70 border-b p-3">
                    <Field
                      label={t("models.router.entryBlock")}
                      hint={t("models.router.entryHint")}
                    >
                      <BlockSelect
                        value={routerConfig.entry ?? ""}
                        blocks={routerConfig.blocks ?? []}
                        placeholder={t("models.router.selectBlock")}
                        ariaLabel={t("models.router.entryBlock")}
                        onChange={(value) =>
                          updateRouter({ ...routerConfig, entry: value })
                        }
                      />
                    </Field>
                  </div>
                  <BlockInspector
                    block={selectedBlock}
                    blocks={routerConfig.blocks ?? []}
                    accounts={accounts}
                    accountsByID={accountsByID}
                    onSelect={setSelectedBlockID}
                    onUpdateID={updateBlockID}
                    onUpdate={(blockID, updater) =>
                      updateBlock(blockID, updater)
                    }
                    onRemove={removeBlock}
                    onToggleAccount={toggleLoadBalanceAccount}
                  />
                </aside>
              </div>
            ) : (
              <RawRouterEditor
                value={rawJson}
                error={rawError}
                onChange={(value) => {
                  setRawJson(value)
                  setRawError("")
                  if (error) setError("")
                }}
              />
            )}

            <ConnectedAccountsPreview
              accounts={activeAccountNames}
              accountMap={accountsByID}
              missingLabel={t("models.router.accountMissing")}
            />

            {error && <p className="text-destructive text-sm">{error}</p>}
            <ConfigChangeNotice
              kind="save"
              title={t("common.saveChangesTitle")}
              description={t("models.unsavedPrompt")}
            />
          </div>
        )}
      </div>
    </div>
  )
}

function EditorModeTabs({
  mode,
  onChange,
}: {
  mode: EditorMode
  onChange: (mode: EditorMode) => void
}) {
  const { t } = useTranslation()
  const items: Array<{
    key: EditorMode
    label: string
    icon: typeof IconRoute
  }> = [
    { key: "visual", label: t("models.router.visualEditor"), icon: IconRoute },
    { key: "json", label: t("models.router.rawJson"), icon: IconBraces },
  ]
  return (
    <div className="bg-muted/60 grid grid-cols-2 gap-1 rounded-lg p-1">
      {items.map((item) => (
        <button
          key={item.key}
          type="button"
          onClick={() => onChange(item.key)}
          className={cn(
            "flex h-9 items-center justify-center gap-2 rounded-md text-sm font-medium transition-colors",
            mode === item.key
              ? "bg-background text-foreground shadow-xs"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          <item.icon className="size-4" />
          {item.label}
        </button>
      ))}
    </div>
  )
}

function SharedModelField({
  value,
  models,
  loading,
  error,
  disabled,
  allowed,
  onChange,
  onRefresh,
}: {
  value: string
  models: string[]
  loading: boolean
  error: string
  disabled: boolean
  allowed: boolean
  onChange: (value: string) => void
  onRefresh: () => void
}) {
  const { t } = useTranslation()
  return (
    <Field
      label={t("models.router.sharedModel")}
      hint={t("models.router.sharedModelHint")}
      required
    >
      <div className="flex gap-2">
        <Select
          value={value || NONE_VALUE}
          onValueChange={(nextValue) => {
            if (nextValue !== NONE_VALUE) onChange(nextValue)
          }}
          disabled={disabled || loading || models.length === 0}
        >
          <SelectTrigger
            className={cn(
              "min-w-0 flex-1",
              !allowed ? "border-destructive" : "",
            )}
            aria-label={t("models.router.sharedModel")}
          >
            <SelectValue
              placeholder={
                disabled
                  ? t("models.router.selectAccountsFirst")
                  : loading
                    ? t("models.router.sharedModelsLoading")
                    : t("models.router.selectSharedModel")
              }
            />
          </SelectTrigger>
          <SelectContent>
            {!value && (
              <SelectItem value={NONE_VALUE} disabled>
                {disabled
                  ? t("models.router.selectAccountsFirst")
                  : t("models.router.selectSharedModel")}
              </SelectItem>
            )}
            {models.map((modelID) => (
              <SelectItem key={modelID} value={modelID}>
                {modelID}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button
          type="button"
          variant="outline"
          size="icon"
          onClick={onRefresh}
          disabled={disabled || loading}
          aria-label={t("models.router.refreshSharedModels")}
          title={t("models.router.refreshSharedModels")}
        >
          {loading ? (
            <IconLoader2 className="size-4 animate-spin" />
          ) : (
            <IconRefresh className="size-4" />
          )}
        </Button>
      </div>
      {!disabled && !loading && models.length === 0 && !error && (
        <p className="text-muted-foreground text-xs">
          {t("models.router.noSharedModels")}
        </p>
      )}
      {error && <p className="text-destructive text-xs">{error}</p>}
      {!allowed && (
        <p className="text-destructive text-xs">
          {t("models.router.errorModelNotShared")}
        </p>
      )}
    </Field>
  )
}

function RouterDiagram({
  routerConfig,
  accountsByID,
  positions,
  selectedBlockID,
  onSelect,
  onMoveBlock,
  onAutoLayout,
}: {
  routerConfig: ModelRouterConfig
  accountsByID: Map<string, RouterAccount>
  positions: BlockPositions
  selectedBlockID: string
  onSelect: (blockID: string) => void
  onMoveBlock: (blockID: string, position: Point) => void
  onAutoLayout: () => void
}) {
  const { t } = useTranslation()
  const svgRef = useRef<SVGSVGElement | null>(null)
  const [scalePercent, setScalePercent] = useState(100)
  const [pan, setPan] = useState<Point>({ x: 0, y: 0 })
  const [drag, setDrag] = useState<CanvasDragState | null>(null)
  const blocks = routerConfig.blocks ?? []
  const scale = scalePercent / 100
  const resolvedPositions = useMemo(
    () => reconcileBlockPositions(routerConfig, positions),
    [positions, routerConfig],
  )
  const edges = blocks
    .map((block) => {
      const from = resolvedPositions[block.id]
      const to = block.fallback ? resolvedPositions[block.fallback] : undefined
      if (!from || !to) return null
      return { from, to, key: `${block.id}-${block.fallback}` }
    })
    .filter((edge): edge is NonNullable<typeof edge> => edge != null)

  useEffect(() => {
    const svg = svgRef.current
    if (!svg) return

    const handleNativeWheel = (event: WheelEvent) => {
      if (!event.shiftKey) return
      event.preventDefault()
      setScalePercent((current) =>
        nextScalePercent(current, event.deltaY < 0 ? "in" : "out"),
      )
    }

    svg.addEventListener("wheel", handleNativeWheel, { passive: false })
    return () => {
      svg.removeEventListener("wheel", handleNativeWheel)
    }
  }, [blocks.length])

  const viewportPoint = (clientX: number, clientY: number): Point => {
    const svg = svgRef.current
    if (!svg) return { x: 0, y: 0 }
    const rect = svg.getBoundingClientRect()
    return {
      x: ((clientX - rect.left) / rect.width) * DIAGRAM_VIEWBOX_WIDTH,
      y: ((clientY - rect.top) / rect.height) * DIAGRAM_VIEWBOX_HEIGHT,
    }
  }

  const handleCanvasPointerDown = (event: ReactPointerEvent<SVGSVGElement>) => {
    if (event.button !== 0) return
    setDrag({
      type: "pan",
      pointerID: event.pointerId,
      startPointer: viewportPoint(event.clientX, event.clientY),
      startPan: pan,
    })
    svgRef.current?.setPointerCapture(event.pointerId)
  }

  const handleNodePointerDown = (
    event: ReactPointerEvent<SVGGElement>,
    blockID: string,
  ) => {
    if (event.button !== 0) return
    event.stopPropagation()
    onSelect(blockID)
    setDrag({
      type: "node",
      pointerID: event.pointerId,
      blockID,
      startPointer: viewportPoint(event.clientX, event.clientY),
      startPosition: resolvedPositions[blockID] ?? { x: 40, y: 40 },
    })
    svgRef.current?.setPointerCapture(event.pointerId)
  }

  const handlePointerMove = (event: ReactPointerEvent<SVGSVGElement>) => {
    if (!drag || drag.pointerID !== event.pointerId) return
    const current = viewportPoint(event.clientX, event.clientY)
    if (drag.type === "pan") {
      setPan({
        x: drag.startPan.x + current.x - drag.startPointer.x,
        y: drag.startPan.y + current.y - drag.startPointer.y,
      })
      return
    }

    onMoveBlock(drag.blockID, {
      x: drag.startPosition.x + (current.x - drag.startPointer.x) / scale,
      y: drag.startPosition.y + (current.y - drag.startPointer.y) / scale,
    })
  }

  const finishDrag = (event: ReactPointerEvent<SVGSVGElement>) => {
    if (!drag || drag.pointerID !== event.pointerId) return
    if (svgRef.current?.hasPointerCapture(event.pointerId)) {
      svgRef.current.releasePointerCapture(event.pointerId)
    }
    setDrag(null)
  }

  const resetView = () => {
    setPan({ x: 0, y: 0 })
    setScalePercent(100)
  }

  if (blocks.length === 0) {
    return (
      <div className="text-muted-foreground p-6 text-sm">
        {t("models.router.noBlocksEmpty")}
      </div>
    )
  }

  return (
    <div className="min-h-0">
      <div className="border-border/70 flex flex-wrap items-center justify-between gap-2 border-b px-3 py-2">
        <div className="text-muted-foreground flex min-w-0 items-center gap-2 text-xs">
          <IconHandMove className="size-4 shrink-0" />
          <span>{t("models.router.canvas")}</span>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={onAutoLayout}
          >
            <IconLayoutList className="size-4" />
            {t("models.router.autoLayout")}
          </Button>
          <Button
            type="button"
            size="icon"
            variant="outline"
            onClick={() =>
              setScalePercent((current) => nextScalePercent(current, "out"))
            }
            aria-label={t("models.router.zoomOut")}
            title={t("models.router.zoomOut")}
          >
            <IconZoomOut className="size-4" />
          </Button>
          <Select
            value={String(scalePercent)}
            onValueChange={(value) => setScalePercent(Number(value))}
          >
            <SelectTrigger
              className="h-8 w-[104px]"
              aria-label={t("models.router.scale")}
            >
              <IconZoomIn className="size-4" />
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {SCALE_OPTIONS.map((value) => (
                <SelectItem key={value} value={String(value)}>
                  {value}%
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button
            type="button"
            size="icon"
            variant="outline"
            onClick={() =>
              setScalePercent((current) => nextScalePercent(current, "in"))
            }
            aria-label={t("models.router.zoomIn")}
            title={t("models.router.zoomIn")}
          >
            <IconZoomIn className="size-4" />
          </Button>
          <Button
            type="button"
            size="icon"
            variant="outline"
            onClick={resetView}
            aria-label={t("models.router.resetView")}
            title={t("models.router.resetView")}
          >
            <IconZoomReset className="size-4" />
          </Button>
        </div>
      </div>
      <div className="h-[560px] min-h-0 overflow-hidden">
        <svg
          ref={svgRef}
          className={cn(
            "text-foreground h-full w-full touch-none select-none",
            drag?.type === "pan" ? "cursor-grabbing" : "cursor-grab",
          )}
          viewBox={`0 0 ${DIAGRAM_VIEWBOX_WIDTH} ${DIAGRAM_VIEWBOX_HEIGHT}`}
          role="group"
          aria-label={t("models.router.diagram")}
          onPointerDown={handleCanvasPointerDown}
          onPointerMove={handlePointerMove}
          onPointerUp={finishDrag}
          onPointerCancel={finishDrag}
        >
          <defs>
            <pattern
              id="account-router-grid"
              width="32"
              height="32"
              patternUnits="userSpaceOnUse"
            >
              <path
                d="M 32 0 L 0 0 0 32"
                className="stroke-border/60 fill-none"
                strokeWidth="1"
              />
            </pattern>
            <marker
              id="account-router-arrow"
              markerWidth="8"
              markerHeight="8"
              refX="4"
              refY="4"
              orient="auto"
            >
              <path d="M0,0 L8,4 L0,8 Z" className="fill-primary" />
            </marker>
          </defs>
          <rect
            x="0"
            y="0"
            width={DIAGRAM_VIEWBOX_WIDTH}
            height={DIAGRAM_VIEWBOX_HEIGHT}
            fill="url(#account-router-grid)"
          />
          <g transform={`translate(${pan.x} ${pan.y}) scale(${scale})`}>
            {edges.map((edge) => {
              const startX = edge.from.x + DIAGRAM_NODE_WIDTH / 2
              const startY = edge.from.y + DIAGRAM_NODE_HEIGHT
              const endX = edge.to.x + DIAGRAM_NODE_WIDTH / 2
              const endY = edge.to.y
              const controlY = startY + (endY - startY) / 2
              return (
                <path
                  key={edge.key}
                  d={`M ${startX} ${startY} C ${startX} ${controlY}, ${endX} ${controlY}, ${endX} ${endY}`}
                  className="stroke-primary fill-none"
                  strokeWidth="2"
                  markerEnd="url(#account-router-arrow)"
                />
              )
            })}
            {blocks.map((block) => {
              const pos = resolvedPositions[block.id] ?? { x: 40, y: 40 }
              const selected = block.id === selectedBlockID
              const entry = block.id === routerConfig.entry
              const summary = blockAccountSummary(block, accountsByID)
              return (
                <g
                  key={block.id}
                  role="button"
                  tabIndex={0}
                  aria-label={t("models.router.editBlock", { id: block.id })}
                  transform={`translate(${pos.x} ${pos.y})`}
                  onPointerDown={(event) =>
                    handleNodePointerDown(event, block.id)
                  }
                  onClick={() => onSelect(block.id)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter" || event.key === " ") {
                      event.preventDefault()
                      onSelect(block.id)
                    }
                  }}
                  className="cursor-move outline-none"
                >
                  <rect
                    x="0"
                    y="0"
                    width={DIAGRAM_NODE_WIDTH}
                    height={DIAGRAM_NODE_HEIGHT}
                    rx="8"
                    className={cn(
                      "fill-background stroke-border",
                      selected && "stroke-primary",
                    )}
                    strokeWidth={selected ? 3 : 1.5}
                  />
                  <text
                    x="16"
                    y="28"
                    className="fill-foreground pointer-events-none text-[14px] font-semibold"
                  >
                    {trimDiagramText(block.id, 30)}
                  </text>
                  <text
                    x="16"
                    y="52"
                    className="fill-muted-foreground pointer-events-none text-[12px]"
                  >
                    {blockTitle(block)}
                  </text>
                  <text
                    x="16"
                    y="78"
                    className="fill-muted-foreground pointer-events-none text-[12px]"
                  >
                    {trimDiagramText(
                      summary || t("models.router.unconfigured"),
                      32,
                    )}
                  </text>
                  <text
                    x="16"
                    y="104"
                    className="fill-muted-foreground pointer-events-none text-[12px]"
                  >
                    {block.fallback
                      ? t("models.router.diagramFallback", {
                          block: block.fallback,
                        })
                      : t("models.router.diagramNoFallback")}
                  </text>
                  {entry && (
                    <g className="pointer-events-none">
                      <rect
                        x={DIAGRAM_NODE_WIDTH - 72}
                        y="12"
                        width="54"
                        height="22"
                        rx="6"
                        className="fill-primary"
                      />
                      <text
                        x={DIAGRAM_NODE_WIDTH - 45}
                        y="27"
                        textAnchor="middle"
                        className="fill-primary-foreground text-[11px] font-semibold"
                      >
                        {t("models.router.entryBadge")}
                      </text>
                    </g>
                  )}
                </g>
              )
            })}
          </g>
        </svg>
      </div>
    </div>
  )
}

function BlockInspector({
  block,
  blocks,
  accounts,
  accountsByID,
  onSelect,
  onUpdateID,
  onUpdate,
  onRemove,
  onToggleAccount,
}: {
  block?: ModelRouterBlock
  blocks: ModelRouterBlock[]
  accounts: RouterAccount[]
  accountsByID: Map<string, RouterAccount>
  onSelect: (blockID: string) => void
  onUpdateID: (oldID: string, nextID: string) => void
  onUpdate: (
    blockID: string,
    updater: (block: ModelRouterBlock) => ModelRouterBlock,
  ) => void
  onRemove: (blockID: string) => void
  onToggleAccount: (blockID: string, accountID: string) => void
}) {
  const { t } = useTranslation()
  if (!block) {
    return (
      <div className="text-muted-foreground p-4 text-sm">
        {t("models.router.noBlocksEmpty")}
      </div>
    )
  }
  return (
    <div className="space-y-4 p-4">
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">
          <h3 className="truncate text-sm font-semibold">{block.id}</h3>
          <p className="text-muted-foreground text-xs">{blockTitle(block)}</p>
        </div>
        <Button
          type="button"
          size="icon"
          variant="ghost"
          disabled={blocks.length <= 1}
          onClick={() => onRemove(block.id)}
          aria-label={t("models.router.removeBlock")}
          title={t("models.router.removeBlock")}
        >
          <IconTrash className="size-4" />
        </Button>
      </div>

      <Field label={t("models.router.blockId")}>
        <Input
          value={block.id}
          onChange={(event) => onUpdateID(block.id, event.target.value)}
          aria-label={t("models.router.blockId")}
        />
      </Field>

      <Field label={t("models.router.blockType")}>
        <Select
          value={block.type}
          onValueChange={(value) =>
            onUpdate(block.id, (current) => {
              const nextType = value as BlockType
              if (nextType === "load_balance") {
                return {
                  id: current.id,
                  type: "load_balance",
                  accounts:
                    current.type === "load_balance" ? current.accounts : [],
                  strategy:
                    current.type === "load_balance"
                      ? (current.strategy ?? "blind")
                      : "blind",
                  fallback: current.fallback,
                }
              }
              return {
                id: current.id,
                type: "account",
                account: current.type === "account" ? current.account : "",
                fallback: current.fallback,
              }
            })
          }
        >
          <SelectTrigger
            className="w-full"
            aria-label={t("models.router.blockType")}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="account">
              {t("models.router.accountBlock")}
            </SelectItem>
            <SelectItem value="load_balance">
              {t("models.router.loadBalanceBlock")}
            </SelectItem>
          </SelectContent>
        </Select>
      </Field>

      {block.type === "account" ? (
        <Field
          label={t("models.router.account")}
          hint={t("models.router.accountHint")}
          required
        >
          <AccountSelect
            value={block.account ?? ""}
            accounts={accounts}
            placeholder={t("models.router.selectAccount")}
            ariaLabel={t("models.router.account")}
            onChange={(value) =>
              onUpdate(block.id, (current) => ({
                ...current,
                account: value,
              }))
            }
          />
        </Field>
      ) : (
        <>
          <Field
            label={t("models.router.poolAccounts")}
            hint={t("models.router.poolHint")}
            required
          >
            <div className="grid grid-cols-1 gap-2">
              {accounts.length === 0 && (
                <p className="text-muted-foreground text-sm">
                  {t("models.router.noAccounts")}
                </p>
              )}
              {accounts.map((account) => {
                const selected = (block.accounts ?? []).includes(account.id)
                return (
                  <button
                    key={account.id}
                    type="button"
                    onClick={() => onToggleAccount(block.id, account.id)}
                    aria-label={account.label}
                    className={cn(
                      "border-border bg-background hover:bg-muted flex min-h-10 items-center justify-between gap-2 rounded-lg border px-3 py-2 text-left text-sm",
                      selected && "border-primary text-primary",
                    )}
                  >
                    <span className="min-w-0">
                      <span className="block truncate">{account.label}</span>
                      <span className="text-muted-foreground block truncate text-[11px]">
                        {account.detail}
                      </span>
                    </span>
                    {selected && <IconCheck className="size-4 shrink-0" />}
                  </button>
                )
              })}
            </div>
          </Field>
          <Field
            label={t("models.router.strategy")}
            hint={t("models.router.strategyHint")}
          >
            <Select
              value={block.strategy ?? "blind"}
              onValueChange={(value) =>
                onUpdate(block.id, (current) => ({
                  ...current,
                  strategy: value as RouterStrategy,
                }))
              }
            >
              <SelectTrigger
                className="w-full"
                aria-label={t("models.router.strategy")}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="blind">
                  {t("models.router.strategyBlind")}
                </SelectItem>
                <SelectItem value="tokens_spent">
                  {t("models.router.strategyTokens")}
                </SelectItem>
                <SelectItem value="closest_limit">
                  {t("models.router.strategyLimit")}
                </SelectItem>
              </SelectContent>
            </Select>
          </Field>
        </>
      )}

      <Field
        label={t("models.router.fallbackConnection")}
        hint={t("models.router.fallbackConnectionHint")}
      >
        <BlockSelect
          value={block.fallback ?? ""}
          blocks={blocks.filter((candidate) => candidate.id !== block.id)}
          placeholder={t("models.router.noFallback")}
          ariaLabel={t("models.router.fallbackConnection")}
          allowEmpty
          onChange={(value) =>
            onUpdate(block.id, (current) => ({
              ...current,
              fallback: value || undefined,
            }))
          }
        />
      </Field>

      <div className="border-border/80 rounded-lg border p-3">
        <p className="text-sm font-medium">
          {t("models.router.blockAccounts")}
        </p>
        <div className="mt-2 flex flex-wrap gap-2">
          {routerAccountNames({
            enabled: true,
            entry: block.id,
            blocks: [block],
          }).map((accountID) => {
            const account = accountsByID.get(accountID)
            return (
              <Badge
                key={accountID}
                variant={account ? "secondary" : "outline"}
              >
                {account?.label ?? accountID}
              </Badge>
            )
          })}
        </div>
      </div>

      <div className="flex flex-wrap gap-2">
        {blocks.map((candidate) => (
          <Button
            key={candidate.id}
            type="button"
            size="sm"
            variant={candidate.id === block.id ? "default" : "outline"}
            onClick={() => onSelect(candidate.id)}
          >
            {candidate.type === "load_balance" ? (
              <IconArrowsShuffle className="size-4" />
            ) : (
              <IconGitBranch className="size-4" />
            )}
            {candidate.id}
          </Button>
        ))}
      </div>
    </div>
  )
}

function RawRouterEditor({
  value,
  error,
  onChange,
}: {
  value: string
  error: string
  onChange: (value: string) => void
}) {
  const { t } = useTranslation()
  return (
    <Field
      label={t("models.router.rawJson")}
      hint={t("models.router.rawJsonHint")}
    >
      <Textarea
        value={value}
        onChange={(event) => onChange(event.target.value)}
        className="min-h-[520px] font-mono text-xs"
        spellCheck={false}
        aria-label={t("models.router.rawJson")}
      />
      {error && <p className="text-destructive text-xs">{error}</p>}
    </Field>
  )
}

function ConnectedAccountsPreview({
  accounts,
  accountMap,
  missingLabel,
}: {
  accounts: string[]
  accountMap: Map<string, RouterAccount>
  missingLabel: string
}) {
  const { t } = useTranslation()
  return (
    <div className="border-border/80 rounded-lg border p-3">
      <div className="mb-2 flex items-center gap-2 text-sm font-medium">
        <IconRoute className="size-4" />
        {t("models.router.connectedAccounts")}
      </div>
      {accounts.length === 0 ? (
        <p className="text-muted-foreground text-sm">
          {t("models.router.noConnectedAccounts")}
        </p>
      ) : (
        <div className="flex flex-wrap gap-2">
          {accounts.map((account) => {
            const routerAccount = accountMap.get(account)
            return (
              <Badge
                key={account}
                variant={routerAccount ? "secondary" : "destructive"}
                className="max-w-full"
              >
                <span className="truncate">
                  {routerAccount?.label ?? account}
                  {!routerAccount ? ` (${missingLabel})` : ""}
                </span>
              </Badge>
            )
          })}
        </div>
      )}
    </div>
  )
}

function AccountSelect({
  value,
  accounts,
  placeholder,
  ariaLabel,
  onChange,
}: {
  value: string
  accounts: RouterAccount[]
  placeholder: string
  ariaLabel: string
  onChange: (value: string) => void
}) {
  return (
    <Select
      value={value || NONE_VALUE}
      onValueChange={(nextValue) => {
        if (nextValue !== NONE_VALUE) onChange(nextValue)
      }}
    >
      <SelectTrigger className="w-full" aria-label={ariaLabel}>
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent>
        {!value && (
          <SelectItem value={NONE_VALUE} disabled>
            {placeholder}
          </SelectItem>
        )}
        {accounts.map((account) => (
          <SelectItem key={account.id} value={account.id}>
            {account.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function BlockSelect({
  value,
  blocks,
  placeholder,
  ariaLabel,
  allowEmpty,
  onChange,
}: {
  value: string
  blocks: ModelRouterBlock[]
  placeholder: string
  ariaLabel: string
  allowEmpty?: boolean
  onChange: (value: string) => void
}) {
  const selectValue = value || NONE_VALUE
  return (
    <Select
      value={selectValue}
      onValueChange={(nextValue) =>
        onChange(nextValue === NONE_VALUE ? "" : nextValue)
      }
    >
      <SelectTrigger className="w-full" aria-label={ariaLabel}>
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent>
        {(allowEmpty || !value) && (
          <SelectItem value={NONE_VALUE} disabled={!allowEmpty}>
            {placeholder}
          </SelectItem>
        )}
        {blocks.map((block) => (
          <SelectItem key={block.id} value={block.id}>
            {block.id}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
