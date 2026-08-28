import {
  IconAlertTriangle,
  IconBrandGithub,
  IconFileText,
  IconGitPullRequest,
  IconLoader2,
  IconSparkles,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { type FormEvent, type ReactNode, useMemo, useState } from "react"

import {
  type CreateDevelopmentWorkspaceRequest,
  DevelopmentWorkspaceAPIError,
  createDevelopmentRequestID,
  createDevelopmentWorkspace,
  listDevelopmentRepositories,
} from "@/api/development-workspaces"
import { CollectionDetailShell } from "@/components/collection"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import { cn } from "@/lib/utils"

type IntakeIntent = "implement_feature" | "pickup_pr"
type FeatureSource = "issue" | "brief"

export function DevelopmentIntakePage({
  onBack,
  onCreated,
  initialIssueURL,
}: {
  onBack: () => void
  onCreated: (workspaceID: string) => void
  initialIssueURL?: string
}) {
  const queryClient = useQueryClient()
  const [intent, setIntent] = useState<IntakeIntent | undefined>(() =>
    initialIssueURL ? "implement_feature" : undefined,
  )
  const [featureSource, setFeatureSource] = useState<FeatureSource>("issue")
  const [issueURL, setIssueURL] = useState(initialIssueURL ?? "")
  const [pullRequestURL, setPullRequestURL] = useState("")
  const [repositoryIdentity, setRepositoryIdentity] = useState("")
  const [brief, setBrief] = useState("")
  const [error, setError] = useState("")
  const repositoriesQuery = useQuery({
    queryKey: ["development-workspaces", "repositories"],
    queryFn: ({ signal }) => listDevelopmentRepositories(signal),
    enabled: intent === "implement_feature" && featureSource === "brief",
    staleTime: 30_000,
  })
  const repositories = useMemo(
    () =>
      (repositoriesQuery.data?.repositories ?? []).filter(
        (repository) => repository.can_implement,
      ),
    [repositoriesQuery.data?.repositories],
  )
  const createMutation = useMutation({
    mutationFn: (input: CreateDevelopmentWorkspaceRequest) =>
      createDevelopmentWorkspace(input),
    onSuccess: async (workspace) => {
      await queryClient.invalidateQueries({
        queryKey: ["development-workspaces"],
      })
      onCreated(workspace.id)
    },
    onError: (cause) => {
      setError(
        cause instanceof DevelopmentWorkspaceAPIError
          ? cause.message
          : "Development work could not be started.",
      )
    },
  })

  const chooseIntent = (nextIntent: IntakeIntent) => {
    setIntent(nextIntent)
    setError("")
    if (nextIntent === "implement_feature") setPullRequestURL("")
    else {
      setIssueURL("")
      setBrief("")
      setRepositoryIdentity("")
    }
  }

  const chooseFeatureSource = (source: FeatureSource) => {
    setFeatureSource(source)
    setError("")
    if (source === "issue") {
      setBrief("")
      setRepositoryIdentity("")
    } else {
      setIssueURL("")
    }
  }

  const submit = (event: FormEvent) => {
    event.preventDefault()
    setError("")
    const requestID = createDevelopmentRequestID()
    if (intent === "pickup_pr") {
      const value = pullRequestURL.trim()
      if (!value) return
      createMutation.mutate({
        intent: "pickup_pr",
        pull_request_url: value,
        request_id: requestID,
      })
      return
    }
    if (intent !== "implement_feature") return
    if (featureSource === "issue") {
      const value = issueURL.trim()
      if (!value) return
      createMutation.mutate({
        intent: "implement_feature",
        source: { kind: "issue", issue_url: value },
        request_id: requestID,
      })
      return
    }
    const content = brief.trim()
    if (!repositoryIdentity || !content) return
    createMutation.mutate({
      intent: "implement_feature",
      source: {
        kind: "brief",
        repository_identity: repositoryIdentity,
        content,
      },
      request_id: requestID,
    })
  }

  const canSubmit =
    intent === "pickup_pr"
      ? pullRequestURL.trim().length > 0
      : intent === "implement_feature" && featureSource === "issue"
        ? issueURL.trim().length > 0
        : intent === "implement_feature" &&
          repositoryIdentity.length > 0 &&
          brief.trim().length > 0

  return (
    <div className="h-full min-h-0" data-testid="development-intake">
      <CollectionDetailShell
        title="New development work"
        onBack={onBack}
        backLabel="All development workspaces"
        contentClassName="max-w-3xl"
      >
        <form onSubmit={submit} className="flex w-full flex-col gap-4 pb-4">
          <fieldset className="space-y-3">
            <legend className="text-sm font-medium">
              What do you want to do?
            </legend>
            <div className="grid gap-3 sm:grid-cols-2">
              <IntentButton
                selected={intent === "implement_feature"}
                icon={<IconSparkles />}
                title="Implement feature"
                description="Start from one issue or a written brief, then create a draft PR."
                onClick={() => chooseIntent("implement_feature")}
              />
              <IntentButton
                selected={intent === "pickup_pr"}
                icon={<IconGitPullRequest />}
                title="Pick up PR"
                description="Continue implementation and validation on one existing PR."
                onClick={() => chooseIntent("pickup_pr")}
              />
            </div>
          </fieldset>

          {intent === "implement_feature" && (
            <Card size="sm" data-testid="implement-feature-form">
              <CardHeader>
                <CardTitle>Implement feature</CardTitle>
                <CardDescription>
                  Choose exactly one source for the feature charter.
                </CardDescription>
              </CardHeader>
              <CardContent className="space-y-4">
                <fieldset className="space-y-2">
                  <legend className="text-sm font-medium">
                    Feature source
                  </legend>
                  <div className="flex flex-wrap gap-2">
                    <Button
                      type="button"
                      variant={
                        featureSource === "issue" ? "default" : "outline"
                      }
                      aria-pressed={featureSource === "issue"}
                      onClick={() => chooseFeatureSource("issue")}
                    >
                      <IconBrandGithub />
                      GitHub issue
                    </Button>
                    <Button
                      type="button"
                      variant={
                        featureSource === "brief" ? "default" : "outline"
                      }
                      aria-pressed={featureSource === "brief"}
                      onClick={() => chooseFeatureSource("brief")}
                    >
                      <IconFileText />
                      Write brief
                    </Button>
                  </div>
                </fieldset>

                {featureSource === "issue" ? (
                  <div className="space-y-2" data-testid="issue-source-fields">
                    <Label htmlFor="development-issue-url">
                      GitHub issue URL
                    </Label>
                    <Input
                      id="development-issue-url"
                      type="url"
                      inputMode="url"
                      autoComplete="off"
                      required
                      value={issueURL}
                      onChange={(event) => setIssueURL(event.target.value)}
                      placeholder="https://github.com/owner/repository/issues/123"
                    />
                  </div>
                ) : (
                  <div className="space-y-4" data-testid="brief-source-fields">
                    <div className="space-y-2">
                      <Label htmlFor="development-repository">Repository</Label>
                      <Select
                        value={repositoryIdentity}
                        onValueChange={setRepositoryIdentity}
                        disabled={
                          repositoriesQuery.isPending ||
                          repositories.length === 0
                        }
                      >
                        <SelectTrigger
                          id="development-repository"
                          className="w-full"
                        >
                          <SelectValue placeholder="Select configured repository" />
                        </SelectTrigger>
                        <SelectContent>
                          {repositories.map((repository) => (
                            <SelectItem
                              key={repository.identity}
                              value={repository.identity}
                            >
                              {repository.name} · {repository.default_branch}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                      {repositoriesQuery.isError && (
                        <p role="alert" className="text-destructive text-sm">
                          Configured repositories could not be loaded.
                        </p>
                      )}
                      {!repositoriesQuery.isPending &&
                        !repositoriesQuery.isError &&
                        repositories.length === 0 && (
                          <p className="text-muted-foreground text-sm">
                            No configured repository can run feature
                            implementation.
                          </p>
                        )}
                    </div>
                    <div className="space-y-2">
                      <Label htmlFor="development-brief">Feature brief</Label>
                      <Textarea
                        id="development-brief"
                        required
                        rows={8}
                        value={brief}
                        onChange={(event) => setBrief(event.target.value)}
                        placeholder="Describe the goal, user outcome, and acceptance criteria."
                      />
                    </div>
                  </div>
                )}
              </CardContent>
            </Card>
          )}

          {intent === "pickup_pr" && (
            <Card size="sm" data-testid="pickup-pr-form">
              <CardHeader>
                <CardTitle>Pick up PR</CardTitle>
                <CardDescription>
                  Review and continue one existing pull request.
                </CardDescription>
              </CardHeader>
              <CardContent className="space-y-2">
                <Label htmlFor="development-pr-url">
                  GitHub pull request URL
                </Label>
                <Input
                  id="development-pr-url"
                  type="url"
                  inputMode="url"
                  autoComplete="off"
                  required
                  value={pullRequestURL}
                  onChange={(event) => setPullRequestURL(event.target.value)}
                  placeholder="https://github.com/owner/repository/pull/123"
                />
              </CardContent>
            </Card>
          )}

          {error && (
            <p
              role="alert"
              className="border-destructive/40 bg-destructive/5 text-destructive flex items-center gap-2 rounded-lg border p-3 text-sm"
            >
              <IconAlertTriangle className="size-4 shrink-0" />
              {error}
            </p>
          )}

          {intent && (
            <div className="flex justify-end">
              <Button
                type="submit"
                disabled={!canSubmit || createMutation.isPending}
              >
                {createMutation.isPending ? (
                  <IconLoader2 className="animate-spin" />
                ) : intent === "pickup_pr" ? (
                  <IconGitPullRequest />
                ) : (
                  <IconSparkles />
                )}
                {intent === "pickup_pr" ? "Pick up PR" : "Start implementation"}
              </Button>
            </div>
          )}
        </form>
      </CollectionDetailShell>
    </div>
  )
}

function IntentButton({
  selected,
  icon,
  title,
  description,
  onClick,
}: {
  selected: boolean
  icon: ReactNode
  title: string
  description: string
  onClick: () => void
}) {
  return (
    <button
      type="button"
      aria-pressed={selected}
      onClick={onClick}
      className={cn(
        "bg-card focus-visible:ring-ring flex min-w-0 gap-3 rounded-lg border p-4 text-left transition-colors focus-visible:ring-2 focus-visible:outline-none",
        selected
          ? "border-primary bg-primary/5"
          : "border-border hover:bg-muted/50",
      )}
    >
      <span className="text-primary mt-0.5 [&>svg]:size-5">{icon}</span>
      <span className="min-w-0">
        <span className="block font-medium">{title}</span>
        <span className="text-muted-foreground mt-1 block text-sm">
          {description}
        </span>
      </span>
    </button>
  )
}
