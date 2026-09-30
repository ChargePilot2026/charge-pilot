import { useEffect, useId, useRef, useState } from 'react';
import { Button, Input, Popover, type InputRef } from 'antd';
import { ClockCircleOutlined } from '@ant-design/icons';
import { clockToMinute, minuteToClock, validatePeriodEndMinute, type PeriodForm } from './model';

type PeriodTimeInputProps = {
  value?: number;
  onChange?: (value: number) => void;
  periods: PeriodForm[];
  index: number;
  fixed?: boolean;
  readOnly?: boolean;
  disabled?: boolean;
  onValidityChange?: (error: string | undefined) => void;
  id?: string;
  'aria-describedby'?: string;
  'aria-labelledby'?: string;
  'aria-label'?: string;
};

// The form keeps integer minutes; unfinished or invalid text stays local so that
// typing cannot silently replace a saved boundary with an invalid numeric value.
export default function PeriodTimeInput({
  value, onChange, periods, index, fixed = false, readOnly = false, disabled = false,
  onValidityChange, id, 'aria-describedby': describedBy, 'aria-labelledby': labelledBy,
  'aria-label': label,
}: PeriodTimeInputProps) {
  const generatedId = useId();
  const errorId = `${id || generatedId}-time-error`;
  const [draft, setDraft] = useState(() => value === undefined ? '' : minuteToClock(value));
  const [pickerOpen, setPickerOpen] = useState(false);
  const inputRef = useRef<InputRef>(null);
  const previousValue = useRef(value);
  const lastEmittedValue = useRef<number>();
  const onChangeRef = useRef(onChange);
  const onValidityChangeRef = useRef(onValidityChange);
  onChangeRef.current = onChange;
  onValidityChangeRef.current = onValidityChange;

  const parsed = clockToMinute(draft);
  const error = validatePeriodEndMinute(periods, index, fixed ? value : parsed);
  const start = index > 0 ? periods[index - 1]?.end_minute : 0;
  const nextEnd = periods[index + 1]?.end_minute;
  const count = periods.length;
  const canEdit = !fixed && !readOnly && !disabled;

  useEffect(() => {
    if (previousValue.current !== value) {
      previousValue.current = value;
      // Our own valid edit should preserve exactly what is being typed until blur.
      // External form resets and list changes should replace the local draft.
      if (lastEmittedValue.current !== value || fixed) {
        lastEmittedValue.current = undefined;
        setDraft(value === undefined ? '' : minuteToClock(value));
        return;
      }
    }
    // A neighboring boundary can make an earlier invalid draft valid again.
    // Commit it as soon as it becomes valid, keeping the displayed value and form
    // value consistent without requiring the user to type the same time twice.
    if (canEdit && parsed !== undefined && !error && parsed !== value && parsed !== lastEmittedValue.current) {
      lastEmittedValue.current = parsed;
      onChangeRef.current?.(parsed);
    }
  }, [value, draft, parsed, error, fixed, canEdit, index, start, nextEnd, count]);

  useEffect(() => {
    onValidityChangeRef.current?.(error);
  }, [error]);

  useEffect(() => () => {
    onValidityChangeRef.current?.(undefined);
  }, []);

  const updateDraft = (text: string) => {
    setDraft(text);
    const minute = clockToMinute(text);
    if (minute !== undefined && !validatePeriodEndMinute(periods, index, minute)) {
      lastEmittedValue.current = minute;
      onChange?.(minute);
    }
  };

  const selectTime = (minute: number) => {
    updateDraft(minuteToClock(minute));
    setPickerOpen(false);
    inputRef.current?.focus();
  };

  const options = Array.from({ length: 95 }, (_, option) => (option + 1) * 15)
    .filter(minute => !validatePeriodEndMinute(periods, index, minute));
  const inputLabel = label || `第 ${index + 1} 段结束时间`;
  const description = [describedBy, error ? errorId : undefined].filter(Boolean).join(' ') || undefined;

  return (
    <div>
      <Input
        ref={inputRef}
        id={id}
        value={fixed ? '24:00' : draft}
        onChange={event => updateDraft(event.target.value)}
        onBlur={() => {
          const minute = clockToMinute(draft.trim());
          if (canEdit && minute !== undefined && !validatePeriodEndMinute(periods, index, minute)) {
            updateDraft(minuteToClock(minute));
          }
        }}
        placeholder="08:00"
        autoComplete="off"
        maxLength={5}
        readOnly={fixed || readOnly}
        disabled={disabled}
        status={error ? 'error' : undefined}
        aria-label={inputLabel}
        aria-labelledby={labelledBy}
        aria-describedby={description}
        aria-invalid={!!error}
        addonAfter={canEdit ? (
          <Popover
            trigger="click"
            placement="bottomRight"
            open={pickerOpen}
            onOpenChange={setPickerOpen}
            content={(
              <div role="dialog" aria-label={`${inputLabel}常用时间`} style={{ width: 248 }}>
                <div style={{ marginBottom: 8, color: '#595959' }}>常用时间 · 每 15 分钟</div>
                {options.length ? (
                  <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 4, maxHeight: 228, overflowY: 'auto' }}>
                    {options.map(minute => (
                      <Button
                        key={minute}
                        size="small"
                        type={minute === parsed ? 'primary' : 'text'}
                        aria-pressed={minute === parsed}
                        onClick={() => selectTime(minute)}
                        style={{ height: 32, padding: '4px 8px' }}
                      >
                        {minuteToClock(minute)}
                      </Button>
                    ))}
                  </div>
                ) : <div style={{ color: '#8c8c8c' }}>本段没有可选的 15 分钟分界，请直接输入时间。</div>}
                <div style={{ marginTop: 8, color: '#8c8c8c', fontSize: 12 }}>也可直接输入，精确到分钟</div>
              </div>
            )}
          >
            <Button
              type="text"
              size="small"
              icon={<ClockCircleOutlined />}
              aria-label={`选择${inputLabel}`}
              aria-haspopup="dialog"
              aria-expanded={pickerOpen}
            />
          </Popover>
        ) : undefined}
      />
      {error && <div id={errorId} role="alert" style={{ marginTop: 4, color: '#ff4d4f', fontSize: 12 }}>{error}</div>}
    </div>
  );
}
