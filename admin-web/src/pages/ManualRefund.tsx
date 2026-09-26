import { useRef, useState } from 'react';
import { Alert, Button, Input, Modal } from 'antd';
import axios from 'axios';
import { apiPost } from '../api/client';
interface Request { request_id: string; amount_cents: number; reason: string }
export default function ManualRefund({ orderId, orderNo, actorId, onCreated }: { orderId: number; orderNo: string; actorId: string; onCreated: () => void }) {
  const [open, setOpen] = useState(false), [amount, setAmount] = useState(''), [reason, setReason] = useState('');
  const [pending, setPending] = useState<Request | null>(null), [error, setError] = useState(''), [notice, setNotice] = useState(''), [busy, setBusy] = useState(false);
  const flight = useRef(false), session = useRef<string | null>(null);
  const key = `cp_manual_refund_${actorId}_${orderId}`;
  function show() {
    setError(''); setNotice(''); session.current = localStorage.getItem('cp_token');
    try {
      const saved = localStorage.getItem(key); const value = saved ? JSON.parse(saved) as Request : null;
      if (value && (typeof value.request_id !== 'string' || !Number.isSafeInteger(value.amount_cents) || typeof value.reason !== 'string')) throw Error('本地申请记录无效，请联系管理员核实');
      setPending(value); setAmount(value ? (value.amount_cents / 100).toFixed(2) : ''); setReason(value?.reason || ''); setOpen(true);
    } catch (e) { setError(e instanceof Error ? e.message : '无法读取本地申请'); }
  }
  async function submit() {
    if (flight.current) return;
    if (session.current !== localStorage.getItem('cp_token')) { setError('登录状态已变化，请重新打开申请'); return; }
    const match = /^(\d{1,8})(?:\.(\d{1,2}))?$/.exec(amount.trim());
    const cents = match ? Number(match[1]) * 100 + Number((match[2] || '').padEnd(2, '0')) : 0;
    if (!pending && (!Number.isSafeInteger(cents) || cents <= 0 || cents > 2147483647 || !reason.trim() || reason.trim().length > 255)) { setError('请填写有效金额（最多两位小数）和退款依据'); return; }
    flight.current = true; setBusy(true); setError('');
    try {
      const body = pending || { request_id: crypto.randomUUID(), amount_cents: cents, reason: reason.trim() };
      localStorage.setItem(key, JSON.stringify(body)); setPending(body);
      const result = await apiPost<{ created: boolean; refund_no: string; request_id: string }>(`/api/v1/admin/orders/${orderId}/refunds`, body);
      if (!result.created || result.request_id !== body.request_id) throw Error('申请结果暂不确定，请重试同一申请');
      localStorage.removeItem(key); setPending(null); setOpen(false); setNotice(`申请 ${result.refund_no} 已创建并记录第一签，请另一名财务人员在退款审核页完成第二签。`); onCreated();
    } catch (e) {
      if (axios.isAxiosError(e) && [400, 409, 422].includes(e.response?.status || 0)) { localStorage.removeItem(key); setPending(null); }
      setError(e instanceof Error ? e.message : '提交失败，请重试同一申请');
    } finally { flight.current = false; setBusy(false); }
  }
  return <>
    <Button onClick={show}>发起人工退款</Button>
    {notice && <Alert type="success" showIcon message={notice} />}
    {error && !open && <Alert type="error" message={error} />}
    <Modal title="人工退款申请与第一签" open={open} onOk={submit} onCancel={() => { if (!busy) setOpen(false); }} confirmLoading={busy} cancelButtonProps={{ disabled: busy }} closable={!busy} maskClosable={!busy} okText={pending ? '重试同一申请' : '提交申请并签署'} cancelText="关闭">
      <p>订单：{orderNo}</p><p>本次提交将记录您的第一签，另一名财务人员审核后才执行退款。</p>
      <Input aria-label="退款金额（元）" placeholder="退款金额（元），最多两位小数" value={amount} onChange={e => setAmount(e.target.value)} disabled={busy || !!pending} />
      <Input.TextArea aria-label="退款依据" placeholder="退款依据与审核意见" maxLength={255} value={reason} onChange={e => setReason(e.target.value)} disabled={busy || !!pending} style={{ marginTop: 12 }} />
      {pending && <Alert type="info" message="已保存申请标识；网络失败后重试会继续核实同一申请。" style={{ marginTop: 12 }} />}
      {error && <Alert type="error" message={error} style={{ marginTop: 12 }} />}
    </Modal>
  </>;
}
