const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadPage(relativePath, app, wx = {}) {
  let page;
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, '..', relativePath), 'utf8'), {
    getApp: () => app,
    Page: value => { page = value; },
    wx: { showToast() {}, showModal({ success }) { success?.({ confirm: true }); }, navigateBack() {}, setClipboardData({ success }) { success?.(); }, openCustomerServiceChat() {}, ...wx },
    Date, Promise, setImmediate,
  });
  page.setData = (value, callback) => { Object.assign(page.data, value); callback?.(); };
  page._generation = 0;
  page._gone = false;
  return page;
}

test('every registered mini-program page has a script, WXML, and page configuration', () => {
  const root = path.join(__dirname, '..');
  const app = JSON.parse(fs.readFileSync(path.join(root, 'app.json'), 'utf8'));
  for (const name of app.pages) {
    const base = path.join(root, name);
    assert.ok(fs.existsSync(`${base}.wxml`), `${name} WXML missing`);
    assert.ok(fs.existsSync(`${base}.json`), `${name} page config missing`);
    assert.ok(fs.existsSync(`${base}.js`) || fs.existsSync(`${base}.ts`), `${name} script missing`);
  }
});

test('coupon list requests the selected status and paginates without dropping prior rows', async () => {
  const calls = [];
  const app = { globalData: { token: 'token' }, request: async (_method, _url, query) => { calls.push(query); return { items: [{ grant_id: query.page, name: '券', discount_type: 'amount', discount_value_cents: 500, min_charge_cents: 0, expired_at: '2026-10-01T00:00:00Z', status: 'unused' }], total: 2 }; } };
  const page = loadPage('pages/coupons/my.js', app);
  await page.load(true);
  await page.load(false);
  assert.deepEqual(calls.map(call => [call.status, call.page, call.page_size]), [['unused', 1, 20], ['unused', 2, 20]]);
  assert.equal(page.data.items.length, 2);
});

test('phone binding sends only the one-time WeChat authorization code', async () => {
  const calls = [];
  const app = { globalData: { token: 'token' }, _generation: 3, request: async (...args) => { calls.push(args); return { bound: true }; } };
  const page = loadPage('pages/profile/phone.js', app);
  await page.onGetPhoneNumber({ detail: { code: 'wx-one-time-code' } });
  assert.equal(JSON.stringify(calls[0]), JSON.stringify(['POST', '/user/phone/bind', { code: 'wx-one-time-code' }]));
  assert.equal(page.data.bound, true);
  assert.equal(page.data.notice, '手机号已通过微信验证并绑定。');
});

test('phone binding does not post when the user declines authorization', async () => {
  let called = false;
  const app = { globalData: { token: 'token' }, _generation: 0, request: async () => { called = true; } };
  const page = loadPage('pages/profile/phone.js', app);
  await page.onGetPhoneNumber({ detail: { errMsg: 'getPhoneNumber:fail user deny' } });
  assert.equal(called, false);
  assert.match(page.data.error, /取消|未取得/);
});

test('invoice application derives the payable amount from the server order detail', async () => {
  const calls = [];
  const app = { globalData: { token: 'token' }, _generation: 1, request: async (method, url, body) => {
    calls.push({ method, url, body });
    if (method === 'POST') return { invoice_no: 'INV-1' };
    if (url === '/user/charge/history') return { items: [{ order_id: 19, order_no: 'ORD-19', device_id: 'D-1', status: 'completed', total_fee_cents: 700 }] };
    return { order_id: 19, order_no: 'ORD-19', status: 'completed', paid_fee_cents: 650, refunded_cents: 0 };
  } };
  const page = loadPage('pages/invoice/apply.js', app);
  await page.loadOrders();
  await page.loadOrderDetail(page.data.orders[0]);
  assert.equal(page.data.selected.amount_cents, 650);
  page.onTitle({ detail: { value: '张三' } });
  await page.submit();
  const post = calls.find(call => call.method === 'POST');
  assert.equal(post.body.biz_id, 19);
  assert.equal(post.body.total_cents, 650);
  assert.equal(page.data.notice, '发票申请已提交，编号 INV-1');
});

test('invoice application blocks an order that has a refund', async () => {
  const app = { globalData: { token: 'token' }, _generation: 1, request: async () => ({ order_id: 20, order_no: 'ORD-20', status: 'completed', paid_fee_cents: 650, refunded_cents: 1 }) };
  const page = loadPage('pages/invoice/apply.js', app);
  await page.loadOrderDetail({ order_id: 20 });
  assert.equal(page.data.selected, null);
  assert.match(page.data.error, /退款/);
});

test('customer service opens only with a configured WeChat entry', async () => {
  let opened;
  const app = { globalData: { token: 'token' }, request: async () => ({ available: true, corp_id: 'ww123', entry_url: 'https://work.weixin.qq.com/kf/1', agent_wechat: 'cs1' }) };
  const page = loadPage('pages/cs/chat.js', app, { openCustomerServiceChat: options => { opened = options; options.complete(); } });
  await page.load();
  page.openChat();
  assert.equal(opened.corpId, 'ww123');
  assert.equal(opened.extInfo.url, 'https://work.weixin.qq.com/kf/1');
});

test('fault report sends the selected device and a constrained category', async () => {
  const calls = [];
  const app = { globalData: { token: 'token' }, _generation: 2, request: async (method, url, value) => {
    calls.push({ method, url, value });
    if (method === 'POST') return { submitted: true, report_id: '17' };
    if (url.endsWith('/17/history')) return { items: [{ event_type: 'fixed', from_status: 'dispatched', to_status: 'fixed', note: '更换连接器后恢复', created_at: '2026-09-27T10:00:00Z' }], total: 1 };
    return { items: [{ report_id: '17', status: 'fixed' }], total: 1 };
  } };
  const page = loadPage('pages/dev/fault.js', app);
  page._deviceId = 'DEV_01';
  page.onDescription({ detail: { value: '接口没有响应' } });
  await page.submit();
  const submit = calls.find(call => call.method === 'POST');
  assert.equal(JSON.stringify(submit.value), JSON.stringify({ device_id: 'DEV_01', fault_type: 'mechanical', description: '接口没有响应' }));
  assert.ok(calls.some(call => call.method === 'GET' && call.url === '/user/device/fault-reports'));
  await page.toggleHistory({ currentTarget: { dataset: { reportId: '17' } } });
  assert.ok(calls.some(call => call.method === 'GET' && call.url === '/user/device/fault-reports/17/history'));
  assert.equal(page.data.reportHistory[0].eventText, '已标记修复');
  assert.equal(page.data.reportHistory[0].note, '更换连接器后恢复');
  assert.match(page.data.notice, /已收到/);
});

test('announcement upstream failures remain visible and never become an empty success', async () => {
  const app = { globalData: { token: 'token' }, request: async () => { throw new Error('admin service unavailable'); } };
  const page = loadPage('pages/announcement/list.js', app);
  await page.load();
  assert.equal(page.data.items.length, 0);
  assert.equal(page.data.error, 'admin service unavailable');
});
