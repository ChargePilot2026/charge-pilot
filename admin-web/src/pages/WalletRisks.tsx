import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Input, Modal, Select, Space, Table, Tag, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { apiGet, apiPost } from '../api/client';

interface RiskReview {
  actor_id: string;
  approved: boolean;
  comment: string;
}

interface RiskRelease {
  actor_id: string;
  comment: string;
  wallet_active: boolean;
}

interface Risk {
  request_id: string;
  user_id: string;
  amount_cents: number;
  reason: string | null;
  created_at: string;
  review?: RiskReview | null;
  review_created_at?: string | null;
  release?: RiskRelease | null;
  release_created_at?: string | null;
  freeze_status?: string | null;
  freeze_linked: boolean;
  can_release: boolean;
}

type QueueStatus = 'pending' | 'reviewed';
type Action = 'approve' | 'reject' | 'release';

const money = (cents: number) => `¥${(cents / 100).toFixed(2)}`;
const dateTime = (value?: string | null) => value ? new Date(value).toLocaleString() : '—';

function actionTitle(action: Action) {
  if (action === 'approve') return '通过钱包退款审核';
  if (action === 'reject') return '拒绝钱包退款申请';
  return '解除退款风控冻结';
}

export default function WalletRisks() {
  const [page, setPage] = useState(1);
  const [status, setStatus] = useState<QueueStatus>('pending');
  const [reload, setReload] = useState(0);
  const [items, setItems] = useState<Risk[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [selected, setSelected] = useState<{ item: Risk; action: Action } | null>(null);
  const [comment, setComment] = useState('');
  const [submitError, setSubmitError] = useState('');
  const [busy, setBusy] = useState(false);
  const flight = useRef(false);
  const session = useRef('');
  const alive = useRef(true);

  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; };
  }, []);

  useEffect(() => {
    let current = true;
    setLoading(true);
    setError('');
    setItems([]);
    apiGet<{ items: Risk[]; total: number }>(
      `/api/v1/admin/billing/wallet-risks?page=${page}&page_size=20&status=${status}`,
    ).then(result => {
      if (current) {
        setItems(result.items);
        setTotal(result.total);
      }
    }).catch((cause: Error) => {
      if (current) setError(cause.message || '风控记录读取失败');
    }).finally(() => {
      if (current) setLoading(false);
    });
    return () => { current = false; };
  }, [page, reload, status]);

  const open = (item: Risk, action: Action) => {
    session.current = localStorage.getItem('cp_token') || '';
    setSelected({ item, action });
    setComment('');
    setSubmitError('');
  };

  const close = () => {
    if (!busy) setSelected(null);
  };

  const submit = async () => {
    if (flight.current || !selected) return;
    const reason = comment.trim();
    if (!reason) {
      setSubmitError(selected.action === 'release' ? '请填写解冻依据' : '请填写审核依据');
      return;
    }
    if (reason.length > 255 || /[\u0000-\u001f\u007f]/.test(reason)) {
      setSubmitError('依据不得超过 255 个字符或包含控制字符');
      return;
    }
    if (session.current !== (localStorage.getItem('cp_token') || '')) {
      setSubmitError('登录账号已变化，请关闭窗口后刷新');
      return;
    }

    flight.current = true;
    setBusy(true);
    setSubmitError('');
    const { item, action } = selected;
    try {
      if (action === 'release') {
        await apiPost(`/api/v1/admin/billing/wallet-risks/${item.request_id}/release`, { comment: reason });
      } else {
        await apiPost(`/api/v1/admin/billing/wallet-risks/${item.request_id}/review`, {
          approved: action === 'approve',
          comment: reason,
        });
      }
      if (alive.current && session.current === (localStorage.getItem('cp_token') || '')) {
        setSelected(null);
        setNotice(action === 'release'
          ? '已解除该退款频次冻结，钱包余额和退款预留保持不变。'
          : action === 'approve'
            ? '已通过审核并进入原路退款处理，实际到账以退款结果为准。'
            : '已拒绝该退款申请，审核意见已保存。');
        setReload(value => value + 1);
      }
    } catch (cause) {
      if (alive.current && session.current === (localStorage.getItem('cp_token') || '')) {
        setSubmitError(cause instanceof Error ? cause.message : '操作结果未确认，请使用相同依据重试');
      }
    } finally {
      flight.current = false;
      if (alive.current) setBusy(false);
    }
  };

  const columns: ColumnsType<Risk> = [
    { title: '申请编号', dataIndex: 'request_id', width: 290, fixed: 'left' },
    { title: '用户 ID', dataIndex: 'user_id', width: 120 },
    { title: '申请金额', width: 120, render: (_, item) => money(item.amount_cents) },
    { title: '申请原因', dataIndex: 'reason', width: 180, render: value => value || '—' },
    { title: '申请时间', width: 190, render: (_, item) => dateTime(item.created_at) },
    {
      title: '审核结果', width: 130, render: (_, item) => item.review
        ? <Tag color={item.review.approved ? 'green' : 'red'}>{item.review.approved ? '已通过' : '已拒绝'}</Tag>
        : <Tag color="gold">待审核</Tag>,
    },
    {
      title: '钱包冻结', width: 130, render: (_, item) => item.freeze_status
        ? <Tag color={item.freeze_status === 'frozen' ? 'orange' : 'default'}>{item.freeze_status === 'frozen' ? '已冻结' : '未冻结'}</Tag>
        : <Tag>无关联记录</Tag>,
    },
    {
      title: '处置记录', width: 270, render: (_, item) => (
        <Space direction="vertical" size={2}>
          {item.review && <Typography.Text>审核：{item.review.comment}（{dateTime(item.review_created_at)}）</Typography.Text>}
          {item.release && <Typography.Text>解冻：{item.release.comment}（{dateTime(item.release_created_at)}）</Typography.Text>}
          {!item.review && !item.release && <Typography.Text type="secondary">尚无处置记录</Typography.Text>}
        </Space>
      ),
    },
    {
      title: '操作', width: 230, fixed: 'right', render: (_, item) => (
        <Space size={4}>
          {!item.review && status === 'pending' && <>
            <Button type="link" disabled={busy} onClick={() => open(item, 'approve')}>通过</Button>
            <Button type="link" danger disabled={busy} onClick={() => open(item, 'reject')}>拒绝</Button>
          </>}
          {item.can_release && <Button type="link" disabled={busy} onClick={() => open(item, 'release')}>解除风控冻结</Button>}
          {status === 'reviewed' && !item.can_release && !item.release && !item.freeze_linked && <Typography.Text type="secondary">无需解冻</Typography.Text>}
          {item.release && <Tag color="green">已解冻</Tag>}
        </Space>
      ),
    },
  ];

  return <div>
    <Alert
      type="info"
      showIcon
      message="钱包退款风控处置"
      description="审核决定是否受理退款；解除风控只移除本次退款频次造成的冻结，不改变余额、退款预留或其他冻结原因。解冻需要 customer_finance 角色及独立权限。"
      style={{ marginBottom: 12 }}
    />
    {notice && <Alert type="success" message={notice} closable onClose={() => setNotice('')} style={{ marginBottom: 12 }} />}
    {error && <Alert type="error" message={error} style={{ marginBottom: 12 }} />}
    <Space style={{ marginBottom: 12 }}>
      <Select<QueueStatus>
        aria-label="风控记录状态"
        value={status}
        style={{ width: 160 }}
        options={[
          { value: 'pending', label: '待审核申请' },
          { value: 'reviewed', label: '审核历史' },
        ]}
        onChange={value => { setStatus(value); setPage(1); setNotice(''); }}
      />
      <Button onClick={() => setReload(value => value + 1)} disabled={loading || busy}>刷新</Button>
    </Space>
    <Table<Risk>
      rowKey="request_id"
      dataSource={items}
      loading={loading}
      scroll={{ x: 1650 }}
      pagination={{ current: page, pageSize: 20, total, showSizeChanger: false, onChange: setPage }}
      columns={columns}
    />
    <Modal
      title={selected ? actionTitle(selected.action) : ''}
      open={!!selected}
      confirmLoading={busy}
      onOk={submit}
      onCancel={close}
      maskClosable={!busy}
      closable={!busy}
      cancelButtonProps={{ disabled: busy }}
      okText={selected?.action === 'release' ? '确认解冻' : selected?.action === 'approve' ? '通过审核' : '拒绝申请'}
    >
      <p>{selected?.item.request_id} · 用户 {selected?.item.user_id} · {money(selected?.item.amount_cents || 0)}</p>
      {selected?.action === 'release' && <Alert type="warning" showIcon message="仅解除与此申请关联的退款频次冻结，不会解冻其他原因造成的冻结。" style={{ marginBottom: 12 }} />}
      <Input.TextArea
        aria-label={selected?.action === 'release' ? '解冻依据' : '审核依据'}
        value={comment}
        onChange={event => setComment(event.target.value)}
        maxLength={255}
        showCount
        rows={4}
        disabled={busy}
        placeholder={selected?.action === 'release' ? '填写核实情况和解冻依据' : '填写核实情况和审核依据'}
      />
      {submitError && <Alert type="error" message={submitError} style={{ marginTop: 12 }} />}
    </Modal>
  </div>;
}
