import { IconChevronRight } from "@tabler/icons-react"
import {
  IconBellRinging,
  IconBrain,
  IconBug,
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
import { Link, useRouterState } from "@tanstack/react-router"
import * as React from "react"
import { useTranslation } from "react-i18next"

import { listDevelopmentNotifications } from "@/api/notifications"
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

interface NavItem {
  title: string
  url: string
  icon: React.ComponentType<{ className?: string }>
  translateTitle?: boolean
  tourId?: string
  search?: Record<string, string>
  exact?: boolean
}

interface NavSection {
  label: string
  items: NavItem[]
  isChannelsSection?: boolean
  translateLabel?: boolean
  controlled?: "development" | "repository-reviews"
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

const developmentNavItem: NavItem = {
  title: "Development",
  url: "/development",
  icon: IconGitPullRequest,
  translateTitle: false,
}

const notificationsNavItem: NavItem = {
  title: "Notifications",
  url: "/notifications",
  icon: IconBellRinging,
  translateTitle: false,
}

const developmentRepositoriesNavItem: NavItem = {
  title: "Repositories",
  url: "/development/repositories",
  icon: IconGitBranch,
  translateTitle: false,
}

const developmentPoliciesNavItem: NavItem = {
  title: "Policies",
  url: "/development/workflow-configurations",
  icon: IconSettings,
  translateTitle: false,
}

const developmentSettingsNavItem: NavItem = {
  title: "Settings",
  url: "/development/settings",
  icon: IconSettings,
  translateTitle: false,
}

const repositoryReviewsNavItem: NavItem = {
  title: "Review runs",
  url: "/repository-reviews",
  icon: IconBug,
  translateTitle: false,
  exact: true,
}

const repositoryReviewRepositoriesNavItem: NavItem = {
  title: "Repositories",
  url: "/repository-reviews/repositories",
  icon: IconGitBranch,
  translateTitle: false,
}

const repositoryReviewProfilesNavItem: NavItem = {
  title: "Profiles",
  url: "/repository-reviews/profiles",
  icon: IconSettings,
  translateTitle: false,
}

const modelEvaluationsNavItem: NavItem = {
  title: "Model review probes",
  url: "/model-evaluations",
  icon: IconBrain,
  translateTitle: false,
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
  title: "Model aliases",
  url: "/models/aliases",
  icon: IconDatabase,
  translateTitle: false,
}

const modelRoutersNavItem: NavItem = {
  title: "Model routers",
  url: "/models/routers",
  icon: IconRoute,
  translateTitle: false,
}

export function AppSidebar({ ...props }: React.ComponentProps<typeof Sidebar>) {
  const routerState = useRouterState()
  const { i18n, t } = useTranslation()
  const { isMobile, setOpenMobile } = useSidebar()
  const [notificationCount, setNotificationCount] = React.useState(0)
  React.useEffect(() => {
    const controller = new AbortController()
    const refresh = () =>
      void listDevelopmentNotifications(
        { query: "status = open", limit: 1 },
        controller.signal,
      )
        .then((page) => setNotificationCount(page.counts?.open ?? 0))
        .catch(() => undefined)
    refresh()
    const interval = window.setInterval(refresh, 30_000)
    return () => {
      controller.abort()
      window.clearInterval(interval)
    }
  }, [])
  const currentPath = routerState.location.pathname
  const developmentDestination =
    currentPath === "/development" ||
    currentPath.startsWith("/development/") ||
    currentPath === "/notifications" ||
    currentPath.startsWith("/notifications/")
      ? currentPath
      : null
  const repositoryReviewsDestination =
    currentPath === "/repository-reviews" ||
    currentPath.startsWith("/repository-reviews/")
      ? currentPath
      : null
  const modelEvaluationsDestination =
    currentPath === "/model-evaluations" ||
    currentPath.startsWith("/model-evaluations/")
      ? currentPath
      : null
  const servicesDestination =
    developmentDestination ??
    repositoryReviewsDestination ??
    modelEvaluationsDestination
  const [servicesOpen, setServicesOpen] = useAutoRevealCollapsible(
    servicesDestination,
    currentPath.startsWith("/agent/") || servicesDestination != null,
  )
  const [developmentOpen, setDevelopmentOpen] = useAutoRevealCollapsible(
    developmentDestination,
    developmentDestination != null,
  )
  const [repositoryReviewsOpen, setRepositoryReviewsOpen] =
    useAutoRevealCollapsible(
      repositoryReviewsDestination ?? modelEvaluationsDestination,
      repositoryReviewsDestination != null ||
        modelEvaluationsDestination != null,
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
            url: "/agent/mcp/servers",
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
        label: "Development",
        controlled: "development",
        items: [
          developmentNavItem,
          notificationsNavItem,
          developmentRepositoriesNavItem,
          developmentPoliciesNavItem,
          developmentSettingsNavItem,
        ],
      },
      {
        label: "Repository reviews",
        translateLabel: false,
        controlled: "repository-reviews",
        items: [
          repositoryReviewsNavItem,
          repositoryReviewRepositoriesNavItem,
          repositoryReviewProfilesNavItem,
          modelEvaluationsNavItem,
        ],
      },
    ]
  }, [channelItems])

  const isNavItemActive = (item: NavItem) => {
    if (item.url === "/repository-reviews") {
      return (
        currentPath === item.url ||
        (currentPath.startsWith(`${item.url}/`) &&
          !currentPath.startsWith("/repository-reviews/repositories") &&
          !currentPath.startsWith("/repository-reviews/profiles") &&
          currentPath !== "/repository-reviews/results")
      )
    }
    const pathActive =
      currentPath === item.url ||
      (!item.exact &&
        item.url !== "/" &&
        currentPath.startsWith(`${item.url}/`))
    return pathActive
  }

  const renderNavItem = (item: NavItem) => {
    const isActive = isNavItemActive(item)
    const linkTo = item.url
    const linkSearch = item.search

    const content = (
      <>
        <item.icon
          className={`size-4 ${isActive ? "opacity-100" : "opacity-60"}`}
        />
        <span className={isActive ? "opacity-100" : "opacity-80"}>
          {item.translateTitle === false ? item.title : t(item.title)}
        </span>
        {item.url === "/notifications" && notificationCount > 0 && (
          <span className="bg-primary text-primary-foreground ml-auto rounded-full px-1.5 text-[10px] font-medium">
            {notificationCount}
          </span>
        )}
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
              item.url === "/development" ? { exact: true } : undefined
            }
            aria-current={isActive ? "page" : undefined}
            data-status={isActive ? "active" : undefined}
            search={linkSearch ?? {}}
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
        section.controlled ? undefined : section.items.some(isNavItemActive)
      }
      open={
        section.controlled === "development"
          ? developmentOpen
          : section.controlled === "repository-reviews"
            ? repositoryReviewsOpen
            : undefined
      }
      onOpenChange={
        section.controlled === "development"
          ? setDevelopmentOpen
          : section.controlled === "repository-reviews"
            ? setRepositoryReviewsOpen
            : undefined
      }
      className="group/service-section mb-1 last:mb-0"
    >
      <CollapsibleTrigger className="text-sidebar-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground flex h-8 w-full cursor-pointer items-center justify-between rounded-lg px-3 text-xs font-medium transition-colors">
        <span>
          {section.translateLabel === false ? section.label : t(section.label)}
        </span>
        <IconChevronRight className="size-3.5 opacity-50 transition-transform duration-200 group-data-[state=open]/service-section:rotate-90" />
      </CollapsibleTrigger>
      <CollapsibleContent>
        <SidebarMenu className="border-sidebar-border/80 ml-3 border-l pt-1 pl-2">
          {section.items.map(renderNavItem)}
          {section.isChannelsSection && hasMoreChannels && (
            <SidebarMenuItem key="channels-more-toggle">
              <SidebarMenuButton
                onClick={toggleShowAllChannels}
                className="text-sidebar-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground h-9 px-3"
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
              <CollapsibleTrigger className="!text-sidebar-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground flex w-full cursor-pointer items-center justify-between rounded-lg px-2 py-1.5 transition-colors">
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
              <CollapsibleTrigger className="!text-sidebar-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground flex w-full cursor-pointer items-center justify-between rounded-lg px-2 py-1.5 transition-colors">
                <span>{t("navigation.services")}</span>
                <IconChevronRight className="size-3.5 opacity-50 transition-transform duration-200 group-data-[state=open]/services-collapsible:rotate-90" />
              </CollapsibleTrigger>
            </SidebarGroupLabel>
            <CollapsibleContent>
              <SidebarGroupContent className="pt-1">
                <SidebarMenu className="mb-1">
                  {renderNavItem(configNavItem)}
                  {renderNavItem(modelsNavItem)}
                  {renderNavItem(modelRoutersNavItem)}
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
