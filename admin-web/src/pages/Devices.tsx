import { Table, Typography, Tag, Space, Button } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { useEffect, useState } from 'react';
import { apiGet } from '../api/client';
import DeviceImport from './DeviceImport';

const { Title } = Typography;

interface Device {
  id: number;
  device_id: string;
  station_id?: number;
  vendor_id?: number;
  model?: string;
  status: string;
  install_at?: string;
}

const statusColor: Record<string, string> = {
  enabled: 'green', disabled: 'default', retired: 'red', fault: 'volcano',
};

export default function DevicesPage() {
  const [data, setData] = useState<Device[]>([]);
  const [loading, setLoading] = useState(false);

  const load = async () => {
    setLoading(true);
    try {
      const d = await apiGet<{ items: Device[] }>('/api/v1/admin/devices');
      setData(d.items || []);
    } catch { setData([]); }
    finally { setLoading(false); }
  };

  useEffect(() => { load(); }, []);

  return (
    <div className="page-container">
      <Space style={{ marginBottom: 12 }}>
        <Title level={3} style={{ margin: 0 }}>设备</Title>
        <Button icon={<ReloadOutlined />} onClick={load}>刷新</Button>
        <DeviceImport onComplete={load} />
      </Space>
      <Table
        rowKey="id"
        loading={loading}
        dataSource={data}
        pagination={{ pageSize: 30 }}
        columns={[
          { title: '设备 ID', dataIndex: 'device_id', width: 200 },
          { title: '型号', dataIndex: 'model', width: 160 },
          { title: '站点', dataIndex: 'station_id', width: 100 },
          { title: '厂商', dataIndex: 'vendor_id', width: 100 },
          { title: '状态', dataIndex: 'status',
            render: (s: string) => <Tag color={statusColor[s] || 'default'}>{s}</Tag> },
          { title: '安装时间', dataIndex: 'install_at', render: (v?: string) => v || '-' },
        ]}
      />
    </div>
  );
}
