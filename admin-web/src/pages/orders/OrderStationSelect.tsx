import { Button, Select, Space, Typography } from 'antd';
import { useEffect, useRef, useState } from 'react';
import { ApiEnvelope, http } from '../../api/client';

interface Station { id: number; name: string }

export default function OrderStationSelect({ value = 0, onChange }: { value?: number; onChange?: (value: number) => void }) {
  const [search, setSearch] = useState('');
  const [query, setQuery] = useState({ keyword: '', page: 1 });
  const [stations, setStations] = useState<Station[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [reload, setReload] = useState(0);
  const names = useRef(new Map<number, string>());

  useEffect(() => {
    const timer = window.setTimeout(() => setQuery(previous => previous.keyword === search.trim()
      ? previous : { keyword: search.trim(), page: 1 }), 250);
    return () => window.clearTimeout(timer);
  }, [search]);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true); setError('');
    if (query.page === 1) setStations([]);
    http.get<ApiEnvelope<{ items: Station[]; total: number }>>('/api/v1/admin/stations', {
      signal: controller.signal, params: { ...query, keyword: query.keyword || undefined, page_size: 100 },
    }).then(response => {
      if (controller.signal.aborted) return;
      const result = response.data.data;
      if (!result) throw new Error('站点响应为空');
      result.items.forEach(station => names.current.set(station.id, station.name));
      setStations(previous => query.page === 1 ? result.items
        : [...new Map([...previous, ...result.items].map(station => [station.id, station])).values()]);
      setTotal(result.total);
    }).catch(() => {
      if (!controller.signal.aborted) setError('站点加载失败');
    }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [query, reload]);

  return <Space direction="vertical" size={0}>
    <Select aria-label="站点" showSearch allowClear value={value} loading={loading} style={{ width: 220 }}
      placeholder="全部站点" filterOption={false} onSearch={setSearch}
      onChange={next => { onChange?.(next ?? 0); setSearch(''); }}
      labelRender={option => option.value === 0 ? '全部站点' : names.current.get(Number(option.value)) || option.label}
      notFoundContent={loading ? '加载中…' : error || '没有匹配的站点'}
      onPopupScroll={event => {
        const element = event.currentTarget;
        if (!loading && !error && stations.length < total && element.scrollTop + element.clientHeight >= element.scrollHeight - 24) {
          setQuery({ ...query, page: query.page + 1 });
        }
      }}
      options={[{ value: 0, label: '全部站点' }, ...stations.map(station => ({ value: station.id, label: station.name }))]} />
    {error && <Space size={4}><Typography.Text type="danger">{error}</Typography.Text><Button type="link" size="small" onClick={() => setReload(value => value + 1)}>重试</Button></Space>}
  </Space>;
}
