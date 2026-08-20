/** isPlainObject narrows unknown JSON-parsed data to a non-null, non-array object -- shared by transport.ts (frame shape classification) and sessionStream.ts (event envelope parsing) so both apply the exact same notion of "object", not two subtly different ones. */
export function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
