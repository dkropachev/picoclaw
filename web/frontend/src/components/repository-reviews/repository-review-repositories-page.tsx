import {
  IconAlertTriangle,
  IconEdit,
  IconPlus,
  IconRefresh,
  IconTrash,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useMemo, useState } from "react"

import {
  type RepositoryReviewAutomation,
  createRepositoryReviewAutomation,
  deleteRepositoryReviewAutomation,
  listRepositoryReviewAutomations,
  listRepositoryReviewProfiles,
  updateRepositoryReviewAutomation,
} from "@/api/repository-reviews"
import { PageHeader } from "@/components/page-header"
import { ReviewAdvancedSection } from "@/components/repository-reviews/review-advanced-section"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

const repositoriesKey = ["repository-review-automations"] as const
const profilesKey = ["repository-review-profiles"] as const

interface RepositoryEditor {
  automation: RepositoryReviewAutomation | null
  repository: string
  profileID: string
  branch: string
}

export function RepositoryReviewRepositoriesPage() {
  const queryClient = useQueryClient()
  const [editor, setEditor] = useState<RepositoryEditor | null>(null)
  const [actionError, setActionError] = useState("")
  const repositoriesQuery = useQuery({
    queryKey: repositoriesKey,
    queryFn: ({ signal }) => listRepositoryReviewAutomations(signal),
  })
  const profilesQuery = useQuery({
    queryKey: profilesKey,
    queryFn: ({ signal }) => listRepositoryReviewProfiles(signal),
  })
  const repositories = useMemo(
    () => repositoriesQuery.data?.automations ?? [],
    [repositoriesQuery.data?.automations],
  )
  const profiles = profilesQuery.data?.profiles ?? []
  const duplicate = Boolean(
    editor &&
    repositories.some(
      (item) =>
        item.id !== editor.automation?.id &&
        item.repository.trim().toLowerCase() ===
          editor.repository.trim().toLowerCase(),
    ),
  )
  const branchError = editor ? repositoryBranchError(editor.branch) : ""

  const saveMutation = useMutation({
    mutationFn: async (value: RepositoryEditor) => {
      const input = {
        repository: value.repository.trim(),
        profile_id: value.profileID,
        branch: value.branch.trim(),
      }
      return value.automation
        ? updateRepositoryReviewAutomation(value.automation.id, {
            ...input,
            expected_version: value.automation.version,
          })
        : createRepositoryReviewAutomation(input)
    },
    onSuccess: (updated) => {
      queryClient.setQueryData<{ automations: RepositoryReviewAutomation[] }>(
        repositoriesKey,
        (current) => ({
          automations: current?.automations.some(
            (item) => item.id === updated.id,
          )
            ? current.automations.map((item) =>
                item.id === updated.id ? updated : item,
              )
            : [updated, ...(current?.automations ?? [])],
        }),
      )
      setEditor(null)
      setActionError("")
    },
    onError: (error) => {
      setActionError(errorMessage(error))
      void repositoriesQuery.refetch()
    },
  })
  const deleteMutation = useMutation({
    mutationFn: (item: RepositoryReviewAutomation) =>
      deleteRepositoryReviewAutomation(item.id, {
        expected_version: item.version,
      }),
    onSuccess: (_result, removed) => {
      queryClient.setQueryData<{ automations: RepositoryReviewAutomation[] }>(
        repositoriesKey,
        (current) => ({
          automations: (current?.automations ?? []).filter(
            (item) => item.id !== removed.id,
          ),
        }),
      )
      setActionError("")
    },
    onError: (error) => {
      setActionError(errorMessage(error))
      void repositoriesQuery.refetch()
    },
  })
  const openNew = () =>
    setEditor({
      automation: null,
      repository: "",
      profileID: profiles[0]?.id ?? "",
      branch: "",
    })

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader title="Review repositories">
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={repositoriesQuery.isFetching || profilesQuery.isFetching}
          onClick={() => {
            void repositoriesQuery.refetch()
            void profilesQuery.refetch()
          }}
        >
          <IconRefresh /> Refresh
        </Button>
        <Button
          type="button"
          size="sm"
          disabled={!profiles.length}
          onClick={openNew}
        >
          <IconPlus /> Add repository
        </Button>
      </PageHeader>
      <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-8 md:px-6">
        <div className="mx-auto max-w-5xl space-y-4">
          <p className="text-muted-foreground text-sm">
            One configuration per repository. Assign exactly one review profile;
            leave branch blank to use the repository&apos;s default branch.
          </p>
          {actionError && (
            <div
              role="alert"
              className="text-destructive flex items-center gap-2 text-sm"
            >
              <IconAlertTriangle className="size-4" /> {actionError}
            </div>
          )}
          {profilesQuery.isSuccess && profiles.length === 0 && (
            <Card size="sm" className="border-dashed">
              <CardContent className="py-8 text-center text-sm">
                Create a review profile before adding a repository.
              </CardContent>
            </Card>
          )}
          {repositoriesQuery.isPending ? (
            <Empty text="Loading repository configurations…" />
          ) : repositoriesQuery.isError ? (
            <Empty text="Repository configurations could not be loaded." />
          ) : repositories.length === 0 ? (
            <Empty text="No repository configured yet." />
          ) : (
            <div className="grid gap-4 lg:grid-cols-2">
              {repositories.map((item) => {
                const profile = profiles.find(
                  (candidate) => candidate.id === item.profile_id,
                )
                const busy =
                  saveMutation.isPending ||
                  deleteMutation.isPending ||
                  item.status === "running" ||
                  item.status === "stopping"
                return (
                  <Card key={item.id} size="sm">
                    <CardHeader>
                      <div className="flex items-start justify-between gap-3">
                        <div className="min-w-0">
                          <CardTitle className="truncate">
                            {item.repository}
                          </CardTitle>
                          <CardDescription className="mt-1">
                            {profile?.name ?? item.name ?? "Missing profile"}
                          </CardDescription>
                        </div>
                        <Badge variant="secondary">{item.status}</Badge>
                      </div>
                    </CardHeader>
                    <CardContent className="space-y-3">
                      <p className="text-muted-foreground text-sm">
                        {item.branch || item.ref
                          ? `Branch override: ${item.branch || item.ref}`
                          : "Uses default repository branch"}
                      </p>
                      <p className="text-muted-foreground text-xs">
                        Profile snapshot v
                        {item.profile_version || profile?.version || "?"}
                      </p>
                      <div className="flex gap-2 border-t pt-3">
                        <Button
                          type="button"
                          size="sm"
                          variant="outline"
                          disabled={busy}
                          onClick={() =>
                            setEditor({
                              automation: item,
                              repository: item.repository,
                              profileID: item.profile_id,
                              branch: item.branch || item.ref || "",
                            })
                          }
                        >
                          <IconEdit /> Edit
                        </Button>
                        <AlertDialog>
                          <AlertDialogTrigger asChild>
                            <Button
                              type="button"
                              size="sm"
                              variant="ghost"
                              disabled={busy}
                            >
                              <IconTrash /> Remove
                            </Button>
                          </AlertDialogTrigger>
                          <AlertDialogContent>
                            <AlertDialogHeader>
                              <AlertDialogTitle>
                                Remove {item.repository}?
                              </AlertDialogTitle>
                              <AlertDialogDescription>
                                This removes its review configuration and run
                                controls. Existing result history stays in
                                Results.
                              </AlertDialogDescription>
                            </AlertDialogHeader>
                            <AlertDialogFooter>
                              <AlertDialogCancel>Cancel</AlertDialogCancel>
                              <AlertDialogAction
                                onClick={() => deleteMutation.mutate(item)}
                              >
                                Remove repository
                              </AlertDialogAction>
                            </AlertDialogFooter>
                          </AlertDialogContent>
                        </AlertDialog>
                      </div>
                    </CardContent>
                  </Card>
                )
              })}
            </div>
          )}
        </div>
      </div>

      <Dialog
        open={editor !== null}
        onOpenChange={(open) => !open && setEditor(null)}
      >
        <DialogContent className="sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>
              {editor?.automation ? "Edit repository" : "Add repository"}
            </DialogTitle>
            <DialogDescription>
              Repository identity is unique. Review behavior comes from its
              assigned profile.
            </DialogDescription>
          </DialogHeader>
          {editor && (
            <div className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="review-repository">Repository</Label>
                <Input
                  id="review-repository"
                  value={editor.repository}
                  placeholder="owner/repository or safe Git URL"
                  onChange={(event) =>
                    setEditor({ ...editor, repository: event.target.value })
                  }
                />
                {duplicate && (
                  <p role="alert" className="text-destructive text-xs">
                    This repository already has a review configuration.
                  </p>
                )}
              </div>
              <div className="space-y-2">
                <Label htmlFor="review-profile">Assigned profile</Label>
                <select
                  id="review-profile"
                  className="border-input bg-background h-9 w-full rounded-md border px-3 text-sm"
                  value={editor.profileID}
                  onChange={(event) =>
                    setEditor({ ...editor, profileID: event.target.value })
                  }
                >
                  <option value="">Select profile</option>
                  {profiles.map((profile) => (
                    <option key={profile.id} value={profile.id}>
                      {profile.name} · {profile.reviewer_model}
                    </option>
                  ))}
                </select>
              </div>
              <ReviewAdvancedSection description="optional branch override">
                <div className="space-y-2">
                  <Label htmlFor="review-branch">Branch override</Label>
                  <Input
                    id="review-branch"
                    value={editor.branch}
                    placeholder="Blank uses the repository default branch"
                    onChange={(event) =>
                      setEditor({ ...editor, branch: event.target.value })
                    }
                  />
                  <p className="text-muted-foreground text-xs">
                    Branch names only. Repository reviews do not accept
                    arbitrary targets, URLs, tags, or commit hashes here.
                  </p>
                  {branchError && (
                    <p role="alert" className="text-destructive text-xs">
                      {branchError}
                    </p>
                  )}
                </div>
              </ReviewAdvancedSection>
              <DialogFooter>
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => setEditor(null)}
                >
                  Cancel
                </Button>
                <Button
                  type="button"
                  disabled={
                    saveMutation.isPending ||
                    !editor.repository.trim() ||
                    !editor.profileID ||
                    duplicate ||
                    Boolean(branchError)
                  }
                  onClick={() => saveMutation.mutate(editor)}
                >
                  Save repository
                </Button>
              </DialogFooter>
            </div>
          )}
        </DialogContent>
      </Dialog>
    </div>
  )
}

