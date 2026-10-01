const portStatuses: Record<number, string> = { 0: '空闲', 1: '充电', 3: '输出故障', 4: '粘连' };

export function signalStrengthText(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '—';
  return `CSQ ${value}${value === 99 ? '（未知）' : ''}`;
}

export function portStatusText(value: number | null | undefined): string {
  return value == null ? '—' : portStatuses[value] || `未知（${value}）`;
}
