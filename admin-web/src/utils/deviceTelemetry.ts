const portStatuses: Record<number, string> = { 0: '空闲', 1: '充电', 3: '输出故障', 4: '粘连' };

export function signalStrengthText(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '—';
  return `CSQ ${value}${value === 99 ? '（未知）' : ''}`;
}

export function portStatusText(value: number | null | undefined): string {
  return value == null ? '—' : portStatuses[value] || `未知（${value}）`;
}

export function lastOnlineText(value: string | null | undefined, now = Date.now()): string {
  if (value == null || !value.trim()) return '从未上线';
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp) || !Number.isFinite(now)) return '—';
  const elapsed = now - timestamp;
  if (elapsed < 5 * 60_000) return '刚刚';
  if (elapsed < 60 * 60_000) return '1小时内';
  return '离线';
}
