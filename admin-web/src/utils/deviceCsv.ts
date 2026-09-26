export interface ImportedDevice { device_id: string; vendor_id: number; station_id: number; port_count: number; model: string | null }

export function parseDeviceCsv(source: string): ImportedDevice[] {
  const text = source.replace(/^\uFEFF/, '');
  const rows: { values: string[]; line: number }[] = [];
  let values: string[] = [], cell = '', state: 'start' | 'plain' | 'quoted' | 'closed' = 'start';
  let line = 1, startLine = 1;
  const field = () => { values.push(cell); cell = ''; state = 'start'; };
  const row = () => { field(); if (values.some(v => v.trim())) rows.push({ values, line: startLine }); values = []; };
  const fail = (message: string): never => { throw new Error(`第 ${line} 行：${message}`); };
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (state === 'quoted') {
      if (c === '"') { if (text[i + 1] === '"') { cell += '"'; i++; } else state = 'closed'; }
      else { cell += c; if (c === '\n') line++; }
      continue;
    }
    if (c === ',') { field(); continue; }
    if (c === '\r' || c === '\n') {
      if (c === '\r' && text[i + 1] === '\n') i++;
      row(); line++; startLine = line; continue;
    }
    if (state === 'closed') fail('闭合引号后只能是逗号或换行');
    if (c === '"') { if (state !== 'start') fail('引号只能出现在字段开头'); state = 'quoted'; }
    else { state = 'plain'; cell += c; }
  }
  if (state === 'quoted') fail('CSV 引号未闭合');
  row();
  const expected = ['device_id', 'vendor_id', 'station_id', 'port_count', 'model'];
  const header = rows.shift()?.values;
  if (!header || header.length !== expected.length || header.some((v, i) => v !== expected[i])) throw new Error('请使用模板中的表头和列顺序');
  if (!rows.length || rows.length > 100) throw new Error('每批导入 1–100 台设备');
  const seen = new Set<string>();
  return rows.map(({ values, line }) => {
    const error = (message: string): never => { throw new Error(`第 ${line} 行：${message}`); };
    if (values.length !== 5) error('应有 5 列');
    const [device_id, vendor, station, ports, model] = values.map(v => v.trim());
    if (!/^[A-Za-z0-9_-]{8,32}$/.test(device_id)) error('设备 ID 格式错误');
    if (seen.has(device_id.toLowerCase())) error('设备 ID 重复');
    seen.add(device_id.toLowerCase());
    const raw = [vendor, station, ports], numbers = raw.map(Number);
    if (raw.some(v => !/^[0-9]+$/.test(v)) || numbers.some(n => !Number.isSafeInteger(n) || n <= 0) || numbers[2] > 255) error('厂商、站点或端口数无效');
    if ([...model].length > 128 || /[\u0000-\u001f\u007f-\u009f]/.test(model)) error('型号须为 1 至 128 个可见字符');
    return { device_id, vendor_id: numbers[0], station_id: numbers[1], port_count: numbers[2], model: model || null };
  });
}