function Empty({ text }: { text: string }) {
  return (
    <Card size="sm" className="border-dashed">
      <CardContent className="text-muted-foreground py-10 text-center text-sm">
        {text}
      </CardContent>
    </Card>
  )
}

function errorMessage(error: unknown): string {
  return error instanceof Error
    ? error.message
    : "Repository configuration request failed."
}

function repositoryBranchError(value: string): string {
  if (!value.trim()) return ""
  const branch = value
  const lower = branch.toLowerCase()
  if (
    branch !== branch.trim() ||
    new TextEncoder().encode(branch).length > 255 ||
    [...branch].some(
      (character) =>
        /\s/u.test(character) ||
        character.charCodeAt(0) < 32 ||
        character.charCodeAt(0) === 127,
    ) ||
    lower === "head" ||
    lower === "@" ||
    lower.startsWith("refs/") ||
    lower.startsWith("tags/") ||
    lower.includes("://") ||
    (/^[0-9a-f]+$/i.test(branch) &&
      branch.length >= 7 &&
      branch.length <= 64) ||
    /[~^:?#*\\]/.test(branch) ||
    branch.includes("[") ||
    branch.includes("..") ||
    branch.includes("@{") ||
    branch.includes("//") ||
    branch.startsWith("-") ||
    branch.startsWith("/") ||
    branch.endsWith("/") ||
    branch.split("/").some((component) => {
      const componentLower = component.toLowerCase()
      return (
        !component ||
        component.startsWith(".") ||
        component.endsWith(".") ||
        componentLower.endsWith(".lock")
      )
    })
  ) {
    return "Enter a branch name, or leave blank for the repository default branch."
  }
  return ""
}
