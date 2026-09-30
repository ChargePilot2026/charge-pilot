export type PackageMode = 'amount' | 'duration' | 'energy';
export type Algorithm = 'server_max_power' | 'server_realtime_power' | 'server_energy';
export interface Tier { max_watts: number; electric_cents: number; service_cents: number }
export interface Period { end_minute: number; electric_cents?: number; service_cents?: number; tiers?: Tier[] }
export interface Package { id: number; name: string; mode: PackageMode; price_cents: number; minutes?: number; kwh?: number }
export interface Scheme {
  name: string; remark: string;
  amount?: { algorithm: Algorithm; periods: Period[] };
  energy?: { electric_cents: number; service_cents: number };
  packages: Package[];
  policy: { free_minutes: number; min_electric_cents: number; max_minutes: number; loss_rate_bp: number; channel_bp: number };
  stop: { stop_when_full: boolean };
  card: { package_id: number; max_minutes: number };
  display: Record<string, boolean>;
}
export interface Template { id: number; scheme: Scheme; version: number; status: string }
export const modes: Record<PackageMode, string> = { amount: '金额', duration: '时长', energy: '电量' };
export const displayLabels: Record<string, string> = {
  show_energy: '显示充电电量', show_power: '显示充电功率', show_tariff: '显示时段计费详情',
  show_method: '显示计费方式', show_rule: '显示规则说明', show_fee_split: '显示订单电费与服务费',
  fee_split_inline: '支付金额旁显示费用', show_fee_on_end: '结束推送显示费用', hide_unit: '隐藏计量单位',
};
export const blankScheme = (): Scheme => ({ name: '', remark: '', packages: [],
  policy: { free_minutes: 0, min_electric_cents: 0, max_minutes: 600, loss_rate_bp: 0, channel_bp: 10000 },
  stop: { stop_when_full: true }, card: { package_id: 0, max_minutes: 600 },
  display: Object.fromEntries(Object.keys(displayLabels).map(key => [key, ['show_energy', 'show_power', 'show_method', 'show_fee_split', 'fee_split_inline', 'show_fee_on_end'].includes(key)])),
});
export function priceEnergy(scheme: Scheme): Scheme {
  return { ...scheme, packages: scheme.packages.map(p => p.mode === 'energy' && scheme.energy
    ? { ...p, price_cents: (p.kwh || 0) * (scheme.energy.electric_cents + scheme.energy.service_cents) } : p) };
}
export function missingInputs(s: Scheme): string[] {
  const errors: string[] = [];
  const validRate = (v: number | undefined) => Number.isInteger(v) && v! >= 0;
  if (!s.name.trim()) errors.push('方案名称');
  if (!s.packages.length) errors.push('至少一个支付套餐');
  if (s.amount) {
    let previous = 0;
    s.amount.periods.forEach((p, i) => {
      if (!Number.isInteger(p.end_minute) || p.end_minute <= previous || p.end_minute > 1440) errors.push(`时段${i + 1}的结束时间`);
      previous = p.end_minute;
      if (s.amount!.algorithm === 'server_energy') {
        if (!validRate(p.electric_cents)) errors.push(`时段${i + 1}电费`);
        if (!validRate(p.service_cents)) errors.push(`时段${i + 1}服务费`);
      } else {
        if (!p.tiers?.length) errors.push(`时段${i + 1}功率档位`);
        if ((p.tiers?.length || 0) > 8) errors.push(`时段${i + 1}最多8个功率档位`);
        let previousWatts = -1;
        p.tiers?.forEach((t, j) => {
          if (!Number.isInteger(t.max_watts) || t.max_watts <= previousWatts || t.max_watts > 9990) errors.push(`时段${i + 1}档位${j + 1}功率上限`);
          previousWatts = t.max_watts;
          if (!validRate(t.electric_cents)) errors.push(`时段${i + 1}档位${j + 1}电费`);
          if (!validRate(t.service_cents)) errors.push(`时段${i + 1}档位${j + 1}服务费`);
        });
      }
    });
    if (previous !== 1440) errors.push('全天时段覆盖');
    if (!s.packages.some(p => p.mode === 'amount')) errors.push('金额套餐');
  }
  if (s.energy) {
    if (!validRate(s.energy.electric_cents)) errors.push('设备电量电费单价');
    if (!validRate(s.energy.service_cents)) errors.push('设备电量服务费单价');
    if (!s.packages.some(p => p.mode === 'energy')) errors.push('电量套餐');
  }
  s.packages.forEach(p => {
    if (!p.name.trim() || !Number.isFinite(p.price_cents) || p.price_cents <= 0) errors.push(`套餐${p.id}名称与价格`);
    if (p.mode === 'duration' && (!Number.isInteger(p.minutes) || !p.minutes || p.minutes > 4320)) errors.push(`套餐${p.id}分钟数`);
    if (p.mode === 'energy' && (!p.kwh || !Number.isInteger(p.kwh) || p.kwh > 65)) errors.push(`套餐${p.id}整数度数`);
    if (p.mode === 'amount' && p.price_cents < s.policy.min_electric_cents) errors.push(`套餐${p.id}金额低于最低电费`);
  });
  if (s.policy.max_minutes < 60 || s.policy.max_minutes > 4320 || s.card.max_minutes < 60 || s.card.max_minutes > 4320) errors.push('1～72小时的时长上限');
  if (s.card.package_id && !s.packages.some(p => p.id === s.card.package_id && p.mode === 'duration')) errors.push('刷卡指定时长套餐');
  if (s.packages.some(p => p.id === s.card.package_id && (p.minutes || 0) > s.card.max_minutes)) errors.push('刷卡套餐超过累计上限');
  return errors;
}
export const clock = (minutes: number) => `${String(Math.floor(minutes / 60)).padStart(2, '0')}:${String(minutes % 60).padStart(2, '0')}`;
export const money = (cents: number) => Number.isFinite(cents) ? `¥${(cents / 100).toFixed(2)}` : '待填写';
