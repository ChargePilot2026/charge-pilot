export const businessStatuses: Record<string, { label: string; color: string }> = {
  pending_start: { label: '待启动', color: 'gold' },
  charging: { label: '充电中', color: 'green' },
  completed: { label: '已完成', color: 'cyan' },
};

export const paymentStatuses: Record<string, { label: string; color: string }> = {
  pending: { label: '待支付', color: 'gold' },
  paid: { label: '已支付', color: 'blue' },
  refunded: { label: '全额退款', color: 'purple' },
  partial_refunded: { label: '部分退款', color: 'orange' },
};

export const paymentStatusColors: Record<string, string> = {
  pending: '#ffa940', paid: '#73d13d', refunded: '#ff4d4f', partial_refunded: '#ff7a45',
};

type StatusRow = {
  business_status: string;
  payment_status: string;
  status?: string;
  refund_status?: string;
  payment_order_status?: string | null;
  failure_reason?: string | null;
};

type StatusInfo = { label: string; color?: string; hint?: string };

export function businessStatusInfo(row: StatusRow): StatusInfo {
  const info = businessStatuses[row.business_status] || { label: row.business_status || '—' };
  if (row.status === 'cancelled') {
    return { ...info, hint: `订单已取消，充电流程已终止。${row.failure_reason || ''}` };
  }
  if (row.status === 'failed') {
    return { ...info, hint: `充电流程失败并已终止。${row.failure_reason || '具体原因请查看订单详情。'}` };
  }
  if (row.business_status === 'completed' && row.failure_reason) {
    return { ...info, hint: `充电流程已终止。${row.failure_reason}` };
  }
  return info;
}

export function paymentStatusInfo(row: Omit<StatusRow, 'business_status'>): StatusInfo {
  const info = paymentStatuses[row.payment_status] || { label: row.payment_status || '—' };
  if (row.refund_status === 'processing' || row.status === 'refunding') {
    return { ...info, hint: '退款处理中，支付状态将在退款成功后更新。' };
  }
  if (row.payment_status === 'pending') {
    const hints: Record<string, string> = {
      paying: '支付确认中，尚未确认到账。',
      failed: '支付失败，尚未确认到账。',
      closed: '支付单已关闭，尚未确认到账。',
    };
    const hint = hints[row.payment_order_status || ''];
    if (hint) return { ...info, hint };
  }
  return info;
}

type DurationRow = {
  business_status: string;
  started_at: string | null;
  ended_at: string | null;
  duration_seconds: number | null;
  live?: { at: string; stale: boolean; seconds: number };
  live_unavailable?: string;
};
type DurationInfo = {
  text: string;
  source: 'live' | 'measured' | 'estimated' | 'unavailable';
  hint?: string;
  sampledAt?: string;
};

function validSeconds(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 && value <= Number.MAX_SAFE_INTEGER;
}

export function formatDuration(seconds: number): string {
  if (!validSeconds(seconds)) return '—';
  const whole = Math.floor(seconds);
  const hours = Math.floor(whole / 3600);
  const minutes = Math.floor((whole % 3600) / 60);
  const remainder = whole % 60;
  return `${hours ? `${hours}小时` : ''}${hours || minutes ? `${minutes}分` : ''}${remainder}秒`;
}

// 来源决定可信程度：实时计量、最终计量、真实起止时间估算；缺失计量不补零。
export function chargingDuration(row: DurationRow): DurationInfo {
  if (row.business_status === 'pending_start') return { text: '—', source: 'unavailable' };
  if (row.business_status === 'charging') {
    if (row.live && validSeconds(row.live.seconds)) {
      return {
        text: `${formatDuration(row.live.seconds)}${row.live.stale ? '（旧）' : ''}`,
        source: 'live', sampledAt: row.live.at,
        hint: row.live.stale ? '读数已过期，保留最后采样时长。' : '设备上报的已充电时长。',
      };
    }
    return { text: '待上报', source: 'unavailable', hint: row.live_unavailable || '等待设备上报有效的充电时长。' };
  }
  if (row.business_status !== 'completed') return { text: '—', source: 'unavailable' };
  if (validSeconds(row.duration_seconds)) return { text: formatDuration(row.duration_seconds), source: 'measured' };
  if (row.started_at && row.ended_at) {
    const start = Date.parse(row.started_at), end = Date.parse(row.ended_at);
    if (Number.isFinite(start) && Number.isFinite(end) && end >= start) {
      return {
        text: `≈${formatDuration((end - start) / 1000)}`, source: 'estimated',
        hint: '最终计量时长尚未记录，按实际开始和结束时间估算。',
      };
    }
  }
  return { text: '—', source: 'unavailable' };
}

export function refundedAmount(cents: number | null | undefined): string {
  return cents == null || !Number.isFinite(cents) ? '—' : `¥${(cents / 100).toFixed(2)}`;
}
