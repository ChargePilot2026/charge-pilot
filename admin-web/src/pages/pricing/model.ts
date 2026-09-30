// 计价模型集中放在这一处：线上的数据结构，以及把存下来的 Spec 换算成运营脑子里
// 装得下的东西的那套算术。
//
// 编辑器一律以「元」和「人看的分钟」为单位（运营读的就是这个），而这个文件是唯一
// 把它换算成引擎实际存的「分」和「结束分钟」的地方。后端强制的链条不变量在这里也
// 一起校验，并且报错要点名是哪一段出问题——服务端只回一句"参数无效"，等于没告诉
// 运营任何能照着改的东西。

export type ChargeMode =
  | 'server_realtime_power' | 'server_max_power' | 'server_energy'
  | 'device_duration' | 'device_energy' | 'device_power';
export type ServerBasis = 'realtime_power' | 'max_power' | 'energy';
export type ServiceBasis = 'none' | 'energy' | 'minute_power' | 'minute' | 'session';
export type TierPriceBasis = 'per_hour_at_ceiling' | 'per_kwh';

export type Tier = { max_watts: number; electric_cents: number; service_cents?: number };
export type Period = { end_minute: number; electric_cents?: number; tiers?: Tier[] };
export type Display = {
  show_energy: boolean; show_power: boolean; show_tariff: boolean; show_fee_split: boolean;
  fee_split_inline: boolean; show_fee_on_end: boolean; show_method: boolean; show_rule: boolean; hide_unit: boolean;
};
export type Spec = {
  mode: ChargeMode;
  electric?: { basis: ServerBasis; periods: Period[] };
  service?: { basis: ServiceBasis; cents_per_kwh?: number; cents_per_minute?: number; cents_per_session?: number };
  multiplier?: { temp_bp: number; card_bp: number };
  tier_price_basis?: TierPriceBasis;
  loss_rate_bp?: number; free_minutes?: number; min_electric_cents?: number;
  time_charge?: { stop_when_full: boolean; max_minutes?: number; float_power_deci_watts?: number; float_seconds?: number };
  spend_cap_cents?: number; stop_grace_seconds?: number;
  default_charge_way?: string; card_max_minutes?: number;
  display?: Display;
};

export type Template = {
  id: number; name: string; remark: string; status: string; version: number;
  spec?: Spec; display?: Display; applied_stations?: string;
};
export type Station = { id: number; code: string; name: string; status?: string };

export const SERVER_MODES: ChargeMode[] = ['server_realtime_power', 'server_max_power', 'server_energy'];
export const DEVICE_MODES: ChargeMode[] = ['device_duration', 'device_energy', 'device_power'];

export const MODE_META: Record<ChargeMode, { label: string; hint: string; group: string }> = {
  server_realtime_power: {
    label: '服务端计费 · 实时功率', group: '服务端计费',
    hint: '按每一分钟的实时功率落入的档位定价，档位单价作用于该段电量。适合错峰与功率阶梯定价。',
  },
  server_max_power: {
    label: '服务端计费 · 最大功率', group: '服务端计费',
    hint: '整场按出现过的最高功率所在档位计费，档位单价为元/小时，短暂冲高也按峰值收。',
  },
  server_energy: {
    label: '服务端计费 · 电量', group: '服务端计费',
    hint: '按时段把充入的电量按单价结算，没有功率档位。每个时段只有一个电价。',
  },
  device_duration: {
    label: '设备计费 · 时长', group: '设备计费',
    hint: '费用在支付时已收，设备按获得的时长消耗。服务端不再计算任何金额，模板不带任何费率。',
  },
  device_energy: {
    label: '设备计费 · 电量', group: '设备计费',
    hint: '费用在支付时已收，设备按获得的电量消耗。服务端不再计算任何金额，模板不带任何费率。',
  },
  device_power: {
    label: '设备计费 · 功率档位', group: '设备计费',
    hint: '费用在支付时已收，设备按功率档位决定何时停止。服务端不再计算任何金额，模板不带任何费率。',
  },
};

