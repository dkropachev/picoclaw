import type { ReactNode } from "react"

import type { CollectionBulkDeleteFailure } from "@/api/collection"
import type { Badge } from "@/components/ui/badge"

export type CollectionView = "list" | "table" | "grid"

export const collectionViews: readonly CollectionView[] = [
  "list",
  "table",
  "grid",
]

export interface CollectionItemIdentity {
  title: ReactNode
  description?: ReactNode
  metadata?: ReactNode
}

export interface CollectionColumn<T> {
  id: string
  header: ReactNode
  cell: (item: T) => ReactNode
  className?: string
  headerClassName?: string
}

export interface CollectionGridFact<T> {
  id: string
  label: ReactNode
  value: (item: T) => ReactNode
}

export interface CollectionBadge<T> {
  id: string
  label: (item: T) => ReactNode | null
  variant?: React.ComponentProps<typeof Badge>["variant"]
}

export interface CollectionItemAction<T> {
  id: string
  label: string | ((item: T) => string)
  icon?: ReactNode
  destructive?: boolean
  hidden?: (item: T) => boolean
  disabled?: (item: T) => boolean
  onSelect: (item: T) => void | Promise<void>
}

export interface CollectionDefinition<T> {
  key: string
  title: string
  defaultQuery: string
  getItemID: (item: T) => string
  getItemLabel: (item: T) => string
  getItemIdentity: (item: T) => CollectionItemIdentity
  columns: readonly CollectionColumn<T>[]
  gridFacts?: readonly CollectionGridFact<T>[]
  badges?: readonly CollectionBadge<T>[]
  actions?: readonly CollectionItemAction<T>[]
  supportedViews?: readonly CollectionView[]
  defaultView?: CollectionView
}

export interface CollectionSelection<T> {
  selectedIDs: ReadonlySet<string>
  disabled?: boolean
  maximumSelected?: number
  isItemDisabled?: (item: T) => boolean
  failuresByID?: ReadonlyMap<string, CollectionBulkDeleteFailure>
  onSelectionChange: (ids: ReadonlySet<string>) => void
}
