import React, { useEffect, useState } from 'react';
import { Card, Row, Col, Typography, Spin, message, Progress, Space, Button } from 'antd';
import { getActivity, getKeys, Key, ActivityResponse } from '../api/client';
import { DateRange, fmtUSD, fmtTokens, fmtCompact, CHART_COLORS, GRID, AXIS } from './Activity';
import dayjs from 'dayjs';

const { Text } = Typography;

interface OverviewProps { range: DateRange; }

// KPI delta: current period vs the previous equal-length period.
const deltaPct = (cur: number, prev: number) =>
  prev > 0 ? ((cur - prev) / prev) * 100 : (cur > 0 ? 100 : 0);

const ActivityOverview: React.FC<OverviewProps> = ({ range }) => {
  const [cur, setCur] = useState<ActivityResponse | null>(null);
  const [prev, setPrev] = useState<ActivityResponse | null>(null);
  const [keys, setKeys] = useState<Key[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  // Top API Keys: from a dedicated key-grouped fetch (declared BEFORE any
  // conditional return — hooks order must be stable across renders).
  const [keyGroups, setKeyGroups] = useState<ActivityResponse | null>(null);
  const [appGroups, setAppGroups] = useState<ActivityResponse | null>(null);
  useEffect(() => {
    let cancelled = false;
    getActivity({ metric: 'tokens', group_by: 'key', rollup: 'day', since: range.since.toISOString(), until: range.until.toISOString() })
      .then(res => { if (!cancelled) setKeyGroups(res.data); })
      .catch(() => {});
    return () => { cancelled = true; };
  }, [range]);
  useEffect(() => {
    let cancelled = false;
    getActivity({ metric: 'tokens', group_by: 'app', rollup: 'day', since: range.since.toISOString(), until: range.until.toISOString() })
      .then(res => { if (!cancelled) setAppGroups(res.data); })
      .catch(() => {});
    return () => { cancelled = true; };
  }, [range]);

  useEffect(() => {
    let cancelled = false;
    const fetch = async () => {
      setLoading(true);
      setError(false);
      try {
        const len = range.until.diff(range.since, 'millisecond');
        const prevSince = range.since.subtract(len, 'millisecond');
        const [curRes, prevRes, keyRes] = await Promise.all([
          getActivity({ metric: 'spend', group_by: 'model', rollup: 'day', since: range.since.toISOString(), until: range.until.toISOString() }),
          getActivity({ metric: 'spend', group_by: 'model', rollup: 'day', since: prevSince.toISOString(), until: range.since.toISOString() }),
          getKeys(),
        ]);
        if (cancelled) return;
        setCur(curRes.data);
        setPrev(prevRes.data);
        setKeys(keyRes.data);
      } catch { if (!cancelled) { setError(true); message.error('Failed to load activity'); } }
      finally { if (!cancelled) setLoading(false); }
    };
    fetch();
    return () => { cancelled = true; };
  }, [range]);

  if (loading) return <Spin style={{ display: 'block', margin: '60px auto' }} />;
  if (error || !cur || !prev) {
    return <Card><Text type="danger">Failed to load activity — check the log file.</Text></Card>;
  }

  const curTotals = cur.totals;
  const prevTotals = prev.totals;
  const cacheRate = (tot: { cache: number; tokens: number }) =>
    tot.tokens > 0 ? ((tot.cache / tot.tokens) * 100).toFixed(1) : '0.0';
  // Blended $/1M = spend / total tokens * 1e6
  const blended = curTotals.tokens > 0 ? (curTotals.spend / curTotals.tokens) * 1e6 : 0;
  const blendedPrev = prevTotals.tokens > 0 ? (prevTotals.spend / prevTotals.tokens) * 1e6 : 0;

  const kpis = [
    { label: 'Total spend', value: fmtUSD(curTotals.spend), delta: deltaPct(curTotals.spend, prevTotals.spend) },
    { label: 'Requests', value: fmtCompact(curTotals.requests), delta: deltaPct(curTotals.requests, prevTotals.requests) },
    { label: 'Token volume', value: fmtTokens(curTotals.tokens), delta: deltaPct(curTotals.tokens, prevTotals.tokens) },
    { label: 'Cache hit rate', value: `${cacheRate(curTotals)}%`, delta: deltaPct(curTotals.cache, prevTotals.cache) },
    { label: 'Blended $/1M', value: `$${blended.toFixed(2)}`, delta: deltaPct(blended, blendedPrev) },
  ];

  // Top API Keys: from a dedicated key-grouped fetch.
  const topKeys = (keyGroups?.summary ?? []).slice(0, 5);
  const maxKeyTokens = topKeys.length ? Math.max(...topKeys.map(k => k.sum)) : 1;

  // Top Apps: grouped by the X-App request header ("" = Unknown).
  const topApps = (appGroups?.summary ?? []).slice(0, 5);
  const maxAppTokens = topApps.length ? Math.max(...topApps.map(k => k.sum)) : 1;

  const keyPrefixFor = (name: string) => {
    const k = keys.find(x => x.name === name);
    if (!k) return '';
    const v = k.key_value || '';
    return v.length > 12 ? `${v.slice(0, 12)}...` : v;
  };

  return (
    <div>
      {/* KPI cards with vs prev period chips */}
      <Row gutter={[16, 16]} style={{ marginBottom: 16 }}>
        {kpis.map(k => (
          <Col xs={12} sm={8} lg={4} key={k.label}>
            <Card size="small" style={{ borderRadius: 12 }}>
              <Text type="secondary" style={{ fontSize: 12 }}>{k.label}</Text>
              <div style={{ fontSize: 20, fontWeight: 600, margin: '4px 0' }}>{k.value}</div>
              <Text style={{ fontSize: 12, color: k.delta >= 0 ? '#22c1a3' : '#ff5f6d' }}>
                {k.delta >= 0 ? '▲' : '▼'} {Math.abs(k.delta).toFixed(1)}%
                <Text type="secondary" style={{ fontSize: 12 }}> vs prev period</Text>
              </Text>
            </Card>
          </Col>
        ))}
      </Row>

      {/* Top API Keys + Top Apps (OpenRouter Overview panels) */}
      <Row gutter={[16, 16]}>
        <Col xs={24} lg={12}>
          <Card
            style={{ borderRadius: 12 }}
            title="Top API Keys"
            extra={<Text type="secondary" style={{ fontSize: 12 }}>by tokens</Text>}
          >
            {topKeys.length === 0 && <Text type="secondary">No usage in this period.</Text>}
            {topKeys.map((k, idx) => (
              <div key={k.group} style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '6px 0' }}>
                <Text type="secondary" style={{ width: 20, textAlign: 'right' }}>{idx + 1}</Text>
                <div style={{ flex: 1 }}>
                  <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                    <Text strong>{k.group}</Text>
                    <Text type="secondary" style={{ fontSize: 12 }}>{keyPrefixFor(k.group)}</Text>
                  </div>
                  <Progress
                    percent={Math.min(100, (k.sum / maxKeyTokens) * 100)}
                    showInfo={false}
                    strokeColor={CHART_COLORS[idx % CHART_COLORS.length]}
                    trailColor={GRID}
                    size={{ height: 6 }}
                  />
                </div>
                <Text strong style={{ width: 90, textAlign: 'right' }}>{fmtTokens(k.sum)}</Text>
              </div>
            ))}
          </Card>
        </Col>
        <Col xs={24} lg={12}>
          <Card
            style={{ borderRadius: 12 }}
            title="Top Apps"
            extra={<Text type="secondary" style={{ fontSize: 12 }}>by tokens</Text>}
          >
            {topApps.length === 0 && <Text type="secondary">No usage in this period.</Text>}
            {topApps.map((k, idx) => (
              <div key={k.group} style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '6px 0' }}>
                <Text type="secondary" style={{ width: 20, textAlign: 'right' }}>{idx + 1}</Text>
                <div style={{ flex: 1 }}>
                  <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                    <Text strong>{k.group}</Text>
                  </div>
                  <Progress
                    percent={Math.min(100, (k.sum / maxAppTokens) * 100)}
                    showInfo={false}
                    strokeColor={CHART_COLORS[(idx + 3) % CHART_COLORS.length]}
                    trailColor={GRID}
                    size={{ height: 6 }}
                  />
                </div>
                <Text strong style={{ width: 90, textAlign: 'right' }}>{fmtTokens(k.sum)}</Text>
              </div>
            ))}
          </Card>
        </Col>
      </Row>
    </div>
  );
};

export default ActivityOverview;
