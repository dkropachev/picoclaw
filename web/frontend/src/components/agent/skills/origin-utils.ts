import type { TFunction } from "i18next"

import type { SkillSupportItem } from "@/api/skills"

export function getSkillOriginKind(skill: SkillSupportItem) {
  const origin = skill.origin || skill.origin_kind || skill.source
  return origin === "global" ? "builtin" : origin
}

export function getOriginLabel(origin: string, t: TFunction) {
  if (origin === "builtin" || origin === "third_party" || origin === "manual") {
    return t(`pages.agent.skills.origin.${origin}`)
  }
  if (origin === "all") {
    return t("pages.agent.skills.origin.all")
  }
  return origin
}

export function getOriginAccentClasses(origin: string) {
  if (origin === "manual") {
    return "bg-emerald-100 text-emerald-800"
  }
  if (origin === "third_party") {
    return "bg-sky-100 text-sky-700"
  }
  if (origin === "builtin") {
    return "bg-amber-100 text-amber-700"
  }
  return "bg-muted text-muted-foreground"
}

export function getOriginBadgeClasses(origin: string) {
  if (origin === "manual") {
    return "bg-emerald-100 text-emerald-800"
  }
  if (origin === "third_party") {
    return "bg-sky-100 text-sky-700"
  }
  if (origin === "builtin") {
    return "bg-amber-100 text-amber-700"
  }
  return "bg-muted text-muted-foreground"
}
