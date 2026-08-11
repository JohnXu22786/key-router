import React, { useEffect, useState, useCallback } from 'react';
import { Typography, Segmented, Space, Button, Spin } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import ActivityOverview from './ActivityOverview';
import ActivityTrends from './ActivityTrends';
import ActivityExplore from './ActivityExplore';
// Shared helpers live in activityShared.ts (NOT here) to avoid a circular
// import: Activity -> ActivityOverview -> activityShared. A cycle can hand
// the children undefined constants and crash the page.
import { RANGES } from './activityShared';
export { fmtCompact, fmtUSD, fmtTokens, fmtPercent, GRID, AXIS, CHART_COLORS, OTHER_COLOR, fmt3sig } from './activityShared';
export type { DateRange } from './activityShared';

const { Title } = Typography;

export type TabKey = 'overview' | 'trends' | 'explore';

const Activity: React.FC = () => {
  const [tab, setTab] = useState<TabKey>('overview');
  const [rangeIdx, setRangeIdx] = useState(4); // default 1mo like the saved page
  const [refreshKey, setRefreshKey] = useState(0);
  const [loading, setLoading] = useState(false);

  const range = RANGES[rangeIdx];

  const handleRefresh = useCallback(async () => {
    setLoading(true);
    setRefreshKey(k => k + 1);
    await new Promise(r => setTimeout(r, 50));
    setLoading(false);
  }, []);

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
