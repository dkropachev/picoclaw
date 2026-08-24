import {
  IconAlertTriangle,
  IconFile,
  IconFileDiff,
  IconFolder,
  IconFolderUp,
  IconLoader2,
} from "@tabler/icons-react"
import { useInfiniteQuery, useQuery } from "@tanstack/react-query"
import {
  Component,
  type ReactNode,
  Suspense,
  lazy,
  useEffect,
  useMemo,
  useState,
} from "react"

import {
  type DevelopmentCodeDiff,
  getDevelopmentCodeBlob,
  getDevelopmentCodeDiff,
  getDevelopmentCodeTree,
} from "@/api/development-workspaces"
import { Button } from "@/components/ui/button"
import { useIsMobile } from "@/hooks/use-mobile"
import { useTheme } from "@/hooks/use-theme"

const MonacoReadOnlyViewer = lazy(
  () => import("@/components/development-workspaces/monaco-read-only-viewer"),
)

export function DevelopmentCodeBrowser({
  workspaceID,
  candidateRevision,
  selectedPath,
  onSelectPath,
  changedFiles,
}: {
  workspaceID: string
  candidateRevision?: string
  selectedPath?: string
  onSelectPath: (path?: string) => void
  changedFiles?: string[]
}) {
  const { theme } = useTheme()
  const isMobile = useIsMobile()
  const [directory, setDirectory] = useState(() => parentPath(selectedPath))
  const [plainText, setPlainText] = useState(false)

  useEffect(() => {
    if (selectedPath) setDirectory(parentPath(selectedPath))
  }, [selectedPath])

  const treeQuery = useInfiniteQuery({
    queryKey: [
      "development-workspace",
      workspaceID,
      "code-tree",
      candidateRevision,
      directory,
    ],
    queryFn: ({ signal, pageParam }) =>
      getDevelopmentCodeTree(
        workspaceID,
        {
          revision: candidateRevision ?? "",
          path: directory,
          ...(pageParam ? { cursor: pageParam } : {}),
        },
        signal,
      ),
    initialPageParam: "",
    getNextPageParam: (lastPage) => lastPage.next_cursor,
    enabled: Boolean(candidateRevision) && changedFiles == null,
  })
  const candidateBlobQuery = useQuery({
    queryKey: [
      "development-workspace",
      workspaceID,
      "code-blob",
      candidateRevision,
      selectedPath,
    ],
    queryFn: ({ signal }) =>
      getDevelopmentCodeBlob(
        workspaceID,
        { revision: candidateRevision ?? "", path: selectedPath ?? "" },
        signal,
      ),
    enabled: Boolean(candidateRevision && selectedPath),
    retry: false,
  })
  const baseBlobQuery = useQuery({
    queryKey: [
      "development-workspace",
      workspaceID,
      "code-blob",
      candidateRevision,
      "base",
      selectedPath,
    ],
    queryFn: ({ signal }) =>
      getDevelopmentCodeBlob(
        workspaceID,
        {
          revision: "base",
          candidate_revision: candidateRevision,
          path: selectedPath ?? "",
        },
        signal,
      ),
    enabled: Boolean(candidateRevision && selectedPath),
    retry: false,
  })
  const diffQuery = useQuery({
    queryKey: [
      "development-workspace",
      workspaceID,
      "code-diff",
      candidateRevision,
      selectedPath,
    ],
    queryFn: ({ signal }) =>
      getDevelopmentCodeDiff(
        workspaceID,
        { revision: candidateRevision ?? "", path: selectedPath ?? "" },
        signal,
      ),
    enabled: Boolean(candidateRevision && selectedPath),
    retry: false,
  })

  const paths = useMemo(
    () =>
      changedFiles?.map((path) => ({
        name: path.split("/").at(-1) ?? path,
        path,
        type: "file" as const,
      })) ??
      treeQuery.data?.pages.flatMap((page) => page.entries) ??
      [],
    [changedFiles, treeQuery.data?.pages],
  )
  const candidateBlob =
    candidateBlobQuery.data?.revision === candidateRevision
      ? candidateBlobQuery.data
      : undefined
  const baseBlob =
    baseBlobQuery.data?.revision === candidateRevision
      ? baseBlobQuery.data
      : undefined
  const trustedDiff =
    diffQuery.data?.candidate_revision === candidateRevision
      ? diffQuery.data
      : undefined
  const viewer =
    trustedDiff?.original != null && trustedDiff.modified != null
      ? {
          original: trustedDiff.original,
          modified: trustedDiff.modified,
          language: trustedDiff.language ?? languageForPath(trustedDiff.path),
        }
      : candidateBlob && baseBlob
        ? {
            original: baseBlob.content,
            modified: candidateBlob.content,
            language:
              candidateBlob.language ?? languageForPath(candidateBlob.path),
          }
        : candidateBlob
          ? {
              modified: candidateBlob.content,
              language:
                candidateBlob.language ?? languageForPath(candidateBlob.path),
            }
          : undefined
  const unifiedDiff = trustedDiff
    ? unifiedText(trustedDiff)
    : candidateBlob && baseBlob
      ? fullFileDiff(
          selectedPath ?? candidateBlob.path,
          baseBlob.content,
          candidateBlob.content,
        )
      : (candidateBlob?.content ?? "")

  if (!candidateRevision) {
    return (
      <div className="border-border text-muted-foreground rounded-lg border border-dashed p-8 text-center text-sm">
        Code browsing becomes available when the workspace has a candidate
        revision.
      </div>
    )
  }

  return (
    <section
      aria-label="Repository code browser"
      className="border-border bg-card grid min-h-0 overflow-hidden rounded-lg border md:grid-cols-[16rem_minmax(0,1fr)]"
      data-testid="development-code-browser"
    >
      <div className="border-border flex min-h-48 flex-col border-b md:min-h-[32rem] md:border-r md:border-b-0">
        <div className="border-border flex h-10 items-center gap-1 border-b px-2">
          {changedFiles == null && directory && (
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label="Open parent directory"
              title="Open parent directory"
              onClick={() => {
                setDirectory(parentPath(directory))
                onSelectPath()
              }}
            >
              <IconFolderUp />
            </Button>
          )}
          <span className="min-w-0 truncate text-xs font-medium">
            {changedFiles == null ? directory || "/" : "Changed files"}
          </span>
        </div>
        <div className="min-h-0 flex-1 overflow-auto p-1">
          {treeQuery.isPending && changedFiles == null ? (
            <p className="text-muted-foreground flex items-center gap-2 p-3 text-xs">
              <IconLoader2 className="size-3.5 animate-spin" /> Loading files…
            </p>
          ) : treeQuery.isError && changedFiles == null ? (
            <p role="alert" className="text-destructive flex gap-2 p-3 text-xs">
              <IconAlertTriangle className="size-3.5 shrink-0" /> Files could
              not be loaded.
            </p>
          ) : paths.length === 0 ? (
            <p className="text-muted-foreground p-3 text-xs">
              {changedFiles == null
                ? "Directory is empty."
                : "No changed files yet."}
            </p>
          ) : (
            paths.map((entry) => {
              const EntryIcon =
                entry.type === "directory" ? IconFolder : IconFile
              return (
                <button
                  key={entry.path}
                  type="button"
                  aria-current={
                    selectedPath === entry.path ? "true" : undefined
                  }
                  onClick={() => {
                    if (entry.type === "directory") {
                      setDirectory(entry.path)
                      onSelectPath()
                    } else {
                      onSelectPath(entry.path)
                    }
                  }}
                  className="hover:bg-muted aria-[current=true]:bg-muted focus-visible:ring-ring flex w-full min-w-0 items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs focus-visible:ring-2 focus-visible:outline-none"
                >
                  <EntryIcon className="text-muted-foreground size-3.5 shrink-0" />
                  <span className="truncate">
                    {changedFiles == null ? entry.name : entry.path}
                  </span>
                </button>
              )
            })
          )}
          {changedFiles == null && treeQuery.hasNextPage && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="mt-1 w-full"
              disabled={treeQuery.isFetchingNextPage}
              onClick={() => void treeQuery.fetchNextPage()}
            >
              {treeQuery.isFetchingNextPage ? "Loading…" : "Load more files"}
            </Button>
          )}
        </div>
      </div>

      <div className="flex min-h-[32rem] min-w-0 flex-col">
        <div className="border-border flex min-h-10 flex-wrap items-center justify-between gap-2 border-b px-2 py-1">
          <span className="flex min-w-0 items-center gap-2 text-xs font-medium">
            {selectedPath ? (
              <>
                <IconFileDiff className="text-muted-foreground size-3.5 shrink-0" />
                <span className="truncate">{selectedPath}</span>
              </>
            ) : (
              "Select a file"
            )}
          </span>
          {selectedPath && (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              onClick={() => setPlainText((current) => !current)}
            >
              {plainText ? "Use Monaco" : "Accessible text view"}
            </Button>
          )}
        </div>
        <div className="min-h-0 flex-1 overflow-auto">
          {!selectedPath ? (
            <div className="text-muted-foreground flex min-h-80 items-center justify-center p-6 text-sm">
              Choose a file to inspect the exact candidate revision.
            </div>
          ) : candidateBlobQuery.isPending ||
            baseBlobQuery.isPending ||
            diffQuery.isPending ? (
            <div className="text-muted-foreground flex min-h-80 items-center justify-center gap-2 text-sm">
              <IconLoader2 className="size-4 animate-spin" /> Loading file…
            </div>
          ) : !viewer && trustedDiff?.unified_diff ? (
            <PlainCode path={selectedPath} content={unifiedDiff} />
          ) : !viewer ? (
            <div
              role="alert"
              className="text-destructive flex min-h-80 items-center justify-center gap-2 p-6 text-sm"
            >
              <IconAlertTriangle className="size-4" /> File content could not be
              loaded.
            </div>
          ) : plainText ? (
            <PlainCode path={selectedPath} content={unifiedDiff} />
          ) : (
            <MonacoBoundary
              key={`${candidateRevision}:${selectedPath}`}
              fallback={<PlainCode path={selectedPath} content={unifiedDiff} />}
            >
              <Suspense
                fallback={
                  <div className="text-muted-foreground flex min-h-80 items-center justify-center gap-2 text-sm">
                    <IconLoader2 className="size-4 animate-spin" /> Loading
                    editor…
                  </div>
                }
              >
                <MonacoReadOnlyViewer
                  path={selectedPath}
                  original={viewer.original}
                  modified={viewer.modified}
                  language={viewer.language}
                  theme={theme}
                  inline={isMobile}
                />
              </Suspense>
            </MonacoBoundary>
          )}
        </div>
      </div>
    </section>
  )
}

function PlainCode({ path, content }: { path: string; content: string }) {
  return (
    <textarea
      readOnly
      spellCheck={false}
      wrap="off"
      aria-label={`Read-only text view for ${path}`}
      value={content}
      className="bg-background text-foreground min-h-80 w-full resize-none overflow-auto border-0 p-4 font-mono text-xs leading-relaxed outline-none"
    />
  )
}

class MonacoBoundary extends Component<
  { children: ReactNode; fallback: ReactNode },
  { failed: boolean }
> {
  state = { failed: false }

  static getDerivedStateFromError() {
    return { failed: true }
  }

  componentDidCatch() {
    // Text fallback is the user-visible recovery path.
  }

  render() {
    return this.state.failed ? this.props.fallback : this.props.children
  }
}

function parentPath(path?: string): string {
  if (!path) return ""
  const index = path.lastIndexOf("/")
  return index < 0 ? "" : path.slice(0, index)
}

function languageForPath(path: string): string {
  const extension = path.split(".").at(-1)?.toLowerCase()
  return (
    {
      c: "c",
      cc: "cpp",
      cpp: "cpp",
      css: "css",
      go: "go",
      html: "html",
      java: "java",
      js: "javascript",
      json: "json",
      jsx: "javascript",
      md: "markdown",
      py: "python",
      rs: "rust",
      sh: "shell",
      ts: "typescript",
      tsx: "typescript",
      yaml: "yaml",
      yml: "yaml",
    }[extension ?? ""] ?? "plaintext"
  )
}

function unifiedText(diff: DevelopmentCodeDiff): string {
  if (diff.unified_diff) return diff.unified_diff
  return fullFileDiff(diff.path, diff.original ?? "", diff.modified ?? "")
}

function fullFileDiff(
  path: string,
  original: string,
  modified: string,
): string {
  return [
    `--- ${path} (base)`,
    `+++ ${path} (candidate)`,
    ...original.split("\n").map((line) => `- ${line}`),
    ...modified.split("\n").map((line) => `+ ${line}`),
  ].join("\n")
}
