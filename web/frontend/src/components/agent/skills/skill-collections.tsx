import { IconPlus, IconTrash } from "@tabler/icons-react"
import { useInfiniteQuery, useMutation, useQuery } from "@tanstack/react-query"
import { useMemo, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { CollectionAPIError } from "@/api/collection"
import {
  type SkillSupportItem,
  bulkDeleteSkills,
  deleteSkill,
  getSkill,
  listSkills,
} from "@/api/skills"
import {
  type CollectionDefinition,
  CollectionDetailShell,
  type StandardCollectionPageSearch,
} from "@/components/collection"
import { StandardCollectionPage } from "@/components/collection/standard-collection-page"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"

import {
  administrativeCollectionViews,
  normalizeSkillsCollectionSearch,
  skillsDefaultQuery,
} from "../skill-tool-collection-route-state"
import { getOriginLabel, getSkillOriginKind } from "./origin-utils"
import { SkillDetailContent } from "./skill-detail-content"

interface SkillsCollectionNavigation {
  onAdd: () => void
  onOpen: (skill: SkillSupportItem) => void
}

export function SkillsCollectionPage({
  search,
  onSearchChange,
  ...navigation
}: SkillsCollectionNavigation & {
  search: StandardCollectionPageSearch
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
}) {
  const { t } = useTranslation()
  const [deleteTarget, setDeleteTarget] = useState<SkillSupportItem | null>(
    null,
  )
  const activeQuery = normalizeSkillsCollectionSearch(search).q
  const query = useInfiniteQuery({
    queryKey: ["skills", "collection", activeQuery],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      listSkills(
        { query: activeQuery, cursor: pageParam || undefined, limit: 50 },
        signal,
      ),
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    retry: false,
  })
  const items = useMemo(
    () => [
      ...new Map(
        (query.data?.pages.flatMap((page) => page.skills) ?? []).map(
          (skill) => [skill.id, skill],
        ),
      ).values(),
    ],
    [query.data?.pages],
  )
  const first = query.data?.pages[0]
  const deleteMutation = useMutation({
    mutationFn: (skill: SkillSupportItem) => deleteSkill(skill.id),
    onSuccess: async (_, skill) => {
      setDeleteTarget(null)
      toast.success(`${skill.name} was removed.`)
      await query.refetch()
    },
    onError: (error) => {
      toast.error(
        error instanceof Error ? error.message : "The skill was not removed.",
      )
    },
  })

  const definition = useMemo<CollectionDefinition<SkillSupportItem>>(
    () => ({
      key: "skills",
      title: t("navigation.skills", "Skills"),
      defaultQuery: skillsDefaultQuery,
      supportedViews: administrativeCollectionViews,
      defaultView: "list",
      getItemID: (skill) => skill.id,
      getItemLabel: (skill) => skill.name,
      getItemIdentity: (skill) => ({
        title: skill.name,
        description:
          skill.description || t("pages.agent.skills.no_description"),
        metadata: `Source: ${skill.source}`,
      }),
      columns: [
        { id: "source", header: "Source", cell: (skill) => skill.source },
        {
          id: "origin",
          header: "Origin",
          cell: (skill) => getOriginLabel(getSkillOriginKind(skill), t),
        },
        {
          id: "registry",
          header: "Registry",
          cell: (skill) => skill.registry || skill.registry_name || "—",
        },
        {
          id: "version",
          header: "Version",
          cell: (skill) => skill.version || skill.installed_version || "—",
        },
        {
          id: "installed",
          header: "Installed",
          cell: (skill) => formatInstalledAt(skill.installed_at),
        },
      ],
      gridFacts: [
        { id: "source", label: "Source", value: (skill) => skill.source },
        {
          id: "origin",
          label: "Origin",
          value: (skill) => getOriginLabel(getSkillOriginKind(skill), t),
        },
        {
          id: "registry",
          label: "Registry",
          value: (skill) => skill.registry || skill.registry_name || "—",
        },
        {
          id: "version",
          label: "Version",
          value: (skill) => skill.version || skill.installed_version || "—",
        },
      ],
      badges: [
        {
          id: "origin",
          label: (skill) => getOriginLabel(getSkillOriginKind(skill), t),
          variant: "outline",
        },
      ],
      actions: [
        {
          id: "remove",
          label: "Remove skill",
          icon: <IconTrash />,
          destructive: true,
          hidden: (skill) => !isSkillRemovable(skill),
          onSelect: setDeleteTarget,
        },
      ],
    }),
    [t],
  )

  return (
    <>
      <StandardCollectionPage
        definition={definition}
        search={search}
        onSearchChange={onSearchChange}
        items={items}
        total={first?.total}
        schema={first?.query_schema}
        canonicalQuery={first?.canonical_query}
        loading={query.isLoading}
        fetching={query.isFetching}
        error={query.error}
        onRefresh={query.refetch}
        hasNextPage={query.hasNextPage}
        loadingMore={query.isFetchingNextPage}
        onLoadMore={query.fetchNextPage}
        onOpenItem={navigation.onOpen}
        addAction={
          <Button type="button" size="sm" onClick={navigation.onAdd}>
            <IconPlus /> Import skill
          </Button>
        }
        onBulkDelete={bulkDeleteSkills}
        isItemSelectable={isSkillRemovable}
        afterBulkDelete={() => query.refetch()}
        bulkDeleteConfirmation={{
          title: (count) =>
            `Remove ${count} selected skill${count === 1 ? "" : "s"}?`,
          description:
            "Only removable workspace skills will be deleted. Built-in, global, and other read-only origins remain selected with their blocker.",
          actionLabel: "Remove selected",
        }}
        emptyTitle="No installed skills"
        emptyDescription="Import a workspace skill or browse the skill marketplace."
      />
      <SkillDeleteDialog
        target={deleteTarget}
        pending={deleteMutation.isPending}
        onOpenChange={(open) => {
          if (!open && !deleteMutation.isPending) setDeleteTarget(null)
        }}
        onConfirm={() => {
          if (deleteTarget) deleteMutation.mutate(deleteTarget)
        }}
      />
    </>
  )
}

export function SkillDetailPage({
  skillID,
  onBack,
}: {
  skillID: string
  onBack: () => void
}) {
  const [confirmOpen, setConfirmOpen] = useState(false)
  const query = useQuery({
    queryKey: ["skills", "detail", skillID],
    queryFn: ({ signal }) => getSkill(skillID, signal),
    retry: false,
  })
  const mutation = useMutation({
    mutationFn: () => deleteSkill(skillID),
    onSuccess: () => {
      toast.success(`${query.data?.name || "Skill"} was removed.`)
      onBack()
    },
    onError: (error) => {
      toast.error(
        error instanceof Error ? error.message : "The skill was not removed.",
      )
    },
  })
  const notFound =
    query.error instanceof CollectionAPIError && query.error.status === 404

  return (
    <>
      <CollectionDetailShell
        title={query.data?.name || "Skill details"}
        identity={
          query.data ? (
            <span className="text-muted-foreground hidden truncate font-mono text-xs sm:inline">
              {query.data.id}
            </span>
          ) : undefined
        }
        status={
          query.data ? (
            <Badge variant="outline">
              {query.data.origin || query.data.origin_kind}
            </Badge>
          ) : undefined
        }
        actions={
          query.data && isSkillRemovable(query.data) ? (
            <Button
              type="button"
              variant="destructive"
              aria-label="Remove skill"
              title="Remove skill"
              onClick={() => setConfirmOpen(true)}
            >
              <IconTrash />{" "}
              <span className="hidden sm:inline">Remove skill</span>
            </Button>
          ) : undefined
        }
        loading={query.isLoading}
        error={notFound ? undefined : query.error?.message}
        notFound={notFound}
        onRetry={() => void query.refetch()}
        onBack={onBack}
      >
        {query.data && <SkillDetailContent skill={query.data} />}
      </CollectionDetailShell>
      <SkillDeleteDialog
        target={query.data ?? null}
        open={confirmOpen}
        pending={mutation.isPending}
        onOpenChange={(open) => {
          if (!mutation.isPending) setConfirmOpen(open)
        }}
        onConfirm={() => mutation.mutate()}
      />
    </>
  )
}

function isSkillRemovable(skill: SkillSupportItem): boolean {
  return skill.removable === true
}

function SkillDeleteDialog({
  target,
  open = target != null,
  pending,
  onOpenChange,
  onConfirm,
}: {
  target: SkillSupportItem | null
  open?: boolean
  pending: boolean
  onOpenChange: (open: boolean) => void
  onConfirm: () => void
}) {
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent size="sm">
        <AlertDialogHeader>
          <AlertDialogTitle>Remove skill?</AlertDialogTitle>
          <AlertDialogDescription>
            {target?.name || "This skill"} will be removed from the workspace.
            This cannot be undone.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={pending || !target}
            onClick={(event) => {
              event.preventDefault()
              onConfirm()
            }}
          >
            <IconTrash /> {pending ? "Removing…" : "Remove skill"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

function formatInstalledAt(value?: number): string {
  if (!value) return "—"
  const date = new Date(value < 10_000_000_000 ? value * 1000 : value)
  return Number.isNaN(date.getTime()) ? "—" : date.toLocaleDateString()
}
