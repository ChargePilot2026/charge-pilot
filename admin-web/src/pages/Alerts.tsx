import { TABLE_PAGINATION } from '../utils/tablePagination';
import { useEffect, useState } from 'react';
import { Table, Tag, Space, Button, App } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { apiGet, apiPost } from '../api/client';
import { formatTime } from '../utils/time';
import { LoadError } from '../components/LoadError';

interface Alert { id: number; device_id: string; severity: string; metric: string; status: string; created_at?: string; }

const sevColor: Record<string, string> = {
  warning: 'gold', critical: 'orange', fatal: 'red',
};
const severityLabel: Record<string, string> = { warning: '警告', critical: '严重', fatal: '致命' };
const statusLabel: Record<string, string> = { active: '待处理', acknowledged: '已确认', resolved: '已恢复', auto_resolved: '自动恢复' };
const metricLabel: Record<string, string> = {
  smoke: '烟雾告警', high_temperature: '高温告警', device_fault: '设备故障',
  voltage_v: '电压', current_a: '电流', temperature_c: '温度', battery_soc: '电池电量', power_w: '功率', meter_kwh: '累计电量',
};
const displayMetric = (metric: string) => metricLabel[metric] || (metric.startsWith('port_fault_') ? `端口 ${metric.slice('port_fault_'.length)} 故障` : metric);

export default function AlertsPage() {
  const [data, setData] = useState<Alert[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const { message } = App.useApp();

  // 这个页面原来把读失败直接吞掉（catch { setData([]) }），于是一屏空白：运维
  // 看到空表格只会得出「设备都没告警」的结论，实际上是接口挂了。这里必须把
  // 故障摆在屏幕上。
  const load = async () => {
    setLoading(true);
    try {
      setData((await apiGet<{ items: Alert[] }>('/api/v1/admin/alerts')).items || []);
      setLoadError(null);
    } catch (e: any) {
      setData([]); setLoadError(e?.message || '告警列表读取失败');
    } finally { setLoading(false); }
  };

  useEffect(() => { load(); }, []);

  const onAck = async (id: number) => {
    try {
      await apiPost(`/api/v1/admin/alerts/${id}/ack`);
      message.success('已确认告警');
      load();
    } catch (e: any) {
      message.error(e?.message || '失败');
    }
  };

  return (
    <div className="page-container">
      <Space style={{ marginBottom: 12 }}>
        <Button icon={<ReloadOutlined />} onClick={load}>刷新</Button>
      </Space>
      {loadError && <LoadError title="告警列表加载失败" detail={loadError} onRetry={load} />}
      <Table pagination={TABLE_PAGINATION} size="middle"
        rowKey="id"
        loading={loading}
        dataSource={data}
        columns={[
          { title: '设备', dataIndex: 'device_id', width: 200 },
          { title: '严重度', dataIndex: 'severity', width: 100,
            render: (s: string) => <Tag color={sevColor[s] || 'default'}>{severityLabel[s] || s}</Tag> },
          { title: '指标', dataIndex: 'metric', width: 140, render: displayMetric },
          { title: '状态', dataIndex: 'status', width: 120, render: (value: string) => statusLabel[value] || value },
          { title: '时间', dataIndex: 'created_at', width: 180, render: formatTime },
          { title: '操作', width: 120,
            render: (_: unknown, r: Alert) => r.status === 'active' ? (
              <Button size="small" onClick={() => onAck(r.id)}>确认</Button>
            ) : <span style={{ color: '#999' }}>已处理</span> },
        ]}
      />
    </div>
  );
}
