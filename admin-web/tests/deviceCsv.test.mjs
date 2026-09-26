import test from 'node:test';
import assert from 'node:assert/strict';
import { parseDeviceCsv } from '../src/utils/deviceCsv.ts';
const header = 'device_id,vendor_id,station_id,port_count,model\n';
test('BOM, CRLF, quoted commas and escaped quotes', () => {
  const [device] = parseDeviceCsv('\uFEFF'+header.replace('\n','\r\n')+'DEVICE_001,1,2,3,"Model, ""A"""\r\n');
  assert.equal(device.model,'Model, "A"'); assert.equal(device.port_count,3);
});
test('rejects malformed quote placements instead of changing device identity', () => {
  for (const row of ['DEVI"CE_001",1,2,3,', '"DEVICE_001"junk,1,2,3,', 'DEVICE_001,1,2,3,"unclosed']) assert.throws(() => parseDeviceCsv(header+row), /引号/);
});
test('reports physical line after blank rows and catches duplicate identity', () => {
  assert.throws(() => parseDeviceCsv(header+'\nDEVICE_001,1,2,3,\n\ndevice_001,1,2,3,'), /第 5 行.*重复/);
});
test('validates integer syntax, safe precision, port range and model control characters', () => {
  for (const value of ['1e2','0x10','1.5','9007199254740992','0','-1']) assert.throws(() => parseDeviceCsv(header+`DEVICE_001,${value},2,3,`), /无效/);
  assert.throws(() => parseDeviceCsv(header+'DEVICE_001,1,2,256,'), /无效/);
  assert.throws(() => parseDeviceCsv(header+'DEVICE_001,1,2,3,"line\nbreak"'), /可见字符/);
});
test('enforces batch and schema bounds', () => {
  assert.throws(() => parseDeviceCsv(header), /1–100/);
  assert.throws(() => parseDeviceCsv(header+Array.from({length:101},(_,i)=>`DEVICE_${i.toString().padStart(3,'0')},1,2,3,`).join('\n')), /1–100/);
  assert.throws(() => parseDeviceCsv(header+'DEVICE_001,1,2,3'), /5 列/);
});
