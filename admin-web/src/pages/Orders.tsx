import { Table, Typography, Tag, Space, Button } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { useEffect, useState } from 'react';
import { apiGet } from '../api/client';

const { Title } = Typography;

interface Order {
  order_id?: number;
  order_no: string;
  status: string;
  total_cents?: number;
  started_at?: string;
  ended_at?: string;
}

const statusColor: Record<string, string> = {
  pending_payment: 'gold', paid: 'blue', charging: 'green',
  completed: 'cyan', cancelled: 'default', failed: 'red',
  refunding: 'orange', refunded: 'purple',
};

export default function OrdersPage() {
  const [data, setData] = useState<Order[]>([]);
  const [loading, setLoading] = useState(false);
  const [page, setPage] = useState(1);

  const load = async () => {
    setLoading(true);
    try {
      const d = await apiGet<{ items: Order[] }>('/api/v1/admin/orders');
      setData(d.items || []);
    } catch (e) {
      setData([]);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { load(); }, [page]);

  return (
    <div className="page-container">
      <Space style={{ marginBottom: 12 }}>
        <Title level={3} style={{ margin: 0 }}>订单</Title>
        <Button icon={<ReloadOutlined />} onClick={load}>刷新</Button>
      </Space>
      <Table
        rowKey="order_no"
        loading={loading}
        dataSource={data}
        pagination={{ current: page, onChange: setPage, pageSize: 20 }}
        columns={[
          { title: '订单号', dataIndex: 'order_no', width: 220 },
          { title: '状态', dataIndex: 'status', width: 120,
            render: (s: string) => <Tag color={statusColor[s] || 'default'}>{s}</Tag> },
          { title: '金额(分)', dataIndex: 'total_cents', width: 120 },
          { title: '开始时间', dataIndex: 'started_at', render: (v?: string) => v || '-' },
          { title: '结束时间', dataIndex: 'ended_at', render: (v?: string) => v || '-' },
        ]}
      />
    </div>
  );
}