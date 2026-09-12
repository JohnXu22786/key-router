import type { Dayjs } from 'dayjs';
import type { Consumption } from '../api/client';
import { bucketWindowShare } from './activityShared';
import type { Granularity } from './activityShared';

type StatsGranularity = Extract<Granularity, 'hour' | 'day'>;

const scaleConsumption = (row: Consumption, share: number): Consumption => ({
  ...row,
  request_count: row.request_count * share,
  input_tokens: row.input_tokens * share,
  output_tokens: row.output_tokens * share,
  cache_hit_tokens: row.cache_hit_tokens * share,
  cache_write_tokens: row.cache_write_tokens * share,
  cost_usd: row.cost_usd * share,
});

// The raw consumptions endpoint widens each query to include the hourly rows
// containing its bounds. Trim those rows before any Stats aggregation so the
// current and previous half-open windows can share a boundary hour without
// counting the same usage twice.
export function prorateStatsConsumptions(
  rows: Consumption[],
  since: Dayjs,
  until: Dayjs,
  cutoff: Dayjs,
  granularity: StatsGranularity,
): Consumption[] {
  return rows.flatMap(row => {
    const share = bucketWindowShare(row.hour_bucket, since, until, cutoff, granularity);
    return share > 0 ? [share === 1 ? row : scaleConsumption(row, share)] : [];
  });
}
