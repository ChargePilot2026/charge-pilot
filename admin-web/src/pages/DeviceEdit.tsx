import { Alert, Button, DatePicker, Descriptions, Form, Input, Modal, Select, Spin, message } from 'antd';
import axios from 'axios';
import dayjs, { type Dayjs } from 'dayjs';
import { useEffect, useRef, useState } from 'react';
import { adminSession, apiGet, apiPut } from '../api/client';
import { LoadError } from '../components/LoadError';

type DeviceDetails = {
  device_id: string;
  station_id?: number;
  station_name?: string;
  vendor_id?: number;
  vendor_name?: string;
  protocol_adapter: string;
  runtime_available?: boolean;
  model?: string | null;
  serial_no?: string | null;
  install_at?: string | null;
  warranty_until?: string | null;
  tags: string[];
  updated_at: string;
};
type DeviceForm = {
  model?: string;
  serial_no?: string;
  install_at?: Dayjs | null;
  warranty_until?: Dayjs | null;
  tags: string[];
};

const errorMessage = (cause: unknown, fallback: string) => cause instanceof Error ? cause.message : fallback;

export default function DeviceEdit({ deviceID, onClose, onComplete }: {
  deviceID: string;
  onClose: () => void;
  onComplete: () => void;
}) {
  const [form] = Form.useForm<DeviceForm>();
  const [device, setDevice] = useState<DeviceDetails>();
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState('');
  const [saveError, setSaveError] = useState('');
  const [conflict, setConflict] = useState(false);
  const [saving, setSaving] = useState(false);
  const generation = useRef(0);
  const submitting = useRef(false);
  const session = useRef(adminSession.epoch());
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  const load = async () => {
    const current = ++generation.current;
    const requestSession = session.current;
    setLoading(true); setLoadError(''); setSaveError(''); setConflict(false); setDevice(undefined);
    form.resetFields();
    try {
      const details = await apiGet<DeviceDetails>('/api/v1/admin/devices/' + encodeURIComponent(deviceID));
      if (current !== generation.current || requestSession !== adminSession.epoch()) return;
      if (!details.updated_at || !dayjs(details.updated_at).isValid() || !Array.isArray(details.tags)
        || details.tags.some(tag => typeof tag !== 'string')
        || (details.install_at && !dayjs(details.install_at).isValid())
        || (details.warranty_until && !dayjs(details.warranty_until).isValid())) {
        throw new Error('设备资料格式不完整，请重新加载后重试');
      }
      form.setFieldsValue({
        model: details.model || '', serial_no: details.serial_no || '',
        install_at: details.install_at ? dayjs(details.install_at) : null,
        warranty_until: details.warranty_until ? dayjs(details.warranty_until) : null,
        tags: details.tags,
      });
      setDevice(details);
    } catch (cause: unknown) {
      if (current === generation.current && requestSession === adminSession.epoch()) {
        setLoadError(errorMessage(cause, '设备资料读取失败'));
      }
    } finally {
      if (current === generation.current && requestSession === adminSession.epoch()) setLoading(false);
    }
  };

  useEffect(() => {
    void load();
    const closeExpiredEditor = () => {
      if (session.current !== adminSession.epoch()) {
        generation.current++;
        onCloseRef.current();
      }
    };
    window.addEventListener('cp-session', closeExpiredEditor);
    return () => {
      generation.current++;
      window.removeEventListener('cp-session', closeExpiredEditor);
    };
  }, [deviceID]);

  const save = async () => {
    if (submitting.current || loading || !device || conflict) return;
    const current = generation.current;
    const requestSession = session.current;
    submitting.current = true;
    setSaving(true); setSaveError('');
    try {
      const values = await form.validateFields();
      if (current !== generation.current || requestSession !== adminSession.epoch()) return;
      await apiPut<DeviceDetails>('/api/v1/admin/devices/' + encodeURIComponent(deviceID), {
        model: values.model?.trim() || null,
        serial_no: values.serial_no?.trim() || null,
        install_at: values.install_at?.toISOString() || null,
        warranty_until: values.warranty_until?.toISOString() || null,
        tags: values.tags.map(tag => tag.trim()),
        expected_updated_at: device.updated_at,
      });
      if (current !== generation.current || requestSession !== adminSession.epoch()) return;
      message.success('设备信息已保存');
      onClose();
      onComplete();
    } catch (cause: unknown) {
      if (current !== generation.current || requestSession !== adminSession.epoch()) return;
      if (cause && typeof cause === 'object' && 'errorFields' in cause) return;
      if (axios.isAxiosError(cause) && cause.response?.status === 409) {
        setConflict(true);
        setSaveError('设备信息已被修改，请重新加载最新信息后再编辑保存。重新加载会替换当前输入。');
      } else {
        setSaveError(errorMessage(cause, '设备信息保存失败，请稍后重试'));
      }
    } finally {
      submitting.current = false;
      if (current === generation.current && requestSession === adminSession.epoch()) setSaving(false);
    }
  };

  const textLength = (label: string) => ({ validator: (_: unknown, value?: string) =>
    !value || Array.from(value.trim()).length <= 128
      ? Promise.resolve() : Promise.reject(new Error(`${label}最多 128 个字符`)) });
  const dateRange = { validator: (_: unknown, value?: Dayjs | null) => {
    if (!value) return Promise.resolve();
    const utcYear = value.toDate().getUTCFullYear();
    return value.isValid() && utcYear >= 1000 && utcYear <= 9999
      ? Promise.resolve() : Promise.reject(new Error('请选择 1000–9999 年范围内的有效时间'));
  } };

  return <Modal title="编辑设备信息" open centered width={560} styles={{ body: { maxHeight: 'calc(100vh - 180px)', overflowY: 'auto' } }}
    confirmLoading={saving} okText="保存" cancelText="取消"
    okButtonProps={{ disabled: loading || !device || conflict }} onOk={() => void save()}
    onCancel={() => { if (!submitting.current) onClose(); }} closable={!saving} maskClosable={!saving} keyboard={!saving}>
    {loading && <div style={{ textAlign: 'center', padding: 32 }}><Spin tip="正在读取设备资料" /></div>}
    {loadError && <LoadError title="设备资料加载失败" detail={loadError} onRetry={() => void load()} />}
    {saveError && <Alert type="error" showIcon message={conflict ? '设备信息已更新' : '设备信息保存失败'} description={saveError}
      action={conflict && <Button size="small" disabled={saving} onClick={() => void load()}>重新加载</Button>} style={{ marginBottom: 16 }} />}
    {device &&
      <Descriptions size="small" column={1} style={{ marginBottom: 16 }} items={[
        { key: 'device_id', label: '设备编号', children: device.device_id },
        { key: 'station', label: '所属站点', children: device.station_name || (device.station_id ? `站点 #${device.station_id}` : '未分配') },
        { key: 'vendor', label: '厂商', children: device.runtime_available === false ? '暂不可读取' : device.vendor_name || '未登记' },
        { key: 'protocol', label: '通信协议', children: device.protocol_adapter === 'dc589' ? 'DC589' : device.protocol_adapter || '未登记' },
      ]} />}
    <Form form={form} name="device_edit" layout="vertical" disabled={saving || conflict || loading || !device}
      style={{ display: device ? undefined : 'none' }} onFinish={() => void save()}>
      <Form.Item name="model" label="型号" rules={[textLength('型号')]}>
        <Input maxLength={256} placeholder="设备型号（选填）" />
      </Form.Item>
      <Form.Item name="serial_no" label="序列号" rules={[textLength('序列号')]}>
        <Input maxLength={256} placeholder="设备序列号（选填）" />
      </Form.Item>
      <Form.Item name="install_at" label="安装时间" rules={[dateRange]}>
        <DatePicker showTime format="YYYY-MM-DD HH:mm:ss" placeholder="选择安装时间（选填）" style={{ width: '100%' }} />
      </Form.Item>
      <Form.Item name="warranty_until" label="质保到期时间" dependencies={['install_at']} rules={[dateRange, {
        validator: (_: unknown, value?: Dayjs | null) => {
          const installed = form.getFieldValue('install_at');
          return !value || !installed || !value.isBefore(installed)
            ? Promise.resolve() : Promise.reject(new Error('质保到期时间不能早于安装时间'));
        },
      }]}>
        <DatePicker showTime format="YYYY-MM-DD HH:mm:ss" placeholder="选择质保到期时间（选填）" style={{ width: '100%' }} />
      </Form.Item>
      <Form.Item name="tags" label="标签" extra="最多 20 项，每项最多 32 个字符。" rules={[{
        validator: (_: unknown, values: string[] = []) => {
          const tags = values.map(tag => tag.trim());
          if (tags.length > 20) return Promise.reject(new Error('最多添加 20 个标签'));
          if (tags.some(tag => !tag || Array.from(tag).length > 32)) return Promise.reject(new Error('标签须为 1–32 个字符'));
          if (new Set(tags).size !== tags.length) return Promise.reject(new Error('标签不能重复'));
          return Promise.resolve();
        },
      }]}>
        <Select mode="tags" maxCount={20} maxTagCount="responsive" placeholder="输入标签后按回车" allowClear />
      </Form.Item>
    </Form>
  </Modal>;
}
