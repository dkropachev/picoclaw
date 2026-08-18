import { IconChevronRight } from "@tabler/icons-react"
import {
  IconBellRinging,
  IconChevronsDown,
  IconChevronsUp,
  IconDatabase,
  IconGitBranch,
  IconGitPullRequest,
  IconKey,
  IconListDetails,
  IconMessageCircle,
  IconMessages,
  IconPlugConnected,
  IconRobot,
  IconRoute,
  IconSearch,
  IconSettings,
  IconSparkles,
  IconTools,
} from "@tabler/icons-react"
import { type HistoryState, Link, useRouterState } from "@tanstack/react-router"
import * as React from "react"
import { useTranslation } from "react-i18next"

import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
  useSidebar,
} from "@/components/ui/sidebar"
import { useSidebarChannels } from "@/hooks/use-sidebar-channels"
import { asPRNavigationState } from "@/routes/-pr-navigation"

interface NavItem {
  title: string
  url: string
  icon: React.ComponentType<{ className?: string }>
  translateTitle?: boolean
  tourId?: string
  pullRequestView?:
    | "work"
    | "workflow-configurations"
    | "repository-assignments"
    | "settings"
  search?: Record<string, string>
}

interface NavSection {
  label: string
  items: NavItem[]
  isChannelsSection?: boolean
}

const chatNavItem: NavItem = {
  title: "navigation.chat",
  url: "/",
  icon: IconMessageCircle,
  translateTitle: true,
}

const threadsNavItem: NavItem = {
  title: "navigation.threads",
  url: "/threads/search",
  icon: IconMessages,
  translateTitle: true,
}

const logsNavItem: NavItem = {
  title: "navigation.logs",
  url: "/logs",
  icon: IconListDetails,
  translateTitle: true,
}

const eventsNavItem: NavItem = {
  title: "navigation.events",
  url: "/events",
  icon: IconBellRinging,
  translateTitle: true,
}

const pullRequestsWorkNavItem: NavItem = {
  title: "navigation.pull_requests_work",
  url: "/pull-requests",
  icon: IconGitPullRequest,
  translateTitle: true,
  pullRequestView: "work",
}

const pullRequestsWorkflowConfigurationsNavItem: NavItem = {
  title: "navigation.pull_requests_workflow_configurations",
  url: "/pull-requests/workflow-configurations",
  icon: IconSettings,
  translateTitle: true,
  pullRequestView: "workflow-configurations",
}

const pullRequestsRepositoryAssignmentsNavItem: NavItem = {
  title: "navigation.pull_requests_repository_assignments",
  url: "/pull-requests/repository-assignments",
  icon: IconGitBranch,
  translateTitle: true,
  pullRequestView: "repository-assignments",
}

const pullRequestsLifecycleSettingsNavItem: NavItem = {
  title: "navigation.pull_requests_lifecycle_settings",
  url: "/pull-requests/settings",
  icon: IconRoute,
  translateTitle: true,
  pullRequestView: "settings",
  search: { tab: "nudging" },
}

const configNavItem: NavItem = {
  title: "navigation.config",
  url: "/config",
  icon: IconSettings,
  translateTitle: true,
}

const accountsNavItem: NavItem = {
  title: "navigation.accounts",
  url: "/accounts",
  icon: IconKey,
  translateTitle: true,
}

const modelsNavItem: NavItem = {
  title: "navigation.models",
  url: "/models",
  icon: IconDatabase,
  translateTitle: true,
}

