import type {
  EvaluationModelOption,
  EvaluationProfileOption,
} from "@/api/model-evaluations"

export function profileAvailableAliases(
  profile: EvaluationProfileOption | undefined,
  models: EvaluationModelOption[],
): string[] {
  if (!profile) return []
  const configured = new Set(
    models.filter((model) => model.available).map((model) => model.alias),
  )
  return profile.available_models.filter((alias) => configured.has(alias))
}

export function selectProfileCandidates(
  current: string[],
  profile: EvaluationProfileOption | undefined,
  models: EvaluationModelOption[],
  maximum: number,
): string[] {
  const available = profileAvailableAliases(profile, models)
  const allowed = new Set(available)
  const selected = current.filter((alias) => allowed.has(alias))
  if (
    profile?.reviewer_model &&
    allowed.has(profile.reviewer_model) &&
    !selected.includes(profile.reviewer_model)
  ) {
    selected.unshift(profile.reviewer_model)
  }
  for (const alias of available) {
    if (selected.length >= 2) break
    if (!selected.includes(alias)) selected.push(alias)
  }
  return selected.slice(0, maximum)
}
