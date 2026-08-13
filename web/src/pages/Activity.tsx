import React, { useEffect, useState, useCallback, useMemo } from 'react';
import { Typography, Space, Button, Spin, Select, DatePicker, Segmented } from 'antd';
import { ReloadOutlined, CalendarOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import ActivityOverview from './ActivityOverview';
import ActivityTrends from './ActivityTrends';
import ActivityExplore from './ActivityExplore';
// Shared helpers live in activityShared.ts (NOT here) to avoid a circular
// import: Activity -> ActivityOverview -> activityShared. A cycle can hand
// the children undefined constants and crash the page.
import { makeRanges, customRange, CUSTOM_KEY, CUSTOM_LABEL } from './activityShared';
import type { DateRange } from './activityShared';
export { fmtCompact, fmtUSD, fmtTokens, fmtPercent, GRID, AXIS, CHART_COLORS, OTHER_COLOR, fmt3sig } from './activityShared';
export type { DateRange } from './activityShared';

const { Title, Text } = Typography;

export type TabKey = 'overview' | 'trends' | 'explore';

// Chip: the small rounded badge (1mo, 4d, 16h) shown next to every preset,
// exactly like OpenRouter's date-range picker chips.
const Chip: React.FC<{ children: React.ReactNode }> = ({ children }) => (
  <span style={{
    display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
    minWidth: 34, padding: '0 6px', height: 18, borderRadius: 6,
    background: 'rgba(120,120,140,0.12)', color: 'rgba(120,120,140,0.9)',
    fontSize: 11, fontWeight: 500, fontVariantNumeric: 'tabular-nums', marginRight: 8,
  }}>
    {children}
  </span>
);

const rangeText = (r: DateRange): string =>
  `${r.since.format('MMM D, h:mm a')} – ${r.until.format('MMM D, h:mm a')}`;

interface OptionType { value: string; label: React.ReactNode; disabled?: boolean; isHeader?: boolean; }

const Activity: React.FC = () => {
  const [tab, setTab] = useState<TabKey>('overview');
  const [rangeKey, setRangeKey] = useState('1mo'); // default like the saved page
  const [custom, setCustom] = useState<{ since: dayjs.Dayjs; until: dayjs.Dayjs } | null>(null);
  const [loading, setLoading] = useState(false);
  // "now" is a state so Refresh recomputes the date windows to the current
  // moment — otherwise the ranges freeze at page-load time and the data
  // never updates. Each change slides the range and re-fetches in place
  // (the pages below keep the previous data visible while refreshing).
  const [now, setNow] = useState(() => dayjs());
  const ranges = useMemo(() => makeRanges(now), [now]);

  const range = useMemo<DateRange>(() => {
    if (rangeKey === CUSTOM_KEY && custom) return customRange(custom.since, custom.until);
    return ranges.find(r => r.key === rangeKey) ?? ranges[7]; // fallback 1mo
  }, [rangeKey, custom, ranges]);

  const handleRefresh = useCallback(async () => {
    setLoading(true);
    setNow(dayjs()); // slide the windows to now
    await new Promise(r => setTimeout(r, 50));
    setLoading(false);
  }, []);

  // Keep the displayed range fresh: slide "now" every 30s (the window
  // presets and the queries are derived from it) and when switching tabs.
  useEffect(() => {
    const t = setInterval(() => setNow(dayjs()), 30000);
    return () => clearInterval(t);
  }, []);
  useEffect(() => { setNow(dayjs()); }, [tab]);

  const onTabChange = (k: string) => setTab(k as TabKey);

  // Picker options, in OpenRouter's order: rolling windows, calendar
  // windows, then the custom range.
  const options: OptionType[] = useMemo(() => [
    { value: '__grp_rolling', label: 'Rolling', isHeader: true, disabled: true },
    ...ranges.slice(0, 9).map(r => ({ value: r.key, label: <span style={{ display: 'inline-flex', alignItems: 'center' }}><Chip>{r.badge}</Chip>{r.label}</span> })),
    { value: '__grp_calendar', label: 'Calendar', isHeader: true, disabled: true },
    ...ranges.slice(9).map(r => ({ value: r.key, label: <span style={{ display: 'inline-flex', alignItems: 'center' }}><Chip>{r.badge}</Chip>{r.label}</span> })),
    { value: CUSTOM_KEY, label: <span style={{ display: 'inline-flex', alignItems: 'center' }}><Chip><CalendarOutlined style={{ fontSize: 11 }} /></Chip>{CUSTOM_LABEL}…</span> },
  ], [ranges]);

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8, flexWrap: 'wrap', gap: 8 }}>
        <div>
          <Title level={3} style={{ margin: 0 }}>Activity</Title>
          <Text type="secondary" style={{ fontSize: 12 }}>
            Your usage across models on KeyRouter
          </Text>
        </div>
        <Space wrap>
          <Select
            value={rangeKey}
            onChange={(v: string) => {
              if (v === CUSTOM_KEY) {
                // Entering custom mode immediately applies a default window
                // (last month) so the charts and the trigger stay consistent
                // until the user picks their own dates.
                setCustom(custom ?? { since: now.subtract(1, 'month'), until: now });
              }
              setRangeKey(v);
            }}
            style={{ minWidth: 300 }}
            popupMatchSelectWidth={false}
            options={options}
            optionRender={(option) => {
              // rc-select's optionRender receives { data, label, value, ... } —
              // custom props live on option.data, not on the wrapper.
              const o = ((option as any).data ?? option) as OptionType;
              if (o.isHeader) {
                return <div style={{ fontSize: 11, color: 'rgba(120,120,140,0.75)', padding: '4px 12px 2px', textTransform: 'uppercase', letterSpacing: '0.04em' }}>{o.label}</div>;
              }
              return o.label as React.ReactNode;
            }}
            labelRender={() => (
              <span style={{ display: 'inline-flex', alignItems: 'center' }}>
                {rangeKey === CUSTOM_KEY
                  ? <Chip><CalendarOutlined style={{ fontSize: 11 }} /></Chip>
                  : <Chip>{range.badge}</Chip>}
                <span className="tabular-nums">{rangeText(range)}</span>
              </span>
            )}
          />
          {rangeKey === CUSTOM_KEY && (
            <DatePicker.RangePicker
              showTime
              value={custom ? [custom.since, custom.until] : [now.subtract(1, 'month'), now]}
              onChange={(v) => {
                if (v && v[0] && v[1]) setCustom({ since: v[0], until: v[1] });
              }}
              style={{ maxWidth: 400 }}
            />
          )}
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

      {/* No key= remount: pages re-fetch on range change and keep the
          previous content visible while refreshing — a refresh must never
          blank the page. */}
      {tab === 'overview' && <ActivityOverview range={range} />}
      {tab === 'trends' && <ActivityTrends range={range} />}
      {tab === 'explore' && <ActivityExplore range={range} />}
    </div>
  );
};

export default Activity;
