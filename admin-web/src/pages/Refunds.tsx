import { useEffect, useState } from 'react';
import { Alert, Button, Input, Modal, Select, Table } from 'antd';
import { apiGet, apiPost } from '../api/client';

interface Refund {
  id: string; refund_no: string; user_id: string; payment_order_id: string;
  refund_cents: number; status: string; reason: string | null;
  failure_reason: string | null; created_at: string; completed_at: string | null;
  can_retry: boolean; task: null | { stage: string; attempts: number; last_error: string | null; scheduled_at: string };
  can_approve: boolean; can_reject: boolean; review?: { status: string; first_signer: string; second_signer: string | null; first_comment: string; second_comment: string | null };
}
const labels: Record<string, string> = { pending: '待处理', processing: '处理中', success: '已退款', failed: '退款异常', rejected: '审核已拒绝' };
export default function Refunds() {
  const [query, setQuery] = useState({ page: 1, page_size: 20, status: '', refund_no: '' });
  const [items, setItems] = useState<Refund[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [reload, setReload] = useState(0);
  const [selected, setSelected] = useState<Refund | null>(null);
  const [action, setAction] = useState<'retry' | 'approve' | 'reject'>('retry');
  const [reason, setReason] = useState('');
  const [retryError, setRetryError] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [notice, setNotice] = useState('');
  async function retry() {
    if (!selected || submitting) return;
    if (!reason.trim()) { setRetryError(action === 'reject' ? '请填写拒绝原因' : action === 'approve' ? '请填写审核意见' : '请填写重试原因'); return; }
    setSubmitting(true); setRetryError('');
    try {
      const result = await apiPost<{ review_status?: string }>('/api/v1/admin/billing/refunds/' + encodeURIComponent(selected.refund_no) + '/' + action, action === 'approve' ? { approve_comment: reason.trim() } : { reason: reason.trim() });
      setSelected(null); setNotice(action === 'reject' ? '已拒绝退款申请，并保存审核原因。' : action === 'retry' ? '已安排重试原退款任务，请刷新查看处理结果。' : result.review_status === 'approved' ? '双签审核通过，已交由退款任务处理。' : '第一签已记录，等待另一名财务人员审核。'); setReload(v => v + 1);
    } catch (e: unknown) { setRetryError(e instanceof Error ? e.message : '重试失败，请稍后再试'); }
    finally { setSubmitting(false); }
  }
  useEffect(() => {
    let active = true;
    setLoading(true); setError(''); setItems([]); setTotal(0);
    const params = new URLSearchParams({ page: String(query.page), page_size: String(query.page_size) });
    if (query.status) params.set('status', query.status);
    if (query.refund_no) params.set('refund_no', query.refund_no);
    apiGet<{ items: Refund[]; total: number }>('/api/v1/admin/billing/refunds?' + params)
      .then(data => { if (active) { setItems(data.items); setTotal(data.total); } })
      .catch(e => { if (active) setError(e.message || '退款记录读取失败'); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [query, reload]);
  return <>
    {notice && <Alert type="success" showIcon message={notice} style={{ marginBottom: 16 }} />}
    <div style={{ display: 'flex', gap: 12, marginBottom: 16, flexWrap: 'wrap' }}>
      <Input.Search placeholder="完整退款单号" allowClear style={{ width: 320 }} onSearch={value => setQuery(q => ({ ...q, page: 1, refund_no: value.trim() }))} />
      <Select aria-label="退款状态" style={{ width: 160 }} value={query.status} options={[{ value: '', label: '全部状态' }, ...Object.entries(labels).map(([value, label]) => ({ value, label }))]} onChange={status => setQuery(q => ({ ...q, page: 1, status }))} />
      <Button loading={loading} onClick={() => setReload(v => v + 1)}>刷新</Button>
    </div>
    {error && <Alert type="error" showIcon message={error} style={{ marginBottom: 16 }} action={<Button onClick={() => setReload(v => v + 1)}>重试</Button>} />}
    <Table<Refund> rowKey="id" dataSource={items} loading={loading} scroll={{ x: 1920 }}
      pagination={{ current: query.page, pageSize: query.page_size, total, showSizeChanger: true, pageSizeOptions: [20, 50, 100], onChange: (page, page_size) => setQuery(q => ({ ...q, page, page_size })) }}
      columns={[
        { title: '退款单号', dataIndex: 'refund_no', width: 220 },
        { title: '用户 ID', dataIndex: 'user_id', width: 130 },
        { title: '退款金额', dataIndex: 'refund_cents', width: 110, render: (v: number) => `¥${(v / 100).toFixed(2)}` },
        { title: '状态', dataIndex: 'status', width: 120, render: (v: string) => labels[v] || v },
        { title: '申请原因', dataIndex: 'reason', width: 200 },
        { title: '异常说明', dataIndex: 'failure_reason', width: 240 },
        { title: '任务异常', width: 220, render: (_, row) => row.task?.last_error || '—' },
        { title: '审核状态', width: 120, render: (_, row) => row.review ? (row.review.status === 'rejected' ? '已拒绝' : row.review.status === 'approved' ? '双签已通过' : '等待第二签') : '未人工审核' },
        { title: '操作', width: 200, fixed: 'right', render: (_, row) => <>
          {row.can_approve && <Button onClick={() => { setSelected(row); setAction('approve'); setReason(''); setRetryError(''); setNotice(''); }}>审核通过</Button>}
          {row.can_reject && <Button danger onClick={() => { setSelected(row); setAction('reject'); setReason(''); setRetryError(''); setNotice(''); }}>拒绝申请</Button>}
          {row.can_retry && <Button onClick={() => { setSelected(row); setAction('retry'); setReason(''); setRetryError(''); setNotice(''); }}>重试任务</Button>}
          {!row.can_approve && !row.can_retry && !row.can_reject && '—'}
        </> },
        { title: '创建时间', dataIndex: 'created_at', width: 180, render: (v: string) => new Date(v).toLocaleString() },
        { title: '完成时间', dataIndex: 'completed_at', width: 180, render: (v: string | null) => v ? new Date(v).toLocaleString() : '—' },
      ]} />
    <Modal title={action === 'reject' ? '拒绝退款申请' : action === 'approve' ? '退款审核' : '重试退款任务'} open={!!selected} onOk={retry} onCancel={() => { if (!submitting) setSelected(null); }} confirmLoading={submitting} cancelButtonProps={{ disabled: submitting }} closable={!submitting} maskClosable={!submitting} okButtonProps={{ danger: action === 'reject' }} okText={action === 'reject' ? '确认拒绝' : action === 'approve' ? '同意退款' : '确认重试'} cancelText="取消">
      <p>退款单号：{selected?.refund_no}</p>
      <p>退款金额：¥{((selected?.refund_cents || 0) / 100).toFixed(2)}</p>
      {action === 'reject' ? <p>拒绝后此申请不再执行退款，请填写具体原因。</p> : action === 'approve' ? <>
        <p>需要两名不同的客户财务人员分别审核，通过后安排退款。</p>
        {selected?.review && <p>第一签账号：{selected.review.first_signer}；意见：{selected.review.first_comment}</p>}
      </> : <p>继续处理这笔退款。操作受理后，请以实际到账状态为准。</p>}
      <Input.TextArea aria-label={action === 'reject' ? '拒绝原因' : action === 'approve' ? '审核意见' : '重试原因'} placeholder={action === 'reject' ? '填写拒绝退款的具体原因' : action === 'approve' ? '填写审核依据与意见' : '填写已核实的异常及重试原因'} value={reason} maxLength={255} disabled={submitting} onChange={e => setReason(e.target.value)} />
      {retryError && <Alert type="error" message={retryError} style={{ marginTop: 12 }} />}
    </Modal>
  </>;
}