export function AppSidebar({ ...props }: React.ComponentProps<typeof Sidebar>) {
  const routerState = useRouterState()
  const { i18n, t } = useTranslation()
  const { isMobile, setOpenMobile } = useSidebar()
  const currentPath = routerState.location.pathname
  const currentSearch = routerState.location.search as
    | Record<string, unknown>
    | undefined
  const originWorkspace =
    typeof currentSearch?.from === "string" &&
    /^prw_[0-9a-f]{32}$/.test(currentSearch.from)
      ? currentSearch.from
      : undefined
  const pullRequestDestination =
    currentPath === "/pull-requests" ||
    currentPath.startsWith("/pull-requests/")
      ? currentPath
      : null
  const [servicesOpen, setServicesOpen] = useAutoRevealCollapsible(
    pullRequestDestination,
    currentPath.startsWith("/agent/") || pullRequestDestination != null,
  )
  const [pullRequestsOpen, setPullRequestsOpen] = useAutoRevealCollapsible(
    pullRequestDestination,
    pullRequestDestination != null,
  )
  const {
    channelItems,
    hasMoreChannels,
    showAllChannels,
    toggleShowAllChannels,
  } = useSidebarChannels({
    language: (i18n.resolvedLanguage ?? i18n.language ?? "").toLowerCase(),
    t,
  })

  const handleNavItemClick = React.useCallback(() => {
    if (isMobile) {
      setOpenMobile(false)
    }
  }, [isMobile, setOpenMobile])

  const serviceSections: NavSection[] = React.useMemo(() => {
    return [
      {
        label: "navigation.channels_group",
        items: channelItems.map((item) => ({
          title: item.title,
          url: item.url,
          icon: item.icon,
          translateTitle: false,
        })),
        isChannelsSection: true,
      },
      {
        label: "navigation.agent_group",
        items: [
          {
            title: "navigation.agents",
            url: "/agent/agents",
            icon: IconRobot,
            translateTitle: true,
          },
          {
            title: "navigation.hub",
            url: "/agent/hub",
            icon: IconSearch,
            translateTitle: true,
          },
          {
            title: "navigation.skills",
            url: "/agent/skills",
            icon: IconSparkles,
            translateTitle: true,
          },
          {
            title: "navigation.tools",
            url: "/agent/tools",
            icon: IconTools,
            translateTitle: true,
          },
          {
            title: "navigation.mcp",
            url: "/agent/mcp",
            icon: IconPlugConnected,
            translateTitle: true,
          },
          {
            title: "navigation.workflows",
            url: "/agent/workflows",
            icon: IconRoute,
            translateTitle: true,
          },
          {
            title: "navigation.git_workspaces",
            url: "/agent/git-workspaces",
            icon: IconGitBranch,
            translateTitle: true,
          },
        ],
      },
      {
        label: "navigation.pull_requests",
        items: [
          pullRequestsWorkNavItem,
          pullRequestsWorkflowConfigurationsNavItem,
          pullRequestsRepositoryAssignmentsNavItem,
          pullRequestsLifecycleSettingsNavItem,
        ],
      },
    ]
  }, [channelItems])

  const isNavItemActive = (item: NavItem) => {
    if (item.pullRequestView === "work") {
      return (
        currentPath === "/pull-requests" ||
        currentPath.startsWith("/pull-requests/prw_")
      )
    }
    if (item.pullRequestView === "workflow-configurations") {
      return (
        currentPath === "/pull-requests/workflow-configurations" ||
        currentPath.startsWith("/pull-requests/workflow-configurations/")
      )
    }
    if (item.pullRequestView === "repository-assignments") {
      return currentPath === "/pull-requests/repository-assignments"
    }
    if (item.pullRequestView === "settings") {
      return (
        currentPath === "/pull-requests/settings" ||
        currentPath.startsWith("/pull-requests/settings/")
      )
    }
    const pathActive =
      currentPath === item.url ||
      (item.url !== "/" && currentPath.startsWith(`${item.url}/`))
    return pathActive
  }

  const renderNavItem = (item: NavItem) => {
    const isActive = isNavItemActive(item)
    const linkTo =
      item.pullRequestView === "work" && originWorkspace
        ? `/pull-requests/${originWorkspace}`
        : item.url
    const linkSearch =
      item.pullRequestView === "workflow-configurations"
        ? originWorkspace
          ? { from: originWorkspace }
          : undefined
        : item.pullRequestView === "repository-assignments"
          ? originWorkspace
            ? { from: originWorkspace }
            : undefined
          : item.pullRequestView === "settings"
            ? {
                tab: "nudging",
                ...(originWorkspace ? { from: originWorkspace } : {}),
              }
            : item.search
    const prParent = currentPath.startsWith(
      "/pull-requests/workflow-configurations",
    )
      ? "workflow-configurations"
      : currentPath === "/pull-requests/repository-assignments"
        ? "repository-assignments"
        : currentPath === "/pull-requests/settings"
          ? "settings"
          : currentPath.startsWith("/pull-requests/prw_")
            ? "workspace"
            : currentPath === "/pull-requests"
              ? "portfolio"
              : undefined

    const content = (
      <>
        <item.icon
          className={`size-4 ${isActive ? "opacity-100" : "opacity-60"}`}
        />
        <span className={isActive ? "opacity-100" : "opacity-80"}>
          {item.translateTitle === false ? item.title : t(item.title)}
        </span>
      </>
    )

    return (
      <SidebarMenuItem key={`${item.url}-${item.title}`}>
        <SidebarMenuButton
          asChild
          isActive={isActive}
          onClick={handleNavItemClick}
          data-tour={item.tourId}
          className={`h-9 px-3 ${isActive ? "bg-sidebar-accent text-sidebar-accent-foreground font-medium dark:bg-white/10 dark:text-white" : "text-sidebar-foreground/75 hover:bg-sidebar-accent hover:text-sidebar-accent-foreground dark:text-white/70 dark:hover:bg-white/10 dark:hover:text-white"}`}
        >
          <Link
            to={linkTo}
            activeOptions={
              item.pullRequestView === "work" ? { exact: true } : undefined
            }
            aria-current={isActive ? "page" : undefined}
            data-status={isActive ? "active" : undefined}
            search={linkSearch}
            state={
              item.pullRequestView && prParent
                ? (previous: HistoryState) => {
                    const current = asPRNavigationState(previous)
                    return {
                      ...previous,
                      prParent,
                      prParentIndex: current.__TSR_index,
                      prParentKey: current.__TSR_key,
                      ...(prParent === "workspace" || prParent === "portfolio"
                        ? {
                            prWorkIndex: current.__TSR_index,
                            prWorkKey: current.__TSR_key,
                          }
                        : {}),
                    }
                  }
                : undefined
            }
          >
            {content}
          </Link>
        </SidebarMenuButton>
      </SidebarMenuItem>
    )
  }

  const renderServiceSection = (section: NavSection) => (
    <Collapsible
      key={section.label}
      defaultOpen={
        section.label === "navigation.pull_requests"
          ? undefined
          : section.items.some(isNavItemActive)
      }
      open={
        section.label === "navigation.pull_requests"
          ? pullRequestsOpen
          : undefined
      }
      onOpenChange={
        section.label === "navigation.pull_requests"
          ? setPullRequestsOpen
          : undefined
      }
      className="group/service-section mb-1 last:mb-0"
    >
      <CollapsibleTrigger className="text-sidebar-foreground/55 hover:bg-sidebar-accent hover:text-sidebar-accent-foreground flex h-8 w-full cursor-pointer items-center justify-between rounded-lg px-3 text-xs font-medium transition-colors">
        <span>{t(section.label)}</span>
        <IconChevronRight className="size-3.5 opacity-50 transition-transform duration-200 group-data-[state=open]/service-section:rotate-90" />
      </CollapsibleTrigger>
      <CollapsibleContent>
        <SidebarMenu className="border-sidebar-border/80 ml-3 border-l pt-1 pl-2">
          {section.items.map(renderNavItem)}
          {section.isChannelsSection && hasMoreChannels && (
            <SidebarMenuItem key="channels-more-toggle">
              <SidebarMenuButton
                onClick={toggleShowAllChannels}
                className="text-sidebar-foreground/70 hover:bg-sidebar-accent hover:text-sidebar-accent-foreground h-9 px-3"
              >
                {showAllChannels ? (
                  <IconChevronsUp className="size-4 opacity-60" />
                ) : (
                  <IconChevronsDown className="size-4 opacity-60" />
                )}
                <span className="opacity-80">
                  {showAllChannels
                    ? t("navigation.show_less_channels")
                    : t("navigation.show_more_channels")}
                </span>
              </SidebarMenuButton>
            </SidebarMenuItem>
          )}
        </SidebarMenu>
      </CollapsibleContent>
    </Collapsible>
  )

  return (
    <Sidebar
      {...props}
      className="border-sidebar-border/60 bg-sidebar border-r pt-2"
    >
      <SidebarContent className="bg-sidebar px-1">
        <Collapsible defaultOpen className="group/chat-collapsible mb-1">
          <SidebarGroup className="px-2 py-0">
            <SidebarGroupLabel asChild>
              <CollapsibleTrigger className="text-sidebar-foreground/65 hover:bg-sidebar-accent hover:text-sidebar-accent-foreground flex w-full cursor-pointer items-center justify-between rounded-lg px-2 py-1.5 transition-colors">
                <span>{t("navigation.chat")}</span>
                <IconChevronRight className="size-3.5 opacity-50 transition-transform duration-200 group-data-[state=open]/chat-collapsible:rotate-90" />
              </CollapsibleTrigger>
            </SidebarGroupLabel>
            <CollapsibleContent>
              <SidebarGroupContent className="pt-1">
                <SidebarMenu>
                  {renderNavItem(chatNavItem)}
                  {renderNavItem(threadsNavItem)}
                </SidebarMenu>
              </SidebarGroupContent>
            </CollapsibleContent>
          </SidebarGroup>
        </Collapsible>

        <Collapsible
          open={servicesOpen}
          onOpenChange={setServicesOpen}
          className="group/services-collapsible mb-1"
        >
          <SidebarGroup className="px-2 py-0">
            <SidebarGroupLabel asChild>
              <CollapsibleTrigger className="text-sidebar-foreground/65 hover:bg-sidebar-accent hover:text-sidebar-accent-foreground flex w-full cursor-pointer items-center justify-between rounded-lg px-2 py-1.5 transition-colors">
                <span>{t("navigation.services")}</span>
                <IconChevronRight className="size-3.5 opacity-50 transition-transform duration-200 group-data-[state=open]/services-collapsible:rotate-90" />
              </CollapsibleTrigger>
            </SidebarGroupLabel>
            <CollapsibleContent>
              <SidebarGroupContent className="pt-1">
                <SidebarMenu className="mb-1">
                  {renderNavItem(configNavItem)}
                  {renderNavItem(modelsNavItem)}
                  {renderNavItem(accountsNavItem)}
                </SidebarMenu>
                {serviceSections.map(renderServiceSection)}
                <SidebarMenu>
                  {renderNavItem(eventsNavItem)}
                  {renderNavItem(logsNavItem)}
                </SidebarMenu>
              </SidebarGroupContent>
            </CollapsibleContent>
          </SidebarGroup>
        </Collapsible>
      </SidebarContent>
      <SidebarRail />
    </Sidebar>
  )
}

function useAutoRevealCollapsible(
  activeDestination: string | null,
  initiallyOpen: boolean,
): [boolean, React.Dispatch<React.SetStateAction<boolean>>] {
  const [open, setOpen] = React.useState(initiallyOpen)
  const previousDestination = React.useRef(activeDestination)

  React.useEffect(() => {
    if (
      activeDestination != null &&
      activeDestination !== previousDestination.current
    ) {
      setOpen(true)
    }
    previousDestination.current = activeDestination
  }, [activeDestination])

  return [open, setOpen]
}