export const MODE_OPTIONS = [
  { label: '服务端计费（平台按实际用量结算）', options: SERVER_MODES.map(m => ({ value: m, label: MODE_META[m].label.replace('服务端计费 · ', '') })) },
  { label: '设备计费（支付时收款，设备自行执行）', options: DEVICE_MODES.map(m => ({ value: m, label: MODE_META[m].label.replace('设备计费 · ', '') })) },
];

export const isServerBilled = (mode: ChargeMode) => SERVER_MODES.includes(mode);
export const basisOf = (mode: ChargeMode): ServerBasis =>
  mode === 'server_realtime_power' ? 'realtime_power'
    : mode === 'server_max_power' ? 'max_power'
      : mode === 'server_energy' ? 'energy' : (undefined as unknown as ServerBasis);
export const usesLadder = (mode: ChargeMode) => mode === 'server_realtime_power' || mode === 'server_max_power';
export const modeLabel = (mode?: string) => (mode && MODE_META[mode as ChargeMode] ? MODE_META[mode as ChargeMode].label : (mode || '—'));

// 走功率阶梯计价的服务费读的是各档位单价，所以在没有阶梯可读的电价模板上这个选项
// 根本不存在。
export const SERVICE_OPTIONS: { value: ServiceBasis; label: string; unit: string }[] = [
  { value: 'none', label: '不收服务费', unit: '' },
  { value: 'energy', label: '按电量（元/度）', unit: '元/度' },
  { value: 'minute_power', label: '按功率档位（取各档位服务费单价）', unit: '元/小时' },
  { value: 'minute', label: '按充电时长（元/分钟）', unit: '元/分钟' },
  { value: 'session', label: '按场次（元/次）', unit: '元/次' },
];

export const DEFAULT_DISPLAY: Display = {
  show_energy: true, show_power: true, show_tariff: false, show_fee_split: true,
  fee_split_inline: true, show_fee_on_end: true, show_method: true, show_rule: false, hide_unit: false,
};

// 详情页必须用运营的说法讲清楚充电用户会看到什么。直接把字段名印出来等于什么都没说：
// 没人会靠读 "show_tariff" 来决定要不要显示电价。
const DISPLAY_LABELS: Record<keyof Display, string> = {
  show_energy: '显示充电电量',
  show_power: '展示充电功率',
  show_tariff: '显示时段计费详情',
  show_fee_split: '订单详情拆分电费与服务费',
  fee_split_inline: '费用直接跟在支付金额后',
  show_fee_on_end: '结束充电推送显示费用',
  show_method: '显示计费方式',
  show_rule: '展示规则说明',
  hide_unit: '隐藏单位',
};

export const describeDisplay = (display?: Display): string => {
  if (!display) return '—';
  const on = (Object.keys(DISPLAY_LABELS) as (keyof Display)[]).filter(k => display[k]);
  return on.length ? on.map(k => DISPLAY_LABELS[k]).join('、') : '全部关闭';
};

export const yuan = (cents?: number | null) => `¥${((cents || 0) / 100).toFixed(2)}`;
export const toCents = (value?: number | null) => Math.round(Number(value || 0) * 100);
export const fromCents = (cents?: number | null) => (cents || 0) / 100;
export const minuteToClock = (minute: number) => {
  const m = Math.max(0, Math.min(1440, Math.round(minute || 0)));
  return `${String(Math.floor(m / 60)).padStart(2, '0')}:${String(m % 60).padStart(2, '0')}`;
};
export const clockToMinute = (value: string) => {
  const [h, m] = String(value || '0:0').split(':').map(Number);
  return (h || 0) * 60 + (m || 0);
};

export type TierForm = { max_watts: number; electric_yuan: number; service_yuan: number };
export type PeriodForm = { end_minute: number; electric_yuan: number; tiers: TierForm[] };

export type SpecForm = {
  mode: ChargeMode;
  periods: PeriodForm[];
  tier_price_basis: TierPriceBasis;
  service_basis: ServiceBasis;
  service_kwh_yuan: number;
  service_minute_yuan: number;
  service_session_yuan: number;
  multiplier_on: boolean;
  temp_bp: number;
  card_bp: number;
  loss_percent: number;
  free_minutes: number;
  min_electric_yuan: number;
  spend_cap_yuan: number;
  stop_grace_seconds: number;
  card_max_minutes: number;
  default_charge_way: string;
  time_charge: { stop_when_full: boolean; max_minutes: number; float_power_deci_watts: number; float_seconds: number };
};

const tierForm = (tier: Tier): TierForm => ({
  max_watts: tier.max_watts,
  electric_yuan: fromCents(tier.electric_cents),
  service_yuan: fromCents(tier.service_cents),
});

export function blankTier(): TierForm {
  return { max_watts: 0, electric_yuan: 0, service_yuan: 0 };
}

export function blankPeriod(): PeriodForm {
  return {
    end_minute: 1440,
    electric_yuan: 0,
    tiers: [blankTier()],
  };
}

export function defaultSpecForm(mode: ChargeMode): SpecForm {
  return {
    mode,
    periods: [blankPeriod()],
    tier_price_basis: 'per_hour_at_ceiling',
    service_basis: 'none',
    service_kwh_yuan: 0,
    service_minute_yuan: 0,
    service_session_yuan: 0,
    multiplier_on: false,
    temp_bp: 10000,
    card_bp: 10000,
    loss_percent: 0,
    free_minutes: 0,
    min_electric_yuan: 0,
    spend_cap_yuan: 0,
    stop_grace_seconds: 0,
    card_max_minutes: 0,
    default_charge_way: '',
    time_charge: { stop_when_full: true, max_minutes: 0, float_power_deci_watts: 0, float_seconds: 0 },
  };
}

export function specToForm(spec?: Spec): SpecForm {
  const base = defaultSpecForm(spec?.mode || 'server_energy');
  if (!spec) return base;
  const form: SpecForm = { ...base, mode: spec.mode || base.mode };
  if (spec.electric?.periods?.length) {
    form.periods = spec.electric.periods.map(p => ({
      end_minute: p.end_minute,
      electric_yuan: fromCents(p.electric_cents),
      tiers: (p.tiers || []).map(tierForm),
    }));
  } else if (isServerBilled(spec.mode)) {
    form.periods = [blankPeriod()];
  }
  if (spec.tier_price_basis) form.tier_price_basis = spec.tier_price_basis;
  if (spec.service?.basis) form.service_basis = spec.service.basis;
  form.service_kwh_yuan = fromCents(spec.service?.cents_per_kwh);
  form.service_minute_yuan = fromCents(spec.service?.cents_per_minute);
  form.service_session_yuan = fromCents(spec.service?.cents_per_session);
  if (spec.multiplier) {
    form.multiplier_on = true;
    form.temp_bp = spec.multiplier.temp_bp || 10000;
    form.card_bp = spec.multiplier.card_bp || 10000;
  }
  form.loss_percent = (spec.loss_rate_bp || 0) / 100;
  form.free_minutes = spec.free_minutes || 0;
  form.min_electric_yuan = fromCents(spec.min_electric_cents);
  form.spend_cap_yuan = fromCents(spec.spend_cap_cents);
  form.stop_grace_seconds = spec.stop_grace_seconds || 0;
  form.card_max_minutes = spec.card_max_minutes || 0;
  form.default_charge_way = spec.default_charge_way || '';
  if (spec.time_charge) {
    form.time_charge = {
      stop_when_full: !!spec.time_charge.stop_when_full,
      max_minutes: spec.time_charge.max_minutes || 0,
      float_power_deci_watts: spec.time_charge.float_power_deci_watts || 0,
      float_seconds: spec.time_charge.float_seconds || 0,
    };
  }
  return form;
}

