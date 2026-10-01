import { TABLE_PAGINATION } from '../../utils/tablePagination';
import { Alert, Card, Descriptions, Empty, Space, Table, Tag, Typography } from 'antd';
import { type PackageMode, type Scheme, clock, modes, money } from '../schemes/model';

export interface SelectedPackage {
  offer: { name: string; mode: PackageMode; price_cents: number; duration_minutes: number; energy_wh?: number; max_minutes?: number };
  scheme?: Scheme;
  billing_mode: string;
  rule_version: number;
  card?: { paid_cents: number; purchased_minutes: number; max_minutes: number };
}

const algorithms: Record<string, string> = {
  server_max_power: '最大功率：按各时段的峰值功率档位计费',
  server_realtime_power: '实时功率：按实际功率片段对应的档位计费',
  server_energy: '实际电量：按各时段的实际用电量计费',
  device_duration: '按购买时长充电',
  device_energy: '按购买电量充电',
};

export default function OrderPackageDetails({ value }: { value: SelectedPackage | null }) {
  if (!value) return <Empty description="此订单没有可用的所选套餐快照" />;
  const { offer, scheme, card } = value;
  const amount = offer.mode === 'amount' ? scheme?.amount : undefined;
  const energy = offer.mode === 'energy' ? scheme?.energy : undefined;
  return <Space direction="vertical" size="middle" style={{ width: '100%' }}>
    <Alert type="info" showIcon message={offer.mode === 'amount'
      ? '支付金额作为充电预算，预算耗尽或达到最长时长停止。免费时长内结束不计费，超出后整段计费；最低电费不含服务费。'
      : offer.mode === 'duration' ? '按实际完整分钟结算，提前结束按未使用时长退还余额。'
        : '按实际计量电量结算，提前结束退还未使用电量对应的金额。'} />
    <Descriptions title="用户所选套餐" bordered column={{ xs: 1, sm: 2, md: 2, lg: 2, xl: 2, xxl: 2 }} items={[
      { key: 'name', label: '套餐名称', children: offer.name, span: 2 },
      { key: 'mode', label: '充电模式', children: modes[offer.mode] || offer.mode },
      { key: 'price', label: card ? '每次刷卡价格' : offer.mode === 'amount' ? '充电预算' : '套餐价格', children: money(offer.price_cents) },
      ...(offer.mode === 'duration' ? [{ key: 'minutes', label: card ? '每次购买时长' : '购买时长', children: `${offer.duration_minutes} 分钟`, span: 2 }] : []),
      ...(offer.mode === 'energy' ? [{ key: 'energy', label: '购买电量', children: `${(offer.energy_wh || 0) / 1000} 度`, span: 2 }] : []),
      { key: 'scheme', label: '方案名称', children: scheme?.name || '未记录' },
      { key: 'version', label: '方案版本', children: value.rule_version || '未记录' },
    ]} />
    {scheme?.remark && <Typography.Paragraph>{scheme.remark}</Typography.Paragraph>}
    {card && <Descriptions title="刷卡累计购买" bordered column={{ xs: 1, sm: 2, md: 2, lg: 2, xl: 2, xxl: 2 }} items={[
      { key: 'paid', label: '累计支付', children: money(card.paid_cents) },
      { key: 'minutes', label: '累计购买时长', children: `${card.purchased_minutes} 分钟` },
      { key: 'max', label: '累计时长上限', children: `${card.max_minutes} 分钟`, span: 2 },
    ]} />}
    <Typography.Text strong>{algorithms[value.billing_mode] || value.billing_mode}</Typography.Text>
    {amount?.periods.map((period, index) => <Card key={index} size="small" title={`${clock(index ? amount.periods[index - 1].end_minute : 0)}～${clock(period.end_minute)}`}>
      {amount.algorithm === 'server_energy' ? <Descriptions column={{ xs: 1, sm: 2, md: 2, lg: 2, xl: 2, xxl: 2 }} items={[
        { key: 'electric', label: '电费', children: `${money(period.electric_cents ?? 0)} / 度` },
        { key: 'service', label: '服务费', children: `${money(period.service_cents ?? 0)} / 度` },
      ]} /> : <Table size="middle" pagination={TABLE_PAGINATION} rowKey="tierIndex" dataSource={period.tiers?.map((tier, tierIndex) => ({ ...tier, tierIndex }))} scroll={{ x: 480 }} columns={[
        { title: '功率范围（含上下限）', render: (_, tier) => `${tier.tierIndex ? period.tiers![tier.tierIndex - 1].max_watts + 1 : 0}～${tier.max_watts} W` },
        { title: '电费', render: (_, tier) => `${money(tier.electric_cents)} / 小时` },
        { title: '服务费', render: (_, tier) => `${money(tier.service_cents ?? 0)} / 小时` },
      ]} />}
    </Card>)}
    {energy && <Descriptions title="全天固定单价" bordered column={{ xs: 1, sm: 2, md: 2, lg: 2, xl: 2, xxl: 2 }} items={[
      { key: 'electric', label: '电费', children: `${money(energy.electric_cents)} / 度` },
      { key: 'service', label: '服务费', children: `${money(energy.service_cents)} / 度` },
    ]} />}
    {offer.mode === 'amount' && <Descriptions title="金额充电策略" bordered column={{ xs: 1, sm: 2, md: 2, lg: 2, xl: 2, xxl: 2 }} items={[
      { key: 'maximum', label: '最长时长', children: scheme?.policy.max_minutes || offer.max_minutes ? `${scheme?.policy.max_minutes || offer.max_minutes} 分钟` : '未记录' },
      { key: 'free', label: '免费时长', children: scheme ? scheme.policy.free_minutes ? `${scheme.policy.free_minutes} 分钟` : '不启用' : '未记录' },
      { key: 'minimum', label: '最低电费', children: scheme ? scheme.policy.min_electric_cents ? money(scheme.policy.min_electric_cents) : '不设下限' : '未记录' },
    ]} />}
    {scheme && <Descriptions bordered column={1} items={[
      { key: 'full', label: '满充停止', children: <Tag style={scheme.stop.stop_when_full ? { color: '#389e0d' } : undefined}>{scheme.stop.stop_when_full ? '启用' : '关闭'}</Tag> },
    ]} />}
  </Space>;
}
