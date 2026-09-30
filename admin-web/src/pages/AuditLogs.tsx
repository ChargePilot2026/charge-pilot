import { useCallback, useEffect, useState } from 'react';
import { Table, Typography, Tag, Space, Button, Input, Select, DatePicker, Drawer, Descriptions } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { apiGet } from '../api/client';
import { LoadError } from '../components/LoadError';

const { Title, Text, Paragraph } = Typography;

interface AuditRow {
  id: number;
  actor_id: number;
  actor_name: string;
  module: string;
  action: string;
  target_type: string | null;
  target_id: string | null;
  request_id: string | null;
  client_ip: string | null;
  before_json: string | null;
  after_json: string | null;
  created_at: string;
}

// 审计日志是「谁改过钱、优惠券、设备或权限」唯一的记录。读它和动手改它是两个独立
// 权限。
export default function AuditLogsPage() {
  const [rows, setRows] = useState<AuditRow[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [module, setModule] = useState('');
  const [actor, setActor] = useState('');
  const [range, setRange] = useState<[any, any] | null>(null);
  const [detail, setDetail] = useState<AuditRow | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    const params = new URLSearchParams({ page: String(page), page_size: String(pageSize) });
    if (module) params.set('module', module);
    if (actor) params.set('actor', actor);
    if (range?.[0] && range?.[1]) {
      params.set('from', range[0].toISOString());
      params.set('to', range[1].toISOString());
    }
    try {
      const data = await apiGet<{ items: AuditRow[]; total: number }>(`/api/v1/admin/audit-logs?${params}`);
      setRows(data.items || []);
      setTotal(data.total || 0);
    } catch (e: any) {
      setError(e?.message || '审计日志读取失败');
    } finally {
      setLoading(false);
    }
  }, [page, pageSize, module, actor, range]);

  useEffect(() => { void load(); }, [load]);

  const renderJSON = (raw: string | null) => {
    if (!raw) return '—';
    try { return JSON.stringify(JSON.parse(raw), null, 2); } catch { return raw; }
  };

  return (
    <div className="page-container">
      <Space style={{ marginBottom: 12 }} wrap>
        <Title level={3} style={{ margin: 0 }}>审计日志</Title>
        <Button icon={<ReloadOutlined />} onClick={() => void load()} loading={loading}>刷新</Button>
        <Input aria-label="操作人" placeholder="按操作人筛选" style={{ width: 200 }} maxLength={64}
          value={actor} onChange={e => setActor(e.target.value)}
          onPressEnter={() => { setPage(1); void load(); }} allowClear />
        <Select aria-label="模块" placeholder="全部模块" style={{ width: 160 }} value={module || undefined}
          onChange={v => { setModule(v || ''); setPage(1); }}
          options={['auth', 'coupon', 'coupon_activity', 'station', 'device', 'admin_user', 'refund', 'withdraw',
            'invoice', 'wallet_risk', 'export_task', 'alert_rule', 'ota', 'pricing', 'webhook']
            .map(v => ({ value: v, label: v }))} allowClear />
        <DatePicker.RangePicker onChange={v => { setRange(v as any); setPage(1); }} />
      </Space>
      {error && <LoadError title="审计日志加载失败" detail={error} onRetry={() => void load()} />}
      <Paragraph type="secondary">
        记录只追加不可修改。退款、提现、钱包放款、发票复核等资金操作与账号权限变更均在此留痕，可按操作前后快照核对。
      </Paragraph>
      <Table<AuditRow> rowKey="id" loading={loading} dataSource={rows} scroll={{ x: 1000 }}
        pagination={{ current: page, pageSize, total, showSizeChanger: true, onChange: (p, s) => { setPage(p); setPageSize(s); } }}
        columns={[
          { title: '时间', dataIndex: 'created_at', width: 200, render: (v: string) => new Date(v).toLocaleString() },
          { title: '操作人', dataIndex: 'actor_name', width: 120 },
          { title: '模块', dataIndex: 'module', width: 130, render: (v: string) => <Tag>{v}</Tag> },
          { title: '动作', dataIndex: 'action', width: 120 },
          { title: '对象', key: 'target', width: 200,
            render: (_: unknown, r: AuditRow) => r.target_type ? `${r.target_type}#${r.target_id ?? '—'}` : '—' },
          { title: '来源 IP', dataIndex: 'client_ip', width: 130, render: (v: string | null) => v || '—' },
          { title: '操作', key: 'actions', width: 90, render: (_: unknown, r: AuditRow) =>
            <Button size="small" onClick={() => setDetail(r)}>详情</Button> },
        ]} />
      <Drawer title="审计详情" width={720} open={detail !== null} onClose={() => setDetail(null)}>
        {detail && <Space direction="vertical" style={{ width: '100%' }}>
          <Descriptions size="small" column={2} bordered items={[
            { key: 't', label: '时间', children: new Date(detail.created_at).toLocaleString() },
            { key: 'a', label: '操作人', children: `${detail.actor_name}（#${detail.actor_id}）` },
            { key: 'm', label: '模块', children: detail.module },
            { key: 'ac', label: '动作', children: detail.action },
            { key: 'tt', label: '对象', children: detail.target_type ? `${detail.target_type}#${detail.target_id ?? '—'}` : '—' },
            { key: 'ip', label: '来源 IP', children: detail.client_ip || '—' },
            { key: 'rq', label: '请求 ID', span: 2, children: detail.request_id || '—' },
          ]} />
          <Text strong>操作前</Text>
          <pre style={{ maxHeight: 260, overflow: 'auto', background: '#fafafa', padding: 12, fontSize: 12 }}>
            {renderJSON(detail.before_json)}</pre>
          <Text strong>操作后</Text>
          <pre style={{ maxHeight: 260, overflow: 'auto', background: '#fafafa', padding: 12, fontSize: 12 }}>
            {renderJSON(detail.after_json)}</pre>
        </Space>}
      </Drawer>
    </div>
  );
}
