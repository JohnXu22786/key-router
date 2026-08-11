import React, { useEffect, useState, useCallback, useMemo } from 'react';
import { Typography, Segmented, Space, Button, Spin } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import ActivityOverview from './ActivityOverview';
import ActivityTrends from './ActivityTrends';
import ActivityExplore from './ActivityExplore';
// Shared helpers live in activityShared.ts (NOT here) to avoid a circular
// import: Activity -> ActivityOverview -> activityShared. A cycle can hand
// the children undefined constants and crash the page.
import { makeRanges } from './activityShared';
export { fmtCompact, fmtUSD, fmtTokens, fmtPercent, GRID, AXIS, CHART_COLORS, OTHER_COLOR, fmt3sig, RANGES } from './activityShared';
export type { DateRange } from './activityShared';

const { Title } = Typography;

export type TabKey = 'overview' | 'trends' | 'explore';

const Activity: React.FC = () => {
  const [tab, setTab] = useState<TabKey>('overview');
  const [rangeIdx, setRangeIdx] = useState(4); // default 1mo like the saved page
  const [refreshKey, setRefreshKey] = useState(0);
  const [loading, setLoading] = useState(false);
  // "now" is a state so Refresh recomputes the date window to the current
  // moment — otherwise the ranges freeze at page-load time and the data
  // never updates.
  const [now, setNow] = useState(() => dayjs());
  const ranges = useMemo(() => makeRanges(now), [now]);
  const range = ranges[rangeIdx];

  const handleRefresh = useCallback(async () => {
    setLoading(true);
    setNow(dayjs()); // slide the window to now
    setRefreshKey(k => k + 1);
    await new Promise(r => setTimeout(r, 50));
    setLoading(false);
  }, []);

  // Keep the displayed range fresh: slide "now" every minute (the range
  // header and the queries are derived from it) and when switching tabs.
  useEffect(() => {
    const t = setInterval(() => setNow(dayjs()), 60000);
    return () => clearInterval(t);
  }, []);
  useEffect(() => { setNow(dayjs()); }, [tab]);
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
            options={ranges.map((r, i) => ({ label: r.label, value: i }))}
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
