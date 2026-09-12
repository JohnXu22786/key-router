import { describe, expect, it } from 'vitest';
import dayjs from 'dayjs';
import type { Consumption } from '../api/client';
import { prorateStatsConsumptions } from './statsAggregation';

const consumption = (hourBucket: string, value = 60): Consumption => ({
  id: Number(hourBucket.slice(-2)) || 1,
  key_id: 1,
  hour_bucket: hourBucket,
  model_name: 'model',
  app_name: 'app',
  request_count: value,
  input_tokens: value,
  output_tokens: value,
  cache_hit_tokens: value / 2,
  cache_write_tokens: value / 4,
  cost_usd: value,
});

const sum = (rows: Consumption[], field: keyof Consumption): number =>
  rows.reduce((total, row) => total + Number(row[field]), 0);

describe('prorateStatsConsumptions', () => {
  it('clips current and previous views independently at a shared boundary hour', () => {
    const since = dayjs('2026-08-13T14:30:00');
    const until = dayjs('2026-08-13T16:15:00');
    const previousSince = dayjs('2026-08-13T12:45:00');

    const current = prorateStatsConsumptions(
      [consumption('2026-08-13T13:00:00'), consumption('2026-08-13T14:00:00'), consumption('2026-08-13T15:00:00'), consumption('2026-08-13T16:00:00')],
      since,
      until,
      until,
      'hour',
    );
    const previous = prorateStatsConsumptions(
      [consumption('2026-08-13T12:00:00'), consumption('2026-08-13T13:00:00'), consumption('2026-08-13T14:00:00')],
      previousSince,
      since,
      until,
      'hour',
    );

    expect(current.map(row => row.hour_bucket)).toEqual([
      '2026-08-13T14:00:00',
      '2026-08-13T15:00:00',
      '2026-08-13T16:00:00',
    ]);
    expect(previous.map(row => row.hour_bucket)).toEqual([
      '2026-08-13T12:00:00',
      '2026-08-13T13:00:00',
      '2026-08-13T14:00:00',
    ]);
    expect(current.find(row => row.hour_bucket.endsWith('14:00:00'))?.cost_usd).toBe(30);
    expect(previous.find(row => row.hour_bucket.endsWith('14:00:00'))?.cost_usd).toBe(30);
    expect(sum(current, 'cost_usd') + sum(previous, 'cost_usd')).toBe(255);
  });

  it('removes non-overlapping rows and scales every additive field on a boundary row', () => {
    const since = dayjs('2026-08-13T14:30:00');
    const until = dayjs('2026-08-13T15:00:00');
    const output = prorateStatsConsumptions(
      [
        consumption('2026-08-13T13:00:00', 99),
        consumption('2026-08-13T14:00:00', 60),
        consumption('2026-08-13T15:00:00', 99),
      ],
      since,
      until,
      until,
      'hour',
    );

    expect(output).toHaveLength(1);
    expect(output[0]).toMatchObject({
      hour_bucket: '2026-08-13T14:00:00',
      request_count: 30,
      input_tokens: 30,
      output_tokens: 30,
      cache_hit_tokens: 15,
      cache_write_tokens: 7.5,
      cost_usd: 30,
    });
  });
});
