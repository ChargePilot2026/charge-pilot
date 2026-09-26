import { useEffect, useState } from 'react';
import { Table, Tag, Typography, Space, Button, App } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { apiGet, apiPost } from '../api/client';

const { Title } = Typography;

interface Alert { id: number; device_id: string; severity: string; metric: string; status: string; created_at?: string; }

const sevColor: Record<string, string> = {
  warning: 'gold', critical: 'orange', fatal: 'red',
};

export default function AlertsPage() {
  const [data, setData] = useState<Alert[]>([]);
  const [loading, setLoading] = useState(false);
  const { message } = App.useApp();

  const load = async () => {
    setLoading(true);
    try { setData((await apiGet<{ items: Alert[] }>('/api/v1/admin/alerts')).items || []); }
    catch { setData([]); } finally { setLoading(false); }
  };

  useEffect(() => { load(); }, []);

  const onAck = async (id: number) => {
    try {
      await apiPost(`/api/v1/admin/alerts/${id}/ack`);
      message.success('已 ACK');
      load();
    } catch (e: any) {
      message.error(e?.message || '失败');
    }
  };

  return (
    <div className="page-container">
      <Space style={{ marginBottom: 12 }}>
        <Title level={3} style={{ margin: 0 }}>告警</Title>
        <Button icon={<ReloadOutlined />} onClick={load}>刷新</Button>
      </Space>
      <Table
        rowKey="id"
        loading={loading}
        dataSource={data}
        columns={[
          { title: '设备', dataIndex: 'device_id', width: 200 },
          { title: '严重度', dataIndex: 'severity', width: 100,
            render: (s: string) => <Tag color={sevColor[s] || 'default'}>{s}</Tag> },
          { title: '指标', dataIndex: 'metric', width: 140 },
          { title: '状态', dataIndex: 'status', width: 120 },
          { title: '时间', dataIndex: 'created_at', render: (v?: string) => v || '-' },
          { title: '操作', width: 120,
            render: (_: unknown, r: Alert) => r.status === 'active' ? (
              <Button size="small" onClick={() => onAck(r.id)}>确认</Button>
            ) : <span style={{ color: '#999' }}>已处理</span> },
        ]}
      />
    </div>
  );
}