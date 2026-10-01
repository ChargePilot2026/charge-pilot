import { Alert, Button, Form, Input, Modal, Select, Space, Table, Tag, Typography, message } from 'antd';
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { useEffect, useRef, useState } from 'react';
import { apiGet, apiPost, apiPut } from '../api/client';
import { listVendors, vendorProtocolLabel, type Vendor } from '../api/vendors';
import { LoadError } from '../components/LoadError';
import { DEFAULT_PAGE_SIZE, TABLE_PAGINATION } from '../utils/tablePagination';

type VendorForm = Pick<Vendor, 'vendor_code' | 'vendor_name' | 'adapter_class' | 'status'>;
const protocolOptions = [{ value: 'dc589', label: 'DC589' }];
const statusOptions = [{ value: 'enabled', label: '已启用' }, { value: 'disabled', label: '已停用' }];
const errorMessage = (cause: unknown) => cause instanceof Error ? cause.message : '厂商保存失败，请稍后重试';

export default function VendorsPage() {
  const [data, setData] = useState<Vendor[]>([]);
  const [total, setTotal] = useState(0);
  const [permissions, setPermissions] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [keyword, setKeyword] = useState('');
  const [status, setStatus] = useState('');
  const [query, setQuery] = useState({ page: 1, page_size: DEFAULT_PAGE_SIZE, keyword: '', status: '' });
  const generation = useRef(0);

  const [form] = Form.useForm<VendorForm>();
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<Vendor | null>(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');
  const [pendingID, setPendingID] = useState<number | null>(null);
  const submitting = useRef(false);
  const canCreate = permissions.includes('vendor.create');
  const canUpdate = permissions.includes('vendor.update');
  const legacyProtocol = editing && (editing.adapter_class !== 'dc589' || editing.protocol !== 'tcp') ? editing : null;

  const load = async () => {
    const current = ++generation.current;
    setLoading(true); setError(''); setPermissions([]);
    try {
      const result = await listVendors(query);
      if (current !== generation.current) return;
      setData(result.items || []); setTotal(result.total); setPermissions(result.permissions || []);
    } catch (cause: unknown) {
      if (current === generation.current) {
        setData([]); setTotal(0); setError(errorMessage(cause));
      }
    } finally {
      if (current === generation.current) setLoading(false);
    }
  };
  useEffect(() => { void load(); return () => { generation.current++; }; }, [query]);

  const showEditor = async (vendor?: Vendor) => {
    if (pendingID !== null) return;
    let detail = vendor || null;
    if (vendor) {
      setPendingID(vendor.id);
      try { detail = await apiGet<Vendor>(`/api/v1/admin/vendors/${vendor.id}`); }
      catch (cause: unknown) { message.error(errorMessage(cause)); return; }
      finally { setPendingID(null); }
    }
    setEditing(detail); setSaveError(''); form.resetFields();
    form.setFieldsValue(detail ? { vendor_code: detail.vendor_code, vendor_name: detail.vendor_name, adapter_class: detail.adapter_class, status: detail.status } : { status: 'enabled' });
    setOpen(true);
  };

  const save = async () => {
    if (submitting.current) return;
    submitting.current = true;
    try {
      const values = await form.validateFields();
      const body = {
        vendor_code: editing?.vendor_code || values.vendor_code.trim(),
        vendor_name: values.vendor_name.trim(),
        adapter_class: values.adapter_class,
        protocol: editing?.protocol || 'tcp', status: values.status,
      };
      setSaving(true); setSaveError('');
      if (editing) await apiPut(`/api/v1/admin/vendors/${editing.id}`, body);
      else await apiPost('/api/v1/admin/vendors', body);
      message.success(editing ? '厂商已保存' : '厂商已创建');
      setOpen(false); form.resetFields();
      if (editing) await load(); else setQuery(previous => ({ ...previous, page: 1 }));
    } catch (cause: unknown) {
      if (!(cause && typeof cause === 'object' && 'errorFields' in cause)) setSaveError(errorMessage(cause));
    } finally { submitting.current = false; setSaving(false); }
  };

  const changeStatus = async (vendor: Vendor, nextStatus: Vendor['status']) => {
    setPendingID(vendor.id);
    try {
      // 先读取最新配置，再只改变状态，避免覆盖已更新的厂商信息。
      const detail = await apiGet<Vendor>(`/api/v1/admin/vendors/${vendor.id}`);
      await apiPut(`/api/v1/admin/vendors/${vendor.id}`, {
        vendor_code: detail.vendor_code, vendor_name: detail.vendor_name,
        adapter_class: detail.adapter_class, protocol: detail.protocol, status: nextStatus,
      });
      message.success(nextStatus === 'enabled' ? '厂商已启用' : '厂商已停用');
      await load();
    } catch (cause: unknown) { message.error(errorMessage(cause)); }
    finally { setPendingID(null); }
  };

  return <div className="page-container">
    <Space wrap style={{ marginBottom: 12 }}>
      <Button icon={<ReloadOutlined />} onClick={() => void load()}>刷新</Button>
      {canCreate && <Button type="primary" icon={<PlusOutlined />} disabled={pendingID !== null} onClick={() => void showEditor()}>新建</Button>}
    </Space>
    <Space wrap style={{ display: 'flex', marginBottom: 12 }}>
      <Input aria-label="厂商关键词" placeholder="搜索厂商编码或名称" maxLength={128} style={{ width: 320 }} allowClear
        value={keyword} onChange={event => setKeyword(event.target.value)} onPressEnter={() => setQuery({ ...query, page: 1, keyword, status })} />
      <Select aria-label="厂商状态" value={status} onChange={setStatus} style={{ width: 140 }} options={[{ value: '', label: '全部状态' }, ...statusOptions]} />
      <Button onClick={() => setQuery({ ...query, page: 1, keyword, status })}>查询</Button>
      <Button onClick={() => { setKeyword(''); setStatus(''); setQuery({ ...query, page: 1, keyword: '', status: '' }); }}>重置</Button>
    </Space>
    {error && <LoadError title="厂商列表加载失败" detail={error} onRetry={() => void load()} />}
    <Table<Vendor> size="middle" rowKey="id" loading={loading} dataSource={data} scroll={{ x: 700 }}
      pagination={{ ...TABLE_PAGINATION, current: query.page, pageSize: query.page_size, total, showTotal: count => `共 ${count} 个厂商`,
        onChange: (page, page_size) => setQuery({ ...query, page: page_size === query.page_size ? page : 1, page_size }) }}
      columns={[
        { title: '厂商 ID', dataIndex: 'id', width: 120, render: (id: number) => <Typography.Text copyable={{ text: String(id), tooltips: ['复制厂商 ID', '已复制'] }}>{id}</Typography.Text> },
        { title: '厂商编码', dataIndex: 'vendor_code' },
        { title: '厂商名称', dataIndex: 'vendor_name' },
        { title: '设备协议', render: (_: unknown, vendor: Vendor) => vendorProtocolLabel(vendor) },
        { title: '状态', dataIndex: 'status', render: (value: string) => <Tag color={value === 'enabled' ? 'green' : 'default'}>{value === 'enabled' ? '已启用' : '已停用'}</Tag> },
        { title: '操作', render: (_: unknown, vendor: Vendor) => canUpdate && <Space>
          <Button type="link" disabled={pendingID !== null} onClick={() => void showEditor(vendor)}>编辑</Button>
          <Button type="link" danger={vendor.status === 'enabled'} loading={pendingID === vendor.id} disabled={pendingID !== null && pendingID !== vendor.id}
            onClick={() => {
              if (vendor.status === 'disabled') { void changeStatus(vendor, 'enabled'); return; }
              Modal.confirm({ title: `停用厂商「${vendor.vendor_name}」？`, content: '停用后，该厂商的设备将无法注册和新增。', okText: '停用', cancelText: '取消', okButtonProps: { danger: true },
                onOk: () => changeStatus(vendor, 'disabled') });
            }}>{vendor.status === 'enabled' ? '停用' : '启用'}</Button>
        </Space> },
      ]} />
    <Modal title={editing ? '编辑厂商' : '新建厂商'} open={open} confirmLoading={saving} okText="保存" cancelText="取消"
      closable={!saving} maskClosable={!saving} keyboard={!saving} onCancel={() => { if (!saving) setOpen(false); }} onOk={() => void save()}>
      {saveError && <Alert type="error" showIcon message="厂商保存失败" description={saveError} style={{ marginBottom: 16 }} />}
      <Form form={form} name="vendor_editor" layout="vertical" disabled={saving} onFinish={() => void save()}>
        <Form.Item name="vendor_code" label="厂商编码" normalize={(value: string) => value.trim()}
          extra={editing ? '厂商编码创建后不可修改。' : '1–64 位字母、数字、下划线或短横线。'}
          rules={[{ required: true, message: '请填写厂商编码' }, { pattern: /^[A-Za-z0-9_-]{1,64}$/, message: '厂商编码须为 1–64 位字母、数字、下划线或短横线' }]}>
          <Input readOnly={!!editing} maxLength={64} placeholder="如：vendor_a" />
        </Form.Item>
        <Form.Item name="vendor_name" label="厂商名称" rules={[{ required: true, whitespace: true, message: '请填写厂商名称' }, {
          validator: (_: unknown, value: string | undefined) => !value || [...value.trim()].length <= 128 ? Promise.resolve() : Promise.reject(new Error('厂商名称最多 128 个字符')),
        }]}>
          <Input maxLength={256} placeholder="厂商名称" />
        </Form.Item>
        <Form.Item name="adapter_class" label="设备协议" rules={[{ required: true, message: '请选择设备协议' }]}
          extra={legacyProtocol ? '保留该厂商现有的设备协议。' : '当前支持 DC589 协议，使用 TCP 连接。'}>
          <Select aria-label="设备协议" placeholder="请选择设备协议" allowClear={!legacyProtocol} disabled={saving || !!legacyProtocol}
            options={legacyProtocol ? [{ value: legacyProtocol.adapter_class, label: vendorProtocolLabel(legacyProtocol) }] : protocolOptions} />
        </Form.Item>
        <Form.Item name="status" label="状态" rules={[{ required: true, message: '请选择厂商状态' }]}>
          <Select options={statusOptions} />
        </Form.Item>
      </Form>
    </Modal>
  </div>;
}
