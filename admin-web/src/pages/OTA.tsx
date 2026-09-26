import { useEffect, useState } from 'react';
import { Tabs, Table, Typography, Space, Button, Tag } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { apiGet } from '../api/client';

const { Title } = Typography;

export default function OTAPage() {
  const [packages, setPackages] = useState<any[]>([]);
  const [schedules, setSchedules] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);

  const load = async () => {
    setLoading(true);
    try {
      const p = await apiGet<{ items: any[] }>('/api/v1/admin/ota/packages');
      const s = await apiGet<{ items: any[] }>('/api/v1/admin/ota/schedules');
      setPackages(p.items || []);
      setSchedules(s.items || []);
    } catch { setPackages([]); setSchedules([]); }
    finally { setLoading(false); }
  };

  useEffect(() => { load(); }, []);

  return (
    <div className="page-container">
      <Space style={{ marginBottom: 12 }}>
        <Title level={3} style={{ margin: 0 }}>OTA 升级</Title>
        <Button icon={<ReloadOutlined />} onClick={load}>刷新</Button>
      </Space>
      <Tabs
        items={[
          {
            key: 'packages', label: '固件包',
            children: (
              <Table rowKey="id" loading={loading} dataSource={packages}
                columns={[
                  { title: '编码', dataIndex: 'code' },
                  { title: '版本', dataIndex: 'version' },
                  { title: '大小(字节)', dataIndex: 'size_bytes' },
                  { title: '状态', dataIndex: 'status',
                    render: (s: string) => <Tag color={s === 'published' ? 'green' : 'default'}>{s}</Tag> },
                ]}
              />
            ),
          },
          {
            key: 'schedules', label: '推送计划',
            children: (
              <Table rowKey="id" loading={loading} dataSource={schedules}
                columns={[
                  { title: '固件包 ID', dataIndex: 'package_id', width: 120 },
                  { title: '策略', dataIndex: 'rollout_strategy' },
                  { title: '状态', dataIndex: 'status' },
                ]}
              />
            ),
          },
        ]}
      />
    </div>
  );
}