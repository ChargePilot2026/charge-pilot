import { Card, Descriptions, Space, Table, Tabs, Tag, Typography } from 'antd';
import { type PackageMode, type Scheme, clock, displayLabels, modes, money } from './model';

const algorithms = {
  server_max_power: '最大功率：按各时段的峰值功率档位计费',
  server_realtime_power: '实时功率：按实际功率片段对应的档位计费',
  server_energy: '实际电量：按各时段的实际用电量计费',
};

export default function SchemeDetails({ scheme }: { scheme: Scheme }) {
  const packages = (mode: PackageMode) => <Card size="small" title={`${modes[mode]}套餐`}>
    <Table size="middle" rowKey="id" pagination={false} dataSource={scheme.packages.filter(p => p.mode === mode)} scroll={{ x: 480 }} columns={[
      { title: '套餐', dataIndex: 'name' },
      { title: mode === 'amount' ? '充电预算' : '支付价格', render: (_, p) => money(p.price_cents) },
      ...(mode === 'duration' ? [{ title: '购买时长', render: (_: unknown, p: Scheme['packages'][number]) => `${p.minutes} 分钟` }]
        : mode === 'energy' ? [{ title: '购买电量', render: (_: unknown, p: Scheme['packages'][number]) => `${p.kwh} 度` }] : []),
    ]} />
  </Card>;
  const amount = scheme.amount;
  const energy = scheme.energy;
  const cardPackage = scheme.packages.find(p => p.id === scheme.card.package_id && p.mode === 'duration');

  return <>
    <Typography.Title level={5}>{scheme.name}</Typography.Title>
    {scheme.remark && <Typography.Paragraph type="secondary">{scheme.remark}</Typography.Paragraph>}
    <Tabs items={[
      ...(amount ? [{ key: 'amount', label: '金额模式', children: <Space direction="vertical" size="middle" style={{ width: '100%' }}>
        <Typography.Text strong>{algorithms[amount.algorithm]}</Typography.Text>
        {amount.periods.map((period, index) => <Card key={index} size="small" title={`${clock(index ? amount.periods[index - 1].end_minute : 0)}～${clock(period.end_minute)}`}>
          {amount.algorithm === 'server_energy' ? <Descriptions size="small" column={{ xs: 1, sm: 2 }} items={[
            { key: 'electric', label: '电费', children: `${money(period.electric_cents!)} / 度` },
            { key: 'service', label: '服务费', children: `${money(period.service_cents!)} / 度` },
          ]} /> : <Table size="middle" pagination={false} rowKey="tierIndex" dataSource={period.tiers?.map((tier, tierIndex) => ({ ...tier, tierIndex }))} scroll={{ x: 480 }} columns={[
            { title: '功率范围（含上下限）', render: (_, tier) => `${tier.tierIndex ? period.tiers![tier.tierIndex - 1].max_watts + 1 : 0}～${tier.max_watts} W` },
            { title: '电费', render: (_, tier) => `${money(tier.electric_cents)} / 小时` },
            { title: '服务费', render: (_, tier) => `${money(tier.service_cents)} / 小时` },
          ]} />}
        </Card>)}
        <Card size="small" title="金额充电策略">
          <Descriptions size="small" column={{ xs: 1, sm: 2 }} items={[
            { key: 'free', label: '免费时长', children: scheme.policy.free_minutes ? `${scheme.policy.free_minutes} 分钟` : '不启用' },
            { key: 'minimum', label: '最低电费', children: scheme.policy.min_electric_cents ? money(scheme.policy.min_electric_cents) : '不设下限' },
            { key: 'maximum', label: '最长时长', children: `${scheme.policy.max_minutes / 60} 小时` },
          ]} />
          <Typography.Paragraph type="secondary" style={{ marginTop: 8, marginBottom: 0 }}>支付金额作为预算，预算耗尽或达到最长时长后停止。免费时长内电费和服务费均免收，超过后整段计费；最低电费不含服务费。</Typography.Paragraph>
        </Card>
        {packages('amount')}
      </Space> }] : []),
      ...(energy ? [{ key: 'energy', label: '电量模式', children: <Space direction="vertical" size="middle" style={{ width: '100%' }}>
        <Card size="small" title="设备电量 · 全天固定单价"><Descriptions size="small" column={{ xs: 1, sm: 2 }} items={[
          { key: 'electric', label: '电费', children: `${money(energy.electric_cents)} / 度` },
          { key: 'service', label: '服务费', children: `${money(energy.service_cents)} / 度` },
          { key: 'total', label: '合计单价', children: `${money(energy.electric_cents + energy.service_cents)} / 度` },
        ]} /></Card>
        <Typography.Text type="secondary">购买整数度电，套餐价格按电费与服务费单价计算；实际用量按计量精度结算。</Typography.Text>
        {packages('energy')}
      </Space> }] : []),
      ...(scheme.packages.some(p => p.mode === 'duration') ? [{ key: 'duration', label: '时长模式', children: <Space direction="vertical" size="middle" style={{ width: '100%' }}>
        {packages('duration')}
        <Card size="small" title="在线刷卡"><Descriptions size="small" column={{ xs: 1, sm: 2 }} items={[
          { key: 'package', label: '指定套餐', children: cardPackage ? `${cardPackage.name} · ${money(cardPackage.price_cents)} / ${cardPackage.minutes} 分钟` : '未启用在线刷卡' },
          ...(cardPackage ? [{ key: 'limit', label: '累计时长上限', children: `${scheme.card.max_minutes / 60} 小时` }] : []),
        ]} />{cardPackage && <Typography.Paragraph type="secondary" style={{ marginTop: 8, marginBottom: 0 }}>每次刷卡购买一份指定套餐，由服务器增加可充时长，累计时长不超过上限。</Typography.Paragraph>}</Card>
      </Space> }] : []),
      { key: 'general', label: '通用配置', children: <Space direction="vertical" size="middle" style={{ width: '100%' }}>
        <Card size="small" title="充电停止规则"><Descriptions size="small" items={[{ key: 'full', label: '满充停止', children: <Tag color={scheme.stop.stop_when_full ? 'green' : 'default'}>{scheme.stop.stop_when_full ? '启用' : '关闭'}</Tag> }]} /></Card>
        <Card size="small" title="用户界面展示"><Descriptions size="small" column={{ xs: 1, sm: 2 }} items={Object.entries(displayLabels).map(([key, label]) => ({ key, label, children: <Tag color={scheme.display[key] ? 'green' : 'default'}>{scheme.display[key] ? '启用' : '关闭'}</Tag> }))} /></Card>
      </Space> },
    ]} />
  </>;
}
