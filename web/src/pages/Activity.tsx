import React, { useEffect, useState, useCallback } from 'react';
import { Typography, Segmented, Space, Button, Spin, message } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import ActivityOverview from './ActivityOverview';
import ActivityTrends from './ActivityTrends';
import ActivityExplore from './ActivityExplore';

const { Title } = Typography;

// Date presets matching OpenRouter's Activity page (from the saved page):
// 1h / 24h / 7d / 1mo with a from/to range shown in the UI.
export interface DateRange {
  key: string;
  label: string;
  since: dayjs.Dayjs;
  until: dayjs.Dayjs;
}

export const RANGES: DateRange[] = [
  { key: 'today', label: 'Today', since: dayjs().startOf('day'), until: dayjs() },
  { key: '24h', label: '24h', since: dayjs().subtract(24, 'hour'), until: dayjs() },
  { key: '3d', label: '3 days', since: dayjs().subtract(3, 'day'), until: dayjs() },
  { key: '7d', label: '7 days', since: dayjs().subtract(7, 'day'), until: dayjs() },
  { key: '1mo', label: '1 month', since: dayjs().subtract(1, 'month'), until: dayjs() },
];

export const fmtCompact = (v: number): string => {
  const abs = Math.abs(v);
  if (abs >= 1e12) return (v / 1e12).toFixed(2).replace(/\.?0+$/, '') + 'T';
  if (abs >= 1e9) return (v / 1e9).toFixed(2).replace(/\.?0+$/, '') + 'B';
  if (abs >= 1e6) return (v / 1e6).toFixed(2).replace(/\.?0+$/, '') + 'M';
  if (abs >= 1e3) return (v / 1e3).toFixed(1).replace(/\.?0$/, '') + 'k';
  return String(Math.round(v));
};

export const fmtUSD = (v: number): string => {
  const abs = Math.abs(v);
  if (abs >= 1e9) return `$${(v / 1e9).toFixed(2)}B`;
  if (abs >= 1e6) return `$${(v / 1e6).toFixed(2)}M`;
  if (abs >= 1e3) return `$${(v / 1e3).toFixed(1)}k`;
  return `$${v.toFixed(2)}`;
};

export const fmtTokens = (v: number): string => fmtCompact(v) + ' tok';

// fmtPercent renders a percentage with few digits: >=100 → integer,
// >=1 → 1 decimal, else 2 decimals. Storage keeps full precision; only the
// display is shortened (nobody reads 0.03149166666666666%).
export const fmtPercent = (v: number): string => {
  if (v >= 100) return `${Math.round(v)}%`;
  if (v >= 1) return `${v.toFixed(1)}%`;
  return `${v.toFixed(2)}%`;
};

export const GRID = 'rgba(120,120,140,0.14)';
export const AXIS = 'rgba(120,120,140,0.75)';
// OpenRouter brand palette (bright accents + deep neutral)
export const CHART_COLORS = ['#6d5cff', '#22c1a3', '#ffb020', '#ff5f6d', '#3b82f6', '#a855f7', '#14b8a6', '#f59e0b', '#64748b'];

export type TabKey = 'overview' | 'trends' | 'explore';

const Activity: React.FC = () => {
  const [tab, setTab] = useState<TabKey>('overview');
  const [rangeIdx, setRangeIdx] = useState(4); // default 1mo like the saved page
  const [refreshKey, setRefreshKey] = useState(0);
  const [loading, setLoading] = useState(false);

  const range = RANGES[rangeIdx];

  const handleRefresh = useCallback(async () => {
    setLoading(true);
    // Bump the key: children refetch with the shared range.
    setRefreshKey(k => k + 1);
    // Small delay so the spinner is visible; children do the real fetch.
    await new Promise(r => setTimeout(r, 50));
    setLoading(false);
  }, []);

  // When the range changes, children refetch via refreshKey.
  useEffect(() => { setRefreshKey(k => k + 1); }, [rangeIdx]);

  const onTabChange = (k: string) => setTab(k as TabKey);

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8, flexWrap: 'wrap', gap: 8 }}>
        <div>
          <Title level={3} style={{ margin: 0 }}>Activity</Title>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            Your usage across models on KeyRouter · {range.since.format('MMM D, h:mm a')} – {range.until.format('MMM D, h:mm a')}
          </Typography.Text>
        </div>
        <Space wrap>
          <Segmented
            options={RANGES.map((r, i) => ({ label: r.label, value: i }))}
            value={rangeIdx}
            onChange={(v) => setRangeIdx(v as number)}
          />
          <Button icon={<ReloadOutlined />} loading={loading} onClick={handleRefresh}>Refresh</Button>
        </Space>
      </div>

      <Segmented
        block
        style={{ marginBottom: 16 }}
        value={tab}
        onChange={onTabChange}
        options={[
          { label: 'Overview', value: 'overview' },
          { label: 'Trends', value: 'trends' },
          { label: 'Explore', value: 'explore' },
        ]}
      />

      {loading && <Spin style={{ display: 'block', margin: '40px auto' }} />}

      {tab === 'overview' && <ActivityOverview key={`ov-${refreshKey}`} range={range} />}
      {tab === 'trends' && <ActivityTrends key={`tr-${refreshKey}`} range={range} />}
      {tab === 'explore' && <ActivityExplore key={`ex-${refreshKey}`} range={range} />}
    </div>
  );
};

export default Activity;
