import { useEffect, useState } from 'react';
import { Button, Card, Col, Row, Space, Spin, Statistic, Typography } from 'antd';
import { ReloadOutlined, ThunderboltOutlined, DollarOutlined, UserOutlined, AlertOutlined, ShoppingCartOutlined, TeamOutlined, UserAddOutlined, EnvironmentOutlined, ToolOutlined } from '@ant-design/icons';
import { apiGet } from '../api/client';
import { LoadError } from '../components/LoadError';

const { Text } = Typography;

interface TrendDay { day: string; completed_orders: number; settled_cents: number; }
interface DashboardMetrics {
  charging_orders: number;
  today_orders: number;
  today_settled_cents: number;
  total_users: number;
  new_users: number;
  today_charging_users: number;
  station_count: number;
  device_count: number;
  active_alerts: number;
  daily_trend: TrendDay[];
  updated_at: string;
}

function TrendChart({ days, kind }: { days: TrendDay[]; kind: 'orders' | 'amount' }) {
  const isAmount = kind === 'amount';
  const title = isAmount ? '结算金额（元）' : '完成订单（单）';
  const color = isAmount ? '#52c41a' : '#1677ff';
  const valueOf = (day: TrendDay) => isAmount ? day.settled_cents : day.completed_orders;
  const format = (value: number) => isAmount ? (value / 100).toFixed(2) : String(value);
  const peak = Math.max(0, ...days.map(valueOf));
  const step = isAmount ? Math.max(25, Math.ceil(peak / 100) * 25) : Math.max(1, Math.ceil(peak / 4));
  const maximum = step * 4;
  const left = 72, top = 32, bottom = 236, width = 512;
  const slot = width / days.length;
  const barWidth = Math.min(40, slot / 2);

  return <section className="dashboard-trend-chart">
    <Text strong>{title}</Text>
    <svg viewBox="0 0 600 276" role="img" aria-label={`近7天${title}`}>
      <title>近7天{title}</title>
      <desc>{days.map(day => `${day.day}：${format(valueOf(day))}${isAmount ? '元' : '单'}`).join('；')}</desc>
      {[0, 1, 2, 3, 4].map(index => {
        const y = bottom - index / 4 * (bottom - top);
        return <g key={index}>
          <line x1={left} x2={left + width} y1={y} y2={y} stroke="#f0f0f0" />
          <text x={left - 10} y={y + 4} textAnchor="end" fill="#8c8c8c" fontSize={12}>{format(step * index)}</text>
        </g>;
      })}
      {days.map((day, index) => {
        const value = valueOf(day);
        const height = value / maximum * (bottom - top);
        const x = left + slot * (index + 0.5);
        return <g key={day.day}>
          <rect x={x - barWidth / 2} y={bottom - height} width={barWidth} height={height} fill={color} rx={3}>
            <title>{day.day}：{format(value)}{isAmount ? '元' : '单'}</title>
          </rect>
          <text x={x} y={bottom - height - 8} textAnchor="middle" fill="#434343" fontSize={13}>{format(value)}</text>
          <text x={x} y={bottom + 24} textAnchor="middle" fill="#8c8c8c" fontSize={12}>{day.day.slice(5)}</text>
        </g>;
      })}
    </svg>
  </section>;
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
  const trend = metrics?.daily_trend || [];
  const updated = metrics?.updated_at ? new Date(metrics.updated_at).toLocaleString() : '';

  return <div className="page-container">
    <Space style={{ marginBottom: 16 }} wrap>
      <Button icon={<ReloadOutlined />} onClick={() => void load()} loading={loading}>刷新</Button>
      {updated && <Text type="secondary">更新时间：{updated}</Text>}
    </Space>
    {error && <LoadError title="仪表盘读取失败" detail={error} onRetry={() => void load()} />}
    {loading && !metrics ? <Card><Spin /></Card> : <>
      <Row gutter={[16, 16]}>
        <Col xs={24} sm={8}><Card><Statistic title="充电中订单" value={metrics ? metrics.charging_orders : '—'} prefix={<ThunderboltOutlined />} suffix="单" /></Card></Col>
        <Col xs={24} sm={8}><Card><Statistic title="本日订单" value={metrics ? metrics.today_orders : '—'} prefix={<ShoppingCartOutlined />} suffix="单" /></Card></Col>
        <Col xs={24} sm={8}><Card><Statistic title="充电金额" value={metrics ? (metrics.today_settled_cents / 100).toFixed(2) : '—'} prefix={<DollarOutlined />} suffix="元" /></Card></Col>
        <Col xs={24} sm={8}><Card><Statistic title="总用户数" value={metrics ? metrics.total_users : '—'} prefix={<TeamOutlined />} suffix="人" /></Card></Col>
        <Col xs={24} sm={8}><Card><Statistic title="新用户数" value={metrics ? metrics.new_users : '—'} prefix={<UserAddOutlined />} suffix="人" /></Card></Col>
        <Col xs={24} sm={8}><Card><Statistic title="本日充电用户数" value={metrics ? metrics.today_charging_users : '—'} prefix={<UserOutlined />} suffix="人" /></Card></Col>
        <Col xs={24} sm={8}><Card><Statistic title="站点数" value={metrics ? metrics.station_count : '—'} prefix={<EnvironmentOutlined />} suffix="个" /></Card></Col>
        <Col xs={24} sm={8}><Card><Statistic title="设备数" value={metrics ? metrics.device_count : '—'} prefix={<ToolOutlined />} suffix="台" /></Card></Col>
        <Col xs={24} sm={8}><Card><Statistic title="待处理告警数" value={metrics ? metrics.active_alerts : '—'} prefix={<AlertOutlined />} suffix="条" valueStyle={{ color: metrics?.active_alerts ? '#cf1322' : undefined }} /></Card></Col>
      </Row>
      <Row gutter={[16, 16]} style={{ marginTop: 8 }}>
        <Col xs={24}>
          <Card title="近 7 天完成订单与结算金额">
            {trend.length > 0 ? <div className="dashboard-trend-grid">
              <TrendChart days={trend} kind="orders" />
              <TrendChart days={trend} kind="amount" />
            </div> : !loading && !error && <Text type="secondary">没有可显示的数据</Text>}
          </Card>
        </Col>
      </Row>
    </>}
  </div>;
}
