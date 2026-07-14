import { IconChevronRight } from "@tabler/icons-react"
import {
  IconAtom,
  IconChevronsDown,
  IconChevronsUp,
  IconKey,
  IconListDetails,
  IconMessageCircle,
  IconMessages,
  IconSearch,
  IconSettings,
  IconSparkles,
  IconTools,
} from "@tabler/icons-react"
import { Link, useRouterState } from "@tanstack/react-router"
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

interface NavItem {
  title: string
  url: string
  icon: React.ComponentType<{ className?: string }>
  translateTitle?: boolean
  tourId?: string
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
  url: "/threads",
  icon: IconMessages,
  translateTitle: true,
}

const logsNavItem: NavItem = {
  title: "navigation.logs",
  url: "/logs",
  icon: IconListDetails,
  translateTitle: true,
}

const configNavItem: NavItem = {
  title: "navigation.config",
  url: "/config",
  icon: IconSettings,
  translateTitle: true,
}

const modelNavItem: NavItem = {
  title: "navigation.models",
  url: "/models",
  icon: IconAtom,
  translateTitle: true,
  tourId: "models-nav",
}

const credentialsNavItem: NavItem = {
  title: "navigation.credentials",
  url: "/credentials",
  icon: IconKey,
  translateTitle: true,
}

export function AppSidebar({ ...props }: React.ComponentProps<typeof Sidebar>) {
  const routerState = useRouterState()
  const { i18n, t } = useTranslation()
  const { isMobile, setOpenMobile } = useSidebar()
  const currentPath = routerState.location.pathname
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
        ],
      },
    ]
  }, [channelItems])

  const renderNavItem = (item: NavItem) => {
    const isActive =
      currentPath === item.url ||
      (item.url !== "/" && currentPath.startsWith(`${item.url}/`))

    return (
      <SidebarMenuItem key={`${item.url}-${item.title}`}>
        <SidebarMenuButton
          asChild
          isActive={isActive}
          onClick={handleNavItemClick}
          data-tour={item.tourId}
          className={`h-9 px-3 ${isActive ? "bg-accent/80 text-foreground font-medium" : "text-muted-foreground hover:bg-muted/60"}`}
        >
          <Link to={item.url}>
            <item.icon
              className={`size-4 ${isActive ? "opacity-100" : "opacity-60"}`}
            />
            <span className={isActive ? "opacity-100" : "opacity-80"}>
              {item.translateTitle === false ? item.title : t(item.title)}
            </span>
          </Link>
        </SidebarMenuButton>
      </SidebarMenuItem>
    )
  }

  const renderServiceSection = (section: NavSection) => (
    <Collapsible
      key={section.label}
      className="group/service-section mb-1 last:mb-0"
    >
      <CollapsibleTrigger className="text-sidebar-foreground/60 hover:bg-muted/60 flex h-8 w-full cursor-pointer items-center justify-between rounded-md px-3 text-xs font-medium transition-colors">
        <span>{t(section.label)}</span>
        <IconChevronRight className="size-3.5 opacity-50 transition-transform duration-200 group-data-[state=open]/service-section:rotate-90" />
      </CollapsibleTrigger>
      <CollapsibleContent>
        <SidebarMenu className="border-border/40 ml-3 border-l pt-1 pl-2">
          {section.items.map(renderNavItem)}
          {section.isChannelsSection && hasMoreChannels && (
            <SidebarMenuItem key="channels-more-toggle">
              <SidebarMenuButton
                onClick={toggleShowAllChannels}
                className="text-muted-foreground hover:bg-muted/60 h-9 px-3"
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
      className="bg-background border-r-border/20 border-r pt-3"
    >
      <SidebarContent className="bg-background">
        <Collapsible defaultOpen className="group/chat-collapsible mb-1">
          <SidebarGroup className="px-2 py-0">
            <SidebarGroupLabel asChild>
              <CollapsibleTrigger className="hover:bg-muted/60 flex w-full cursor-pointer items-center justify-between rounded-md px-2 py-1.5 transition-colors">
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

        <Collapsible className="group/services-collapsible mb-1">
          <SidebarGroup className="px-2 py-0">
            <SidebarGroupLabel asChild>
              <CollapsibleTrigger className="hover:bg-muted/60 flex w-full cursor-pointer items-center justify-between rounded-md px-2 py-1.5 transition-colors">
                <span>{t("navigation.services")}</span>
                <IconChevronRight className="size-3.5 opacity-50 transition-transform duration-200 group-data-[state=open]/services-collapsible:rotate-90" />
              </CollapsibleTrigger>
            </SidebarGroupLabel>
            <CollapsibleContent>
              <SidebarGroupContent className="pt-1">
                <SidebarMenu className="mb-1">
                  {renderNavItem(configNavItem)}
                  {renderNavItem(modelNavItem)}
                  {renderNavItem(credentialsNavItem)}
                </SidebarMenu>
                {serviceSections.map(renderServiceSection)}
                <SidebarMenu>{renderNavItem(logsNavItem)}</SidebarMenu>
              </SidebarGroupContent>
            </CollapsibleContent>
          </SidebarGroup>
        </Collapsible>
      </SidebarContent>
      <SidebarRail />
    </Sidebar>
  )
}