export function formToSpec(form: SpecForm): Spec {
  const spec: Spec = { mode: form.mode };
  if (isServerBilled(form.mode)) {
    const basis = basisOf(form.mode);
    spec.electric = {
      basis,
      periods: (form.periods || []).map(p => basis === 'energy'
        ? { end_minute: Number(p.end_minute), electric_cents: toCents(p.electric_yuan) }
        : {
          end_minute: Number(p.end_minute),
          tiers: (p.tiers || []).map(t => ({
            max_watts: Number(t.max_watts),
            electric_cents: toCents(t.electric_yuan),
            ...(form.service_basis === 'minute_power' ? { service_cents: toCents(t.service_yuan) } : {}),
          })),
        }),
    };
    if (form.mode === 'server_realtime_power') spec.tier_price_basis = form.tier_price_basis;
    if (form.service_basis !== 'none') {
      const service: NonNullable<Spec['service']> = { basis: form.service_basis };
      if (form.service_basis === 'energy') service.cents_per_kwh = toCents(form.service_kwh_yuan);
      if (form.service_basis === 'minute') service.cents_per_minute = toCents(form.service_minute_yuan);
      if (form.service_basis === 'session') service.cents_per_session = toCents(form.service_session_yuan);
      spec.service = service;
    }
    if (form.multiplier_on) spec.multiplier = { temp_bp: Number(form.temp_bp) || 0, card_bp: Number(form.card_bp) || 0 };
    spec.loss_rate_bp = Math.round((Number(form.loss_percent) || 0) * 100);
    if (Number(form.free_minutes) > 0) spec.free_minutes = Number(form.free_minutes);
    if (Number(form.min_electric_yuan) > 0) spec.min_electric_cents = toCents(form.min_electric_yuan);
    if (Number(form.spend_cap_yuan) > 0) spec.spend_cap_cents = toCents(form.spend_cap_yuan);
    if (Number(form.stop_grace_seconds) > 0) spec.stop_grace_seconds = Number(form.stop_grace_seconds);
  } else {
    // 设备侧计价的模板根本不带费率：后端会拒绝，而一个没人读的费率比没有费率更糟。
    if (form.mode === 'device_duration') {
      spec.time_charge = {
        stop_when_full: !!form.time_charge.stop_when_full,
        max_minutes: Number(form.time_charge.max_minutes) || 0,
        float_power_deci_watts: Number(form.time_charge.float_power_deci_watts) || 0,
        float_seconds: Number(form.time_charge.float_seconds) || 0,
      };
    }
  }
  if (Number(form.card_max_minutes) > 0) spec.card_max_minutes = Number(form.card_max_minutes);
  if (form.default_charge_way) spec.default_charge_way = form.default_charge_way;
  return spec;
}

// insertPeriod 把一段时段接到 `at` 位置的链条里。链条不允许有缺口，所以新时段必须从
// 相邻那一段里切一块下来，而不能并排放在旁边：追加时把原来的尾巴推到新的边界上、
// 把 1440 交给新时段，插在中间时则把它落进去的那一段劈成两半。
export function insertPeriod(periods: PeriodForm[], at: number): PeriodForm[] {
  const list = (periods || []).map(p => ({ ...p, tiers: [...(p.tiers || [])] }));
  const prevEnd = at > 0 ? Number(list[at - 1].end_minute) : 0;
  const nextEnd = at < list.length ? Number(list[at].end_minute) : 1440;
  const candidate = Math.floor((prevEnd + nextEnd) / 2);
  const end = Math.min(Math.max(candidate, prevEnd + 1), nextEnd - 1);
  const fresh: PeriodForm = { end_minute: at < list.length ? end : 1440, electric_yuan: 0, tiers: [blankTier()] };
  if (at >= list.length) {
    if (list.length > 0) list[list.length - 1].end_minute = end;
    list.push(fresh);
  } else {
    list.splice(at, 0, fresh);
  }
  return list;
}

export function removePeriod(periods: PeriodForm[], at: number): PeriodForm[] {
  const list = (periods || []).map(p => ({ ...p, tiers: [...(p.tiers || [])] }));
  if (list.length <= 1) return list;
  list.splice(at, 1);
  if (list.length > 0) list[list.length - 1].end_minute = 1440;
  return list;
}

