const MAX_USER_ID = '18446744073709551615';

/** Public charging-user IDs remain decimal text, including IDs above Number.MAX_SAFE_INTEGER. */
export function isChargeUserID(value: unknown): value is string {
  return typeof value === 'string' && /^[1-9]\d{0,19}$/.test(value)
    && (value.length < MAX_USER_ID.length || value <= MAX_USER_ID);
}
