import { useEffect, useState } from 'react';
import { Alert, Button, Card, Col, Progress, Row, Space, Spin, Statistic, Table, Typography } from 'antd';
import { ReloadOutlined, ThunderboltOutlined, DollarOutlined, UserOutlined, AlertOutlined } from '@ant-design/icons';
import { apiGet } from '../api/client';

const { Title, Paragraph, Text } = Typography;

interface TrendDay { day: string; completed_orders: number; settled_cents: number; }
interface DashboardMetrics {
  charging_orders: number;
  today_order_users: number;
  today_completed_orders: number;
  today_settled_cents: number;
  active_alerts: number;
  daily_trend: TrendDay[];
  updated_at: string;
}

export default function DashboardPage() {
  const [metrics, setMetrics] = useState<DashboardMetrics | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const load = async () => {
    setLoading(true);
    setError('');
    try { setMetrics(await apiGet<DashboardMetrics>('/api/v1/admin/dashboard')); }
    catch (failure: any) { setMetrics(null); setError(failure?.message || '仪表盘数据读取失败'); }
    finally { setLoading(false); }
  };

  useEffect(() => { void load(); }, []);
  const peakCents = Math.max(1, ...(metrics?.daily_trend || []).map((item) => item.settled_cents));
  const updated = metrics?.updated_at ? new Date(metrics.updated_at).toLocaleString() : '';

  return <div className="page-container">
    <Space style={{ marginBottom: 8 }}>
      <Title level={3} style={{ margin: 0 }}>仪表盘</Title>
      <Button icon={<ReloadOutlined />} onClick={() => void load()} loading={loading}>刷新</Button>
    </Space>
    <Paragraph type="secondary">
      充电中订单和结算指标来自用户订单数据；结算金额按已结束订单汇总，未扣除后续退款。{updated ? ` 更新于 ${updated}` : ''}
    </Paragraph>
    {error && <Alert style={{ marginBottom: 16 }} type="error" showIcon message="仪表盘读取失败" description={error} action={<Button size="small" onClick={() => void load()}>重试</Button>} />}
    {loading && !metrics ? <Card><Spin /></Card> : <>
      <Row gutter={[16, 16]}>
        <Col xs={24} sm={12} xl={6}><Card><Statistic title="充电中订单" value={metrics ? metrics.charging_orders : '—'} prefix={<ThunderboltOutlined />} suffix="单" /></Card></Col>
        <Col xs={24} sm={12} xl={6}><Card><Statistic title="今日充电结算金额" value={metrics ? (metrics.today_settled_cents / 100).toFixed(2) : '—'} prefix={<DollarOutlined />} suffix="元" /></Card></Col>
        <Col xs={24} sm={12} xl={6}><Card><Statistic title="今日下单用户" value={metrics ? metrics.today_order_users : '—'} prefix={<UserOutlined />} suffix="人" /></Card></Col>
        <Col xs={24} sm={12} xl={6}><Card><Statistic title="待处理告警" value={metrics ? metrics.active_alerts : '—'} prefix={<AlertOutlined />} suffix="条" valueStyle={{ color: metrics?.active_alerts ? '#cf1322' : undefined }} /></Card></Col>
      </Row>
      <Row gutter={[16, 16]} style={{ marginTop: 8 }}>
        <Col xs={24}>
          <Card title="近 7 天完成订单与结算金额">
            <Table rowKey="day" size="small" pagination={false} dataSource={metrics?.daily_trend || []} columns={[
              { title: '日期', dataIndex: 'day', width: 150, render: (day: string) => day.slice(5) },
              { title: '完成订单', dataIndex: 'completed_orders', width: 160 },
              { title: '结算金额', dataIndex: 'settled_cents', width: 180, render: (cents: number) => `¥${(cents / 100).toFixed(2)}` },
              { title: '金额走势', dataIndex: 'settled_cents', render: (cents: number) => <Progress percent={Math.round((cents / peakCents) * 100)} showInfo={false} /> },
            ]} />
            {!metrics && !loading && !error && <Text type="secondary">没有可显示的数据</Text>}
          </Card>
        </Col>
      </Row>
    </>}
  </div>;
}
