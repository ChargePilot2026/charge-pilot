import { Table, Tag, Tooltip, Typography } from 'antd';
import { useEffect, useState } from 'react';
import dayjs from 'dayjs';
import { adminSession, http, type ApiEnvelope } from '../../api/client';
import { LoadError } from '../../components/LoadError';
import { DEFAULT_PAGE_SIZE, TABLE_PAGINATION } from '../../utils/tablePagination';
import { paymentStatusColors, paymentStatusInfo, refundedAmount } from '../orders/presentation';

interface Recharge {
  payment_order_id: number;
  order_no: string;
  user_id: string;
  biz_type: 'wallet_recharge';
  pay_method: string;
  status: string;
  payment_status: string;
  total_cents: number;
  paid_cents: number;
  refunded_cents: number;
  paid_at: string | null;
  created_at: string;
}
interface RechargePage { items: Recharge[]; total: number; page: number; page_size: number }
const paymentMethods: Record<string, string> = { wechat: '微信支付', balance: '余额支付' };
const time = (value: string | null) => value ? dayjs(value).format('YYYY-MM-DD HH:mm:ss') : '—';
const paymentTag = (row: Recharge) => {
  const info = paymentStatusInfo({ payment_status: row.payment_status, payment_order_status: row.status });
  const color = paymentStatusColors[row.payment_status];
  const tag = <Tag style={color ? { color } : undefined}>{info.label}</Tag>;
  return info.hint ? <Tooltip title={info.hint}>{tag}</Tooltip> : tag;
};

export default function ChargeUserRecharges({ userID }: { userID: string }) {
  const [pagination, setPagination] = useState({ page: 1, page_size: DEFAULT_PAGE_SIZE });
  const [page, setPage] = useState<RechargePage | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [reload, setReload] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    const epoch = adminSession.epoch();
    const current = () => !controller.signal.aborted && epoch === adminSession.epoch();
    const invalidate = () => {
      if (epoch === adminSession.epoch()) return;
      controller.abort();
      setPage(null); setError(''); setLoading(false);
    };
    window.addEventListener('cp-session', invalidate);
    window.addEventListener('storage', invalidate);
    setPage(null); setError(''); setLoading(true);
    const request = { signal: controller.signal, params: pagination, cpEpoch: epoch };
    http.get<ApiEnvelope<RechargePage>>(`/api/v1/admin/users/${userID}/recharges`, request).then(response => {
      if (!current()) return;
      const result = response.data.data;
      if (!result || !Array.isArray(result.items) || result.items.some(row => row.user_id !== userID || row.biz_type !== 'wallet_recharge')) {
        throw new Error('余额记录响应不完整，请重新读取');
      }
      setPage(result);
    }).catch(cause => {
      if (current()) setError(cause instanceof Error ? cause.message : '余额记录读取失败，请稍后重试');
    }).finally(() => { if (current()) setLoading(false); });
    return () => {
      controller.abort();
      window.removeEventListener('cp-session', invalidate);
      window.removeEventListener('storage', invalidate);
    };
  }, [userID, pagination, reload]);

  return <>
    {error && <LoadError title="余额记录加载失败" detail={error} onRetry={() => setReload(value => value + 1)} />}
    <Table<Recharge> rowKey="order_no" size="middle" loading={loading} dataSource={page?.items || []}
      scroll={{ x: 1100 }} locale={{ emptyText: error ? '暂时无法获取余额记录' : '暂无余额充值记录' }}
      pagination={{ ...TABLE_PAGINATION, current: pagination.page, pageSize: pagination.page_size, total: page?.total || 0,
        showTotal: total => `共 ${total} 笔`,
        onChange: (page, page_size) => setPagination(current => ({ page: page_size !== current.page_size ? 1 : page, page_size })) }}
      columns={[
        { title: '充值单号', dataIndex: 'order_no', width: 90, align: 'center', render: (orderNo: string) =>
          <Typography.Text copyable={{ text: orderNo, tooltips: ['复制充值单号', '已复制'] }} /> },
        { title: '应付金额', dataIndex: 'total_cents', width: 120, align: 'right', render: refundedAmount },
        { title: '实付金额', dataIndex: 'paid_cents', width: 120, align: 'right', render: refundedAmount },
        { title: '退款金额', dataIndex: 'refunded_cents', width: 120, align: 'right', render: refundedAmount },
        { title: '支付方式', dataIndex: 'pay_method', width: 110, render: value => paymentMethods[value] || value || '—' },
        { title: '支付状态', width: 130, render: (_, row) => paymentTag(row) },
        { title: '支付时间', dataIndex: 'paid_at', width: 190, render: time },
        { title: '创建时间', dataIndex: 'created_at', width: 190, render: time },
      ]} />
  </>;
}
