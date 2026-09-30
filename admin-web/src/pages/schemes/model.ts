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
export type AmountConfig = NonNullable<Scheme['amount']>;
export type AmountDrafts = Partial<Record<Algorithm, AmountConfig>>;
export function switchAmountAlgorithm(amount: AmountConfig, drafts: AmountDrafts, algorithm: Algorithm): { amount: AmountConfig; drafts: AmountDrafts } {
  const nextDrafts = { ...drafts, [amount.algorithm]: structuredClone(amount) };
  const next = nextDrafts[algorithm] ?? { algorithm, periods: algorithm === 'server_energy'
    ? [{ end_minute: 1440, electric_cents: 0, service_cents: 0 }]
    : [{ end_minute: 1440, tiers: [{ max_watts: 200, electric_cents: 0, service_cents: 0 }] }] };
  return { amount: structuredClone(next), drafts: nextDrafts };
}
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
export interface SchemeIssue { message: string; fields: string[]; step: number }
export function validateScheme(s: Scheme): SchemeIssue[] {
  const errors: SchemeIssue[] = [];
  const add = (message: string, step: number, ...fields: string[]) => errors.push({ message, step, fields });
  const validRate = (v: number | undefined) => Number.isInteger(v) && v! >= 0;
  if (!s.name.trim()) add('方案名称', 0, 'name');
  if (!s.packages.length) add('至少一个支付套餐', s.amount ? 1 : s.energy ? 2 : 3, 'packages');
  if (s.amount) {
    let previous = 0;
    s.amount.periods.forEach((p, i) => {
      const path = `amount.periods.${i}`;
      if (!Number.isInteger(p.end_minute) || p.end_minute <= previous || p.end_minute > 1440) add(`时段${i + 1}的结束时间`, 1, `${path}.end_minute`);
      previous = p.end_minute;
      if (s.amount!.algorithm === 'server_energy') {
        if (!validRate(p.electric_cents)) add(`时段${i + 1}电费`, 1, `${path}.electric_cents`);
        if (!validRate(p.service_cents)) add(`时段${i + 1}服务费`, 1, `${path}.service_cents`);
      } else {
        if (!p.tiers?.length) add(`时段${i + 1}功率档位`, 1, `${path}.tiers`);
        if ((p.tiers?.length || 0) > 8) add(`时段${i + 1}最多8个功率档位`, 1, `${path}.tiers`);
        let previousWatts = -1;
        p.tiers?.forEach((t, j) => {
          const tierPath = `${path}.tiers.${j}`;
          if (!Number.isInteger(t.max_watts) || t.max_watts <= previousWatts || t.max_watts > 9990) add(`时段${i + 1}档位${j + 1}功率上限`, 1, `${tierPath}.max_watts`);
          previousWatts = t.max_watts;
          if (!validRate(t.electric_cents)) add(`时段${i + 1}档位${j + 1}电费`, 1, `${tierPath}.electric_cents`);
          if (!validRate(t.service_cents)) add(`时段${i + 1}档位${j + 1}服务费`, 1, `${tierPath}.service_cents`);
        });
      }
    });
    if (previous !== 1440) add('全天时段覆盖', 1, 'amount.periods', `amount.periods.${s.amount.periods.length - 1}.end_minute`);
    if (!s.packages.some(p => p.mode === 'amount')) add('金额套餐', 1, 'amount.packages');
  }
  if (s.energy) {
    if (!validRate(s.energy.electric_cents)) add('设备电量电费单价', 2, 'energy.electric_cents');
    if (!validRate(s.energy.service_cents)) add('设备电量服务费单价', 2, 'energy.service_cents');
    if (!s.packages.some(p => p.mode === 'energy')) add('电量套餐', 2, 'energy.packages');
  }
  s.packages.forEach(p => {
    const step = p.mode === 'amount' ? 1 : p.mode === 'energy' ? 2 : 3;
    const path = `packages.${p.id}`;
    const invalid = [];
    if (!p.name.trim()) invalid.push(`${path}.name`);
    if (!Number.isInteger(p.price_cents) || p.price_cents <= 0) invalid.push(`${path}.price_cents`);
    if (invalid.length) add(`套餐${p.id}名称与价格`, step, ...invalid);
    if (p.mode === 'duration' && (!Number.isInteger(p.minutes) || !p.minutes || p.minutes < 1 || p.minutes > 4320)) add(`套餐${p.id}分钟数`, step, `${path}.minutes`);
    if (p.mode === 'energy' && (!p.kwh || !Number.isInteger(p.kwh) || p.kwh < 1 || p.kwh > 65)) add(`套餐${p.id}整数度数`, step, `${path}.kwh`);
    if (p.mode === 'amount' && p.price_cents < s.policy.min_electric_cents) add(`套餐${p.id}金额低于最低电费`, step, `${path}.price_cents`);
  });
  if (!Number.isInteger(s.policy.max_minutes) || s.policy.max_minutes < 60 || s.policy.max_minutes > 4320) add('金额模式时长上限须为1～72小时', 1, 'policy.max_minutes');
  if (!Number.isInteger(s.card.max_minutes) || s.card.max_minutes < 60 || s.card.max_minutes > 4320) add('刷卡累计上限须为1～72小时', 3, 'card.max_minutes');
  if (!Number.isInteger(s.policy.free_minutes) || s.policy.free_minutes < 0 || s.policy.free_minutes > s.policy.max_minutes) add('免费时长须为0到金额模式最长时长之间的整数分钟', 1, 'policy.free_minutes');
  if (!validRate(s.policy.min_electric_cents)) add('最低电费须为非负金额', 1, 'policy.min_electric_cents');
  if (s.card.package_id && !s.packages.some(p => p.id === s.card.package_id && p.mode === 'duration')) add('刷卡指定时长套餐', 3, 'card.package_id');
  if (s.packages.some(p => p.id === s.card.package_id && (p.minutes || 0) > s.card.max_minutes)) add('刷卡套餐超过累计上限', 3, 'card.package_id', 'card.max_minutes', `packages.${s.card.package_id}.minutes`);
  return errors;
}
export const missingInputs = (s: Scheme): string[] => validateScheme(s).map(issue => issue.message);
export const clock = (minutes: number) => `${String(Math.floor(minutes / 60)).padStart(2, '0')}:${String(minutes % 60).padStart(2, '0')}`;
export const money = (cents: number) => Number.isFinite(cents) ? `¥${(cents / 100).toFixed(2)}` : '待填写';
