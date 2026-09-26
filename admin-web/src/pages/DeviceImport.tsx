import { Alert, Button, Modal, Space, Table, Typography } from 'antd';
import { useState } from 'react';
import axios from 'axios';
import { apiGet, apiPost } from '../api/client';

import { parseDeviceCsv as parseCsv, type ImportedDevice as Device } from '../utils/deviceCsv';
interface Job { import_id: string; status: string; last_error: string | null }
function message(error: unknown): string { return axios.isAxiosError(error) ? error.response?.data?.message || '请求失败，请刷新导入记录后重试' : error instanceof Error ? error.message : '导入失败'; }

export default function DeviceImport({ onComplete }: { onComplete: () => void }) {
  const [open, setOpen] = useState(false), [busy, setBusy] = useState(false);
  const [devices, setDevices] = useState<Device[]>([]), [jobs, setJobs] = useState<Job[]>([]);
  const [error, setError] = useState(''), [id, setId] = useState('');
  const refresh = async () => setJobs(await apiGet<Job[]>('/api/v1/admin/device-imports'));
  const perform = async (retry?: string) => {
    setBusy(true); setError('');
    try {
      const job = await apiPost<Job>(retry ? `/api/v1/admin/device-imports/${retry}/retry` : '/api/v1/admin/device-imports', retry ? undefined : { import_id: id, devices });
      if (job.status === 'completed') { setDevices([]); onComplete(); }
      else setError(job.last_error || '同步尚未完成，可在记录中重试');
      await refresh();
    } catch (e) { setError(message(e)); } finally { setBusy(false); }
  };
  return <>
    <Button onClick={() => { setOpen(true); refresh().catch(e => setError(message(e))); }}>导入设备</Button>
    <Modal title="批量导入设备" open={open} width={900} footer={null} onCancel={() => { if (!busy) setOpen(false); }}>
      <Space direction="vertical" style={{ width: '100%' }}>
        <Typography.Paragraph>选择 UTF-8 CSV 文件，每批最多 100 台。站点和启用的厂商须已存在；重复导入相同配置不会新增设备。</Typography.Paragraph>
        <a download="devices.csv" href={'data:text/csv;charset=utf-8,' + encodeURIComponent('device_id,vendor_id,station_id,port_count,model\nDEVICE_001,1,1,2,\n')}>下载 CSV 模板</a>
        <input aria-label="选择设备 CSV" type="file" accept=".csv" disabled={busy} onChange={async event => {
          const file = event.target.files?.[0]; event.target.value = ''; setDevices([]); setError(''); if (!file) return;
          try { if (file.size > 256 * 1024) throw new Error('文件不能超过 256 KB'); setDevices(parseCsv(await file.text())); setId(crypto.randomUUID()); }
          catch (e) { setError(message(e)); }
        }} />
        {error && <Alert type="error" showIcon message={error} />}
        <Table rowKey="device_id" size="small" dataSource={devices} pagination={{ pageSize: 5 }} columns={[
          { title: '设备 ID', dataIndex: 'device_id' }, { title: '厂商 ID', dataIndex: 'vendor_id' },
          { title: '站点 ID', dataIndex: 'station_id' }, { title: '端口数', dataIndex: 'port_count' }, { title: '型号', dataIndex: 'model' },
        ]} />
        <Button type="primary" disabled={!devices.length} loading={busy} onClick={() => perform()}>确认导入 {devices.length} 台</Button>
        <Button disabled={busy} onClick={() => refresh().catch(e => setError(message(e)))}>刷新导入记录</Button>
        <Table rowKey="import_id" size="small" dataSource={jobs} pagination={{ pageSize: 5 }} columns={[
          { title: '批次', dataIndex: 'import_id' }, { title: '状态', dataIndex: 'status', render: value => ({ completed: '已完成', failed: '失败', pending: '待同步' }[value as string] || value) },
          { title: '错误', dataIndex: 'last_error' }, { title: '操作', render: (_, job) => job.status !== 'completed' && <Button disabled={busy} onClick={() => perform(job.import_id)}>重试</Button> },
        ]} />
      </Space>
    </Modal>
  </>;
}
