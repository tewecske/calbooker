// Unique values so specs never collide with each other, with earlier runs, or
// with other worktrees sharing the same provider and Hydra.

/** A unique address in the reserved example.test domain. */
export function uniqueEmail(prefix: string): string {
  return `${prefix}-${crypto.randomUUID()}@example.test`;
}

/** A unique throwaway password that meets the provider's rules. */
export function uniquePassword(): string {
  return `sso-${crypto.randomUUID()}-password`;
}

/** A short unique display name, for example a group or resource name. */
export function uniqueName(prefix: string): string {
  return `${prefix} ${crypto.randomUUID().slice(0, 8)}`;
}
