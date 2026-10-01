export interface ProcessSample {
  id: number;
  ts: string;
  power_w: number;
  charged_kwh: number;
  remaining_kwh: number;
  charged_seconds: number;
  remaining_seconds: number;
  signal_strength: number;
  port_status?: number | null;
  voltage_v?: number | null;
  temperature_c?: number | null;
  device_status?: number | null;
}
export interface ProcessPage { items: ProcessSample[]; next_after_id: number | null }
export interface PowerPoint { sample: ProcessSample; at: number }

// 每页都用服务器返回的 ID 游标继续，时间排序留到全部页读取完成后处理。
export async function readProcessPages(
  fetchPage: (afterID: number | undefined, signal: AbortSignal) => Promise<ProcessPage>,
  signal: AbortSignal,
  afterID?: number,
): Promise<ProcessSample[]> {
  let cursor = afterID;
  const samples: ProcessSample[] = [];
  while (true) {
    signal.throwIfAborted();
    const page = await fetchPage(cursor, signal);
    signal.throwIfAborted();
    if (!page || !Array.isArray(page.items) || !Object.prototype.hasOwnProperty.call(page, 'next_after_id')) {
      throw new Error('功率采样响应不完整，请重试');
    }
    let previousID = cursor ?? 0;
    for (const sample of page.items) {
      if (!Number.isSafeInteger(sample.id) || sample.id <= previousID) throw new Error('功率采样游标无效，请重试');
      previousID = sample.id;
    }
    samples.push(...page.items);
    if (page.next_after_id === null) return samples;
    if (!Number.isSafeInteger(page.next_after_id) || page.next_after_id <= (cursor ?? 0)
      || !page.items.length || page.next_after_id !== page.items[page.items.length - 1].id) {
      throw new Error('功率采样游标未前进，请重试');
    }
    cursor = page.next_after_id;
  }
}

export function powerPoints(samples: ProcessSample[]): PowerPoint[] {
  const unique = new Map(samples.map(sample => [sample.id, sample]));
  return Array.from(unique.values()).map(sample => {
    const at = Date.parse(sample.ts);
    if (!Number.isFinite(at) || !Number.isFinite(sample.power_w) || sample.power_w < 0) {
      throw new Error('功率采样包含无效时间或功率，请重试');
    }
    return { sample, at };
  }).sort((a, b) => a.at - b.at || a.sample.id - b.sample.id);
}

export function powerSegments(points: PowerPoint[], gapMS = 60_000): PowerPoint[][] {
  const segments: PowerPoint[][] = [];
  points.forEach((point, index) => {
    if (!index || point.at - points[index - 1].at > gapMS) segments.push([]);
    segments[segments.length - 1].push(point);
  });
  return segments;
}

export function nearestPowerPoint(points: PowerPoint[], at: number): number {
  if (!points.length) return -1;
  let low = 0, high = points.length - 1;
  while (low < high) {
    const middle = Math.floor((low + high) / 2);
    if (points[middle].at < at) low = middle + 1;
    else high = middle;
  }
  return low > 0 && at - points[low - 1].at <= points[low].at - at ? low - 1 : low;
}

export function powerAxisStep(peak: number): number {
  if (peak <= 0) return 1;
  const target = peak / 4, base = 10 ** Math.floor(Math.log10(target));
  return [1, 2, 5, 10].find(step => step * base >= target)! * base;
}
