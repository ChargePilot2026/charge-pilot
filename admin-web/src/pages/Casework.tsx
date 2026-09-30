import { useEffect, useState } from 'react';
import { Button, Form, Image, Input, Modal, Select, Space, Table, Tabs, Tag, Typography, message } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { apiGet, apiPost } from '../api/client';
import { formatTime } from '../utils/time';
import { LoadError } from '../components/LoadError';

const { Title, Text } = Typography;
const feedbackEndpoint = '/api/v1/admin/feedback';
const faultsEndpoint = '/api/v1/admin/device-fault-reports';

interface Feedback {
  id: string;
  user_id: string;
  order_id: string | null;
  rating: number | null;
  category: string;
  content: string | null;
  images: string[];
  status: 'pending' | 'processed' | 'closed';
  reply_content: string | null;
  created_at: string;
}

interface Fault {
  id: string;
  device_id: string;
  user_id: string | null;
  report_source: string;
  fault_type: string;
  description: string | null;
  images: string[];
  status: 'open' | 'dispatched' | 'fixed' | 'closed';
  assigned_to: string | null;
  created_at: string;
}

interface FaultEvent {
  event_id: string;
  event_type: string;
  actor_id?: string | null;
  assigned_to?: string | null;
  from_status: string | null;
  to_status: string | null;
  note: string | null;
  created_at: string;
  user_visible?: boolean;
}

interface AdminUser { id: number; username: string; display_name: string | null; status: string; }
interface ListResult<T> { items: T[]; total: number; page: number; page_size: number; }

const feedbackStatus: Record<string, string> = { pending: '待处理', processed: '已回复', closed: '已关闭' };
const faultStatus: Record<string, string> = { open: '待派单', dispatched: '处理中', fixed: '已修复', closed: '已关闭' };
const faultType: Record<string, string> = { mechanical: '机械故障', electrical: '电气故障', communication: '通讯故障', display: '显示故障', other: '其他' };
const category: Record<string, string> = { rating: '评价', complaint: '投诉', suggestion: '建议' };
const faultEventLabel: Record<string, string> = { reported: '用户提交报修', dispatched: '首次派单', reassigned: '改派', fixed: '标记已修复', closed: '关闭报修', migration_baseline: '迁移时状态快照' };

function imageLinks(urls: string[]) {
  return <Space wrap>{urls.map((url) => <Image key={url} src={url} width={48} height={48} style={{ objectFit: 'cover' }} />)}</Space>;
}