export function insertTier(tiers: TierForm[], at: number): TierForm[] {
  const list = [...(tiers || [])];
  const prev = at > 0 ? Number(list[at - 1].max_watts) : -1;
  const next = at < list.length ? Number(list[at].max_watts) : 9990;
  const candidate = Math.floor((prev + next) / 2);
  const ceiling = Math.min(Math.max(candidate, prev + 1), next - 1);
  const fresh: TierForm = { max_watts: at < list.length ? ceiling : 9990, electric_yuan: 0, service_yuan: 0 };
  if (at >= list.length) {
    if (list.length > 0) list[list.length - 1].max_watts = ceiling;
    list.push(fresh);
  } else {
    list.splice(at, 0, fresh);
  }
  return list;
}

export function removeTier(tiers: TierForm[], at: number): TierForm[] {
  const list = [...(tiers || [])];
  if (list.length <= 1) return list;
  list.splice(at, 1);
  return list;
}

const isMoney = (v: unknown) => typeof v === 'number' && Number.isFinite(v) && v >= 0 && v <= 10000;

// validateSpecForm 与后端 ValidateSpec 对齐，并且点名是第几行出的问题。每条提示都
// 说清要改什么，因为服务端唯一的回答是一句笼统的"参数无效"，运营没法从十四个字段里
// 猜它说的是哪一个。
export function validateSpecForm(form: SpecForm): string[] {
  const errors: string[] = [];
  if (!form.mode || !MODE_META[form.mode]) {
    errors.push('请选择计费方式');
    return errors;
  }
  if (isServerBilled(form.mode)) {
    const periods = form.periods || [];
    if (periods.length === 0) errors.push('至少需要一个时段');
    if (periods.length > 48) errors.push(`时段最多 48 段，当前 ${periods.length} 段`);
    let prev = 0;
    periods.forEach((p, i) => {
      const end = Number(p.end_minute);
      const where = `第 ${i + 1} 段`;
      if (!Number.isInteger(end) || end < 1 || end > 1440) {
        errors.push(`${where}结束时刻无效：${end}，应为 1..1440 的分钟数（即 00:01–24:00）`);
      } else if (end <= prev) {
        errors.push(`${where}结束时刻（${minuteToClock(end)}）必须晚于上一段结束时刻（${minuteToClock(prev)}），时段按时间先后排列且不能重叠`);
      } else {
        prev = end;
      }
      if (basisOf(form.mode) === 'energy') {
        if (!isMoney(Number(p.electric_yuan))) errors.push(`${where}电价必须为 0–10000 元之间`);
      } else {
        const tiers = p.tiers || [];
        if (tiers.length === 0) errors.push(`${where}至少需要一个功率档位`);
        if (tiers.length > 8) errors.push(`${where}档位最多 8 条，当前 ${tiers.length} 条`);
        let prevW = -1;
        tiers.forEach((t, j) => {
          const w = Number(t.max_watts);
          const label = `${where}第 ${j + 1} 档`;
          if (!Number.isInteger(w) || w < 0 || w > 9990) errors.push(`${label}上限瓦数无效：${t.max_watts}，应为 0–9990`);
          else if (w <= prevW) errors.push(`${label}上限（${w} 瓦）必须大于上一档上限（${prevW === -1 ? '起点' : `${prevW} 瓦`}）`);
          else prevW = w;
          if (!isMoney(Number(t.electric_yuan))) errors.push(`${label}电费单价必须为 0–10000 元之间`);
          if (form.service_basis === 'minute_power' && !isMoney(Number(t.service_yuan))) errors.push(`${label}服务费单价必须为 0–10000 元之间`);
        });
        if (tiers.length > 0 && Number(tiers[0].max_watts) !== 0) {
          errors.push(`${where}第一档上限必须是 0 瓦：从 0 瓦起，档位下限由上一档上限 +1 推导`);
        }
      }
    });
    if (periods.length > 0) {
      const last = Number(periods[periods.length - 1].end_minute);
      if (last !== 1440) errors.push(`最后一段结束时刻必须是 24:00（1440 分钟），当前为 ${minuteToClock(last)}，否则一天中有时段没有费率`);
    }
    if (form.mode === 'server_realtime_power' && !form.tier_price_basis) errors.push('请选择档位单价的换算口径');
    if (form.service_basis === 'minute_power' && basisOf(form.mode) === 'energy') {
      errors.push('按功率档位收取服务费需要功率档位，当前电量计费没有档位，请改用其它服务费口径');
    }
    if (form.service_basis === 'energy' && !isMoney(Number(form.service_kwh_yuan))) errors.push('服务费单价必须为 0–10000 元之间');
    if (form.service_basis === 'minute' && !isMoney(Number(form.service_minute_yuan))) errors.push('服务费单价必须为 0–10000 元之间');
    if (form.service_basis === 'session' && !isMoney(Number(form.service_session_yuan))) errors.push('服务费单价必须为 0–10000 元之间');
    if (form.multiplier_on) {
      for (const [key, label] of [['temp_bp', '临时费率'], ['card_bp', '刷卡费率']] as const) {
        const bp = Number(form[key]);
        if (!Number.isInteger(bp) || bp < 0 || bp > 100000) errors.push(`${label}基点必须为 0–100000（10000 = 1.0 倍）`);
      }
    }
    if (!(Number(form.loss_percent) >= 0) || Number(form.loss_percent) > 10) errors.push('电损率必须在 0–10% 之间');
    if (!(Number(form.free_minutes) >= 0) || Number(form.free_minutes) > 1440) errors.push('免费时长必须为 0–1440 分钟');
    if (!isMoney(Number(form.min_electric_yuan))) errors.push('电费最低消费必须为 0–10000 元之间');
    if (!isMoney(Number(form.spend_cap_yuan))) errors.push('费用封顶必须为 0–10000 元之间');
    if (!(Number(form.stop_grace_seconds) >= 0) || Number(form.stop_grace_seconds) > 3600) errors.push('停机宽限必须为 0–3600 秒');
  } else if (form.mode === 'device_duration') {
    const tc = form.time_charge;
    if (!(Number(tc.max_minutes) >= 0) || Number(tc.max_minutes) > 999) errors.push('单次时长上限必须为 0–999 分钟');
    if (!(Number(tc.float_seconds) >= 0) || Number(tc.float_seconds) > 10800) errors.push('涓流阶段时长必须为 0–10800 秒');
    if (!(Number(tc.float_power_deci_watts) >= 0) || Number(tc.float_power_deci_watts) > 500) errors.push('涓流功率必须为 0–500（单位 0.1 瓦）');
  }
  if (Number(form.card_max_minutes) < 0 || Number(form.card_max_minutes) > 999) errors.push('刷卡单次时长上限必须为 0–999 分钟');
  return errors;
}

export function describeSpec(spec?: Spec): string {
  if (!spec) return '—';
  const parts: string[] = [MODE_META[spec.mode]?.label || spec.mode];
  const electric = spec.electric;
  if (electric) {
    if (electric.basis === 'energy') {
      parts.push(`${electric.periods.length} 个时段，${yuan(electric.periods[0]?.electric_cents)}/度起`);
    } else {
      const first = electric.periods[0];
      const rungs = first?.tiers?.length || 0;
      const unit = spec.mode === 'server_max_power' || spec.tier_price_basis === 'per_kwh' ? '' : '（元/小时）';
      parts.push(`${electric.periods.length} 个时段 × ${rungs} 档功率${unit}，最高 ${first?.tiers?.[rungs - 1]?.max_watts ?? 0} 瓦`);
    }
  }
  if (spec.service && spec.service.basis !== 'none') {
    parts.push(`服务费：${SERVICE_OPTIONS.find(o => o.value === spec.service!.basis)?.label || spec.service.basis}`);
  }
  return parts.join('，');
}
