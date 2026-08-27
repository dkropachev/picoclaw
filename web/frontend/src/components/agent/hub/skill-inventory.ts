import { maximumCollectionPageSize } from "@/api/collection"
import { type SkillSupportItem, listSkills } from "@/api/skills"

const maximumSkillInventoryPages = 100

export async function loadSkillInventory(
  signal?: AbortSignal,
): Promise<SkillSupportItem[]> {
  const items = new Map<string, SkillSupportItem>()
  const seenCursors = new Set<string>()
  let cursor: string | undefined

  for (let page = 0; page < maximumSkillInventoryPages; page += 1) {
    signal?.throwIfAborted()
    const response = await listSkills(
      { cursor, limit: maximumCollectionPageSize },
      signal,
    )
    for (const skill of response.skills) items.set(skill.id, skill)

    const nextCursor = response.next_cursor?.trim()
    if (!nextCursor) return [...items.values()]
    if (seenCursors.has(nextCursor)) {
      throw new Error("Skill inventory pagination returned a repeated cursor.")
    }
    seenCursors.add(nextCursor)
    cursor = nextCursor
  }

  throw new Error("Skill inventory exceeds the safe pagination limit.")
}

export function indexWorkspaceSkills(
  skills: readonly SkillSupportItem[],
): Map<string, SkillSupportItem> {
  return new Map(
    skills
      .filter((skill) => skill.source === "workspace")
      .map((skill) => [normalizeSkillLookupName(skill.name), skill] as const),
  )
}

export function findWorkspaceSkill(
  skillsByName: ReadonlyMap<string, SkillSupportItem>,
  name: string,
): SkillSupportItem | null {
  return skillsByName.get(normalizeSkillLookupName(name)) ?? null
}

function normalizeSkillLookupName(name: string): string {
  return name.trim().toLowerCase()
}