export default function CaseworkPage() {
  const [feedback, setFeedback] = useState<Feedback[]>([]);
  const [faults, setFaults] = useState<Fault[]>([]);
  const [feedbackStatusFilter, setFeedbackStatusFilter] = useState<string>();
  const [faultStatusFilter, setFaultStatusFilter] = useState<string>();
  const [feedbackPage, setFeedbackPage] = useState(1);
  const [faultPage, setFaultPage] = useState(1);
  const [feedbackTotal, setFeedbackTotal] = useState(0);
  const [faultTotal, setFaultTotal] = useState(0);
  const [feedbackLoading, setFeedbackLoading] = useState(false);
  const [faultLoading, setFaultLoading] = useState(false);
  const [replying, setReplying] = useState<Feedback | null>(null);
  const [assigning, setAssigning] = useState<Fault | null>(null);
  const [resolving, setResolving] = useState<{ fault: Fault; status: 'fixed' | 'closed' } | null>(null);
  const [historyFault, setHistoryFault] = useState<Fault | null>(null);
  const [history, setHistory] = useState<FaultEvent[]>([]);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [admins, setAdmins] = useState<AdminUser[]>([]);
  const [feedbackError, setFeedbackError] = useState<string | null>(null);
  const [faultError, setFaultError] = useState<string | null>(null);
  const [adminsError, setAdminsError] = useState<string | null>(null);
  const [historyError, setHistoryError] = useState<string | null>(null);
  const [form] = Form.useForm();
  const [dispatchForm] = Form.useForm();
  const [resolveForm] = Form.useForm();
  const [saving, setSaving] = useState(false);
  let currentAdminId = '';
  try { currentAdminId = String(JSON.parse(localStorage.getItem('cp_admin') || 'null')?.admin_user_id || ''); } catch { currentAdminId = ''; }

  const loadFeedback = async () => {
    setFeedbackLoading(true);
    try {
      const result = await apiGet<ListResult<Feedback>>(feedbackEndpoint, { page: feedbackPage, page_size: 50, ...(feedbackStatusFilter ? { status: feedbackStatusFilter } : {}) });
      setFeedback(Array.isArray(result?.items) ? result.items : []);
      setFeedbackTotal(result?.total || 0);
      setFeedbackError(null);
    } catch (error: any) { setFeedback([]); setFeedbackTotal(0); setFeedbackError(error?.message || '反馈队列读取失败'); }
    finally { setFeedbackLoading(false); }
  };

  const loadFaults = async () => {
    setFaultLoading(true);
    try {
      const result = await apiGet<ListResult<Fault>>(faultsEndpoint, { page: faultPage, page_size: 50, ...(faultStatusFilter ? { status: faultStatusFilter } : {}) });
      setFaults(Array.isArray(result?.items) ? result.items : []);
      setFaultTotal(result?.total || 0);
      setFaultError(null);
    } catch (error: any) { setFaults([]); setFaultTotal(0); setFaultError(error?.message || '报修队列读取失败'); }
    finally { setFaultLoading(false); }
  };

  useEffect(() => { void loadFeedback(); }, [feedbackStatusFilter, feedbackPage]);
  useEffect(() => { void loadFaults(); }, [faultStatusFilter, faultPage]);

  const submitReply = async () => {
    if (!replying || saving) return;
    try {
      const values = await form.validateFields();
      setSaving(true);
      await apiPost(`${feedbackEndpoint}/${replying.id}/reply`, { action: 'reply', reply_content: values.reply_content.trim() });
      message.success('回复已保存');
      setReplying(null);
      await loadFeedback();
    } catch (error: any) { if (!error?.errorFields) message.error(error?.message || '回复失败'); }
    finally { setSaving(false); }
  };

  const closeFeedback = (item: Feedback) => Modal.confirm({
    title: '关闭反馈', content: `关闭用户 ${item.user_id} 的这条反馈？`, okText: '关闭',
    onOk: async () => {
      await apiPost(`${feedbackEndpoint}/${item.id}/reply`, { action: 'close' });
      message.success('反馈已关闭'); await loadFeedback();
    },
  });

  // 指派下拉的候选来自管理员列表，和报修数据不是一个接口，所以单独一个错误态：
  // 读不到人时如果只把下拉留空，客服会以为「没有可指派的人」而直接放弃派单。
  const loadAdmins = async () => {
    try {
      const result = await apiGet<{ items: AdminUser[] }>('/api/v1/admin/users');
      setAdmins((result.items || []).filter((user) => user.status === 'active'));
      setAdminsError(null);
    } catch (error: any) {
      setAdmins([]); setAdminsError(error?.message || '巡检人员列表读取失败');
    }
  };

  const openDispatch = async (item: Fault) => {
    setAssigning(item);
    dispatchForm.resetFields();
    dispatchForm.setFieldsValue({ assigned_to: item.assigned_to ? Number(item.assigned_to) : undefined });
    await loadAdmins();
  };

  const submitDispatch = async () => {
    if (!assigning || saving) return;
    try {
      const values = await dispatchForm.validateFields();
      setSaving(true);
      await apiPost(`${faultsEndpoint}/${assigning.id}/dispatch`, { assigned_to: values.assigned_to, note: values.note?.trim() || null });
      message.success('报修已派单'); setAssigning(null); await loadFaults();
    } catch (error: any) { if (!error?.errorFields) message.error(error?.message || '派单失败'); }
    finally { setSaving(false); }
  };

  const resolveFault = (fault: Fault, status: 'fixed' | 'closed') => {
    resolveForm.resetFields();
    setResolving({ fault, status });
  };

  const submitResolution = async () => {
    if (!resolving || saving) return;
    try {
      const values = await resolveForm.validateFields();
      setSaving(true);
      await apiPost(`${faultsEndpoint}/${resolving.fault.id}/resolve`, { status: resolving.status, note: values.note?.trim() || null });
      message.success(resolving.status === 'fixed' ? '报修已标记修复' : '报修已关闭');
      setResolving(null); await loadFaults();
    } catch (error: any) { if (!error?.errorFields) message.error(error?.message || '处理失败'); }
    finally { setSaving(false); }
  };

  const openFaultHistory = async (fault: Fault) => {
    setHistoryFault(fault); setHistory([]); setHistoryError(null); setHistoryLoading(true);
    try {
      const result = await apiGet<ListResult<FaultEvent>>(`${faultsEndpoint}/${fault.id}/history`, { page: 1, page_size: 100 });
      setHistory(Array.isArray(result?.items) ? result.items : []);
    } catch (error: any) { setHistoryError(error?.message || '处理记录读取失败'); }
    finally { setHistoryLoading(false); }
  };

  const feedbackColumns = [
    { title: '提交时间', dataIndex: 'created_at', width: 190, render: formatTime },
    { title: '用户 / 订单', render: (_: unknown, row: Feedback) => <><div>用户 {row.user_id}</div><Text type="secondary">订单 {row.order_id || '—'}</Text></> },
    { title: '类型 / 评分', render: (_: unknown, row: Feedback) => <>{category[row.category] || row.category}{row.rating ? ` · ${row.rating} 星` : ''}</> },
    { title: '内容', dataIndex: 'content', render: (content: string | null, row: Feedback) => <><div style={{ maxWidth: 300, whiteSpace: 'pre-wrap' }}>{content || '—'}</div>{row.images?.length ? imageLinks(row.images) : null}{row.reply_content && <Text type="secondary">客服回复：{row.reply_content}</Text>}</> },
    { title: '状态', dataIndex: 'status', width: 100, render: (status: string) => <Tag color={status === 'pending' ? 'orange' : status === 'processed' ? 'blue' : 'default'}>{feedbackStatus[status] || status}</Tag> },
    { title: '操作', width: 160, render: (_: unknown, row: Feedback) => <Space>{row.status === 'pending' && <Button type="link" onClick={() => { setReplying(row); form.resetFields(); }}>回复</Button>}{row.status !== 'closed' && <Button type="link" danger onClick={() => closeFeedback(row)}>关闭</Button>}</Space> },
  ];

  const faultColumns = [
    { title: '提交时间', dataIndex: 'created_at', width: 190, render: formatTime },
    { title: '设备 / 用户', render: (_: unknown, row: Fault) => <><div>{row.device_id}</div><Text type="secondary">用户 {row.user_id || '巡检/监控'}</Text></> },
    { title: '故障', render: (_: unknown, row: Fault) => <><div>{faultType[row.fault_type] || row.fault_type}</div><Text type="secondary">{row.report_source}</Text></> },
    { title: '说明 / 图片', dataIndex: 'description', render: (content: string | null, row: Fault) => <><div style={{ maxWidth: 300, whiteSpace: 'pre-wrap' }}>{content || '—'}</div>{row.images?.length ? imageLinks(row.images) : null}</> },
    { title: '指派人员', dataIndex: 'assigned_to', width: 120, render: (id: string | null) => admins.find((user) => String(user.id) === id)?.display_name || admins.find((user) => String(user.id) === id)?.username || id || '未指派' },
    { title: '状态', dataIndex: 'status', width: 100, render: (status: string) => <Tag color={status === 'open' ? 'orange' : status === 'dispatched' ? 'blue' : status === 'fixed' ? 'green' : 'default'}>{faultStatus[status] || status}</Tag> },
    { title: '操作', width: 240, render: (_: unknown, row: Fault) => <Space wrap><Button type="link" onClick={() => void openFaultHistory(row)}>处理记录</Button>{['open', 'dispatched'].includes(row.status) && <Button type="link" onClick={() => void openDispatch(row)}>{row.assigned_to ? '改派' : '派单'}</Button>}{row.status === 'dispatched' && row.assigned_to === currentAdminId && <Button type="link" onClick={() => resolveFault(row, 'fixed')}>标记修复</Button>}{row.status === 'fixed' && row.assigned_to === currentAdminId && <Button type="link" danger onClick={() => resolveFault(row, 'closed')}>关闭</Button>}</Space> },
  ];

  return <div className="page-container">
    <Space style={{ marginBottom: 12 }}><Title level={3} style={{ margin: 0 }}>反馈与报修</Title></Space>
    <Tabs items={[
      { key: 'feedback', label: '评价与投诉', children: <><Space style={{ marginBottom: 12 }}><Select allowClear placeholder="全部状态" style={{ width: 150 }} value={feedbackStatusFilter} onChange={(value) => { setFeedbackPage(1); setFeedbackStatusFilter(value); }} options={Object.entries(feedbackStatus).map(([value, label]) => ({ value, label }))} /><Button icon={<ReloadOutlined />} onClick={() => void loadFeedback()} loading={feedbackLoading}>刷新</Button></Space>{feedbackError && <LoadError title="评价与投诉加载失败" detail={feedbackError} onRetry={() => void loadFeedback()} />}<Table rowKey="id" loading={feedbackLoading} dataSource={feedback} columns={feedbackColumns} scroll={{ x: 1100 }} pagination={{ current: feedbackPage, pageSize: 50, total: feedbackTotal, onChange: setFeedbackPage, showTotal: (total) => `共 ${total} 条` }} /></> },
      { key: 'faults', label: '设备报修', children: <><Space style={{ marginBottom: 12 }}><Select allowClear placeholder="全部状态" style={{ width: 150 }} value={faultStatusFilter} onChange={(value) => { setFaultPage(1); setFaultStatusFilter(value); }} options={Object.entries(faultStatus).map(([value, label]) => ({ value, label }))} /><Button icon={<ReloadOutlined />} onClick={() => void loadFaults()} loading={faultLoading}>刷新</Button></Space>{faultError && <LoadError title="设备报修加载失败" detail={faultError} onRetry={() => void loadFaults()} />}<Table rowKey="id" loading={faultLoading} dataSource={faults} columns={faultColumns} scroll={{ x: 1200 }} pagination={{ current: faultPage, pageSize: 50, total: faultTotal, onChange: setFaultPage, showTotal: (total) => `共 ${total} 条` }} /></> },
    ]} />
    <Modal title="回复用户反馈" open={!!replying} onCancel={() => setReplying(null)} onOk={() => void submitReply()} confirmLoading={saving} destroyOnHidden>
      {replying && <><Text type="secondary">用户 {replying.user_id} · {category[replying.category] || replying.category}</Text><p style={{ whiteSpace: 'pre-wrap' }}>{replying.content || '未填写文字'}</p>{replying.images?.length ? imageLinks(replying.images) : null}<Form form={form} layout="vertical" style={{ marginTop: 16 }}><Form.Item name="reply_content" label="回复内容" rules={[{ required: true, whitespace: true, max: 2000 }]}><Input.TextArea rows={5} maxLength={2000} showCount /></Form.Item></Form></>}
    </Modal>
    <Modal title="指派巡检人员" open={!!assigning} onCancel={() => setAssigning(null)} onOk={() => void submitDispatch()} confirmLoading={saving} destroyOnHidden>
      {assigning && <><Text type="secondary">设备 {assigning.device_id} · {faultType[assigning.fault_type] || assigning.fault_type}</Text><Form form={dispatchForm} layout="vertical" style={{ marginTop: 16 }}>{adminsError && <LoadError title="巡检人员列表加载失败" detail={adminsError} onRetry={() => void loadAdmins()} />}<Form.Item name="assigned_to" label="指派账号" rules={[{ required: true }]}><Select showSearch optionFilterProp="label" options={admins.map((user) => ({ value: user.id, label: `${user.display_name || user.username} · ${user.username}` }))} /></Form.Item><Form.Item name="note" label="处理备注（报修人可见）" rules={[{ max: 2000 }]}><Input.TextArea rows={3} maxLength={2000} showCount /></Form.Item></Form></>}
    </Modal>
    <Modal title={resolving?.status === 'fixed' ? '记录现场修复' : '关闭报修'} open={!!resolving} onCancel={() => setResolving(null)} onOk={() => void submitResolution()} confirmLoading={saving} destroyOnHidden>
      {resolving && <><Text type="secondary">设备 {resolving.fault.device_id} · 处理备注会显示给报修人</Text><Form form={resolveForm} layout="vertical" style={{ marginTop: 16 }}><Form.Item name="note" label={resolving.status === 'fixed' ? '修复说明' : '关闭说明'} rules={[{ required: resolving.status === 'fixed', whitespace: true, max: 2000 }]}><Input.TextArea rows={4} maxLength={2000} showCount /></Form.Item></Form></>}
    </Modal>
    <Modal title={`报修 ${historyFault?.id || ''} 的处理记录`} open={!!historyFault} onCancel={() => setHistoryFault(null)} footer={null} width={720}>
      {historyError ? (
        <LoadError title="处理记录加载失败" detail={historyError}
          onRetry={() => { if (historyFault) void openFaultHistory(historyFault); }} />
      ) : historyLoading ? <Typography.Text>正在读取…</Typography.Text> : history.length ? <Space direction="vertical" style={{ width: '100%' }}>{history.map((event) => <div key={event.event_id} style={{ borderLeft: '2px solid #d9d9d9', padding: '2px 0 12px 12px' }}><Typography.Text strong>{faultEventLabel[event.event_type] || event.event_type}</Typography.Text><div><Text type="secondary">{new Date(event.created_at).toLocaleString()} · 状态 {faultStatus[event.from_status || ''] || event.from_status || '—'} → {faultStatus[event.to_status || ''] || event.to_status || '—'}</Text></div><div><Text type="secondary">操作人 {event.actor_id || '—'} · 指派 {event.assigned_to || '—'}</Text></div>{event.note && <div style={{ whiteSpace: 'pre-wrap', marginTop: 4 }}>{event.note}</div>}</div>)}</Space> : <Typography.Text type="secondary">暂无处理记录</Typography.Text>}
    </Modal>
  </div>;
}
