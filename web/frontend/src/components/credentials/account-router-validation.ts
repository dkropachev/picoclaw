export function isReservedAccountRouterCreateName(name: string): boolean {
  return name.trim().toLowerCase() === "new"
}
