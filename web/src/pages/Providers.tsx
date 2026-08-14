import React, { useEffect, useState, useCallback, useRef } from 'react';
import {
  Table, Button, Modal, Form, Input, InputNumber, Select, message, Space,
  Typography, Popconfirm, Tag, Descriptions, Progress, Collapse,
} from 'antd';
import {
  PlusOutlined, EditOutlined, DeleteOutlined, EyeOutlined, HolderOutlined,
} from '@ant-design/icons';
import {
  getProviders, createProvider, updateProvider, deleteProvider,
  getKeys, createKey, updateKey, deleteKey, reorderKeys, getKeyDetail, resetKeySpend,
  getRoutes, Provider, Key, Route,
} from '../api/client';
import { subscribeEvents, jsonEqual } from '../api/events';
import { useDragSort } from '../hooks/useDragSort';
import { usdToMicroUsd, microUsdToUsd } from './keyLimits';

const { Title, Text } = Typography;

const statusColors: Record<string, string> = {
  active: 'green', rate_limited: 'orange', disabled: 'red', testing: 'blue',
};

const reasonLabels: Record<string, string> = {
  auth_failed: 'Auth failed',
  insufficient_quota: 'Insufficient quota',
  rate_limited: 'Rate limited',
  upstream_error: 'Upstream error',
  spend_limit_exhausted: 'Spend budget exhausted',
};

const metricOptions = [
  { value: 'requests', label: 'Requests' },
  { value: 'tokens', label: 'Tokens' },
  { value: 'cost', label: 'Cost (USD)' },
];

const windowTypes = [
  { key: 'rpm', label: 'RPM', limitField: 'rpm_limit' },
  { key: 'tpm', label: 'TPM', limitField: 'tpm_limit' },
  { key: 'rp5h', label: '5 Hour', limitField: 'rp5h_limit', metricField: 'rp5h_metric' },
  { key: 'rpd', label: 'Daily', limitField: 'rpd_limit', metricField: 'rpd_metric' },
  { key: 'rpw', label: 'Weekly', limitField: 'rpw_limit', metricField: 'rpw_metric' },
  { key: 'rpmo', label: 'Monthly', limitField: 'rpm_month_limit', metricField: 'rpm_metric' },
];

// fmtPercent: short display, full-precision storage.
const fmtPercent = (v: number): string => {
  if (v >= 100) return `${Math.round(v)}%`;
  if (v >= 1) return `${v.toFixed(1)}%`;
  return `${v.toFixed(2)}%`;
};

const Providers: React.FC = () => {
  const [providers, setProviders] = useState<Provider[]>([]);
  const [keys, setKeys] = useState<Key[]>([]);
  const [routes, setRoutes] = useState<Route[]>([]);
  const [loading, setLoading] = useState(false);
  const [provModal, setProvModal] = useState(false);
  const [editingProv, setEditingProv] = useState<Provider | null>(null);
  const [keyModal, setKeyModal] = useState(false);
  const [editingKey, setEditingKey] = useState<Key | null>(null);
  const [detailOpen, setDetailOpen] = useState(false);
  const [detailData, setDetailData] = useState<any>(null);
  const [activeProviders, setActiveProviders] = useState<string[]>([]);
  const [provForm] = Form.useForm();
  const [keyForm] = Form.useForm();
  const statusTouched = useRef(false);
  // Number of drag orders committed but not yet persisted: the background
  // poll must not overwrite the local order with pre-persist server state.
  // Mirror (assigned during render, Models.tsx pattern) so the SSE handler
  // and poll — which run in []-dep effects — can read whether the detail
  // modal is open.
  const detailOpenRef = useRef(false);
  detailOpenRef.current = detailOpen;
  // The key the open detail modal is showing. The 5s poll refreshes that
  // modal so its Window Counters track the live sliding windows as the
  // buckets rotate (see refreshDetail below).
  const detailIdRef = useRef<number | null>(null);
  detailIdRef.current = detailData?.key?.id ?? null;
  // Coalescing for SSE-triggered refetches: a burst of status flips must
  // not launch N concurrent fetches whose out-of-order responses could
  // briefly revert the table. One fetch runs at a time; events arriving in
  // between just schedule one trailing re-run.
  const keysRefetchingRef = useRef(false);
  const keysRefetchAgainRef = useRef(false);
  const detailFetchingRef = useRef(false);
  const detailFetchAgainRef = useRef(false);
  // Captured at fetch START as well (with the persist generation), so a
  // poll that raced a commit or a persist is discarded even if the persist
  // settles before the poll response arrives.
  const pendingPersistsRef = useRef(0);
  const persistGenRef = useRef(0);

  // Drag-reorder with live preview animation (keys within a provider).
  const drag = useDragSort<Key>(
    keys,
    (from, to) => keys[from]?.provider_id === keys[to]?.provider_id,
    (next) => { setKeys(next); persistOrder(next); },
  );

  const fetch = async () => {
    setLoading(true);
    try {
      const [p, k, r] = await Promise.all([getProviders(), getKeys(), getRoutes()]);
      setProviders(p.data); setKeys(k.data); setRoutes(r.data);
    } catch { message.error('Failed to load providers'); }
    finally { setLoading(false); }
  };

  useEffect(() => { fetch(); }, []);

  // Refresh the open key-detail modal. Shared by the SSE push (key status
  // flips) and the 5s poll (window usage) — without the poll the modal's
  // Window Counters would freeze on screen: window budgets never flip a
  // key's status, so SSE alone never refetches them while they slide.
  // Skipped entirely while the modal is closed. Events/polls arriving
  // while a fetch is in flight schedule one trailing re-run, so no
  // update is dropped.
  const refreshDetail = useCallback((keyId: number) => {
    if (!detailOpenRef.current) return;
    if (detailFetchingRef.current) { detailFetchAgainRef.current = true; return; }
    detailFetchingRef.current = true;
    getKeyDetail(keyId).then(res => {
      detailFetchingRef.current = false;
      if (detailFetchAgainRef.current) {
        detailFetchAgainRef.current = false;
        // Re-run for the modal's CURRENT key: the call that queued this
        // re-run may have been for a key the user already switched away
        // from, and its response would be discarded by the id guard
        // below. No-ops via the detailOpenRef guard when the modal closed.
        refreshDetail(detailIdRef.current ?? keyId);
        return;
      }
      if (detailOpenRef.current) {
        setDetailData((prev: any) => (prev?.key?.id === keyId && !jsonEqual(prev, res.data) ? res.data : prev));
      }
    }).catch(() => { detailFetchingRef.current = false; });
  }, []);

  // Live push: the backend publishes key_status_changed over SSE the moment
  // the relay/health checker flips a key, so the table turns red at the
  // same time as the detail panel instead of waiting for the next poll.
  // Re-fetched data is applied only when it actually changed (jsonEqual) —
  // an unchanged response must not re-render the table.
  useEffect(() => {
    const refreshKeysOnly = () => {
      // Coalesce bursts: while a refetch is in flight, further events just
      // mark "run again at the end" — N flips must not launch N concurrent
      // fetches whose out-of-order responses could briefly revert the table.
      if (keysRefetchingRef.current) { keysRefetchAgainRef.current = true; return; }
      keysRefetchingRef.current = true;
      // Same race guards as the poll: a fetch that started while a drag
      // commit or persist was pending (or raced one) is discarded, or the
      // push would revert the UI to pre-persist order. The poll catches up.
      const wasPersisting = pendingPersistsRef.current > 0;
      const gen = persistGenRef.current;
      getKeys().then(res => {
        keysRefetchingRef.current = false;
        if (keysRefetchAgainRef.current) {
          keysRefetchAgainRef.current = false;
          refreshKeysOnly();
          return;
        }
        if (drag.draggingRef.current) return;
        if (!wasPersisting && pendingPersistsRef.current === 0 && gen === persistGenRef.current) {
          setKeys(prev => (jsonEqual(prev, res.data) ? prev : res.data));
        }
      }).catch(() => { keysRefetchingRef.current = false; });
    };
    return subscribeEvents(evt => {
      if (evt.type !== 'key_status_changed' || evt.key_id == null) return;
      refreshKeysOnly();
      refreshDetail(evt.key_id);
    });
  }, [refreshDetail]);

  // Poll for key status changes (relay/health checker flip keys as traffic
  // flows) as a fallback for anything a push event can miss (window usage,
  // dropped SSE connection). Keeps expanded groups and scroll position —
  // only row data updates, and only when it changed. The open detail modal
  // is refreshed on the same tick so its window counters (5h/daily/weekly/
  // monthly) keep auto-scrolling as the backend buckets rotate.
  useEffect(() => {
    const t = setInterval(() => {
      // A fetch that raced a commit or a persist may carry pre-persist data:
      // skip keys unless no persist was pending at fetch start AND none is
      // pending now AND the persist generation is unchanged, or the poll
      // would revert the UI until the next one.
      const wasPersisting = pendingPersistsRef.current > 0;
      const gen = persistGenRef.current;
      Promise.all([getProviders(), getKeys(), getRoutes()])
        .then(([p, k, r]) => {
          setProviders(prev => (jsonEqual(prev, p.data) ? prev : p.data));
          setRoutes(prev => (jsonEqual(prev, r.data) ? prev : r.data));
          // Skip keys while a drag is in progress: the drag commit splices
          // the array at pointerdown-era indices, so the array must not
          // change underneath it (the next poll catches up).
          if (!drag.draggingRef.current && !wasPersisting && pendingPersistsRef.current === 0 && gen === persistGenRef.current) {
            setKeys(prev => (jsonEqual(prev, k.data) ? prev : k.data));
          }
        })
        .catch(() => {});
      // No-op when the modal is closed (refreshDetail's first guard); the
      // coalescing refs keep concurrent SSE pushes from stacking fetches.
      if (detailIdRef.current != null) refreshDetail(detailIdRef.current);
    }, 5000);
    return () => clearInterval(t);
  }, [refreshDetail]);

  // ---- Provider CRUD ----
  const saveProvider = async () => {
    try {
      const values = await provForm.validateFields();
      if (editingProv) { await updateProvider(editingProv.id, values); message.success('Provider updated'); }
      else { await createProvider(values); message.success('Provider created'); }
      setProvModal(false); setEditingProv(null); provForm.resetFields(); fetch();
    } catch (err: any) { message.error(err?.message || 'Failed to save provider'); }
  };

  const deleteProvider = async (id: number) => {
    try {
      await deleteProvider(id); message.success('Provider deleted'); fetch();
    } catch { message.error('Failed to delete provider'); }
  };

  // ---- Key CRUD ----
  const saveKey = async () => {
    try {
      const values = await keyForm.validateFields();
      // Unit conversion: cost-metric windows and the lifetime budget are
      // entered in USD but stored in micro-USD (1e6 per $1). Convert on
      // `values` so BOTH the create and edit paths send stored units — the
      // create path sends `values` directly. (A missing conversion here once
      // stored a "$30" budget as 30 micro-USD, disabling the key the moment
      // its next request pushed total_spent past it.)
      for (const wt of windowTypes) {
        if (values[wt.limitField] == null || values[wt.limitField] === 0) continue;
        if (wt.metricField && values[wt.metricField] === 'cost') {
          values[wt.limitField] = usdToMicroUsd(values[wt.limitField]);
        }
      }
      if (values.total_spend_limit != null && values.total_spend_limit !== 0) {
        values.total_spend_limit = usdToMicroUsd(values.total_spend_limit);
      }
      // Editing must NOT reset fields the user did not touch: only send the
      // fields actually modified (rate-limit values, strategy, ...) so a
      // name-only edit leaves the limits exactly as they were. Creating
      // sends everything.
      const payload: any = { ...values };
      if (editingKey) {
        for (const key of Object.keys(values)) {
          if (!keyForm.isFieldTouched(key)) delete payload[key];
        }
        if (!statusTouched.current) delete payload.status;
        if (payload.provider_id == null) delete payload.provider_id;
      }
      if (editingKey) { await updateKey(editingKey.id, payload); message.success('Key updated'); }
      else { await createKey(values); message.success('Key created'); }
      statusTouched.current = false;
      setKeyModal(false); setEditingKey(null); keyForm.resetFields(); fetch();
    } catch { message.error('Failed to save key'); }
  };

  const deleteKey = async (id: number) => {
    try {
      await deleteKey(id); message.success('Key deleted'); fetch();
    } catch { message.error('Failed to delete key'); }
  };

  const showDetail = async (key: Key) => {
    try {
      const res = await getKeyDetail(key.id);
      setDetailData(res.data);
      setDetailOpen(true);
    } catch { message.error('Failed to load key details'); }
  };

  const openEditKey = (k: Key) => {
    setEditingKey(k);
    statusTouched.current = false;
    const values: any = { ...k, provider_id: k.provider_id };
    for (const wt of windowTypes) {
      if (wt.metricField) {
        const metric = (k as any)[wt.metricField];
        if (metric === 'cost') values[wt.limitField] = (k as any)[wt.limitField] / 1e6;
      }
    }
    // Lifetime budget is stored in micro-USD; the form's field is in USD.
    values.total_spend_limit = microUsdToUsd(k.total_spend_limit || 0);
    keyForm.setFieldsValue(values);
    setKeyModal(true);
  };

  const handleResetSpend = async (id: number) => {
    try {
      await resetKeySpend(id);
      message.success('Lifetime budget reset — key re-enabled');
      fetch();
    } catch { message.error('Failed to reset key spend'); }
  };
  const persistOrder = useCallback((ordered: Key[]) => {
    // Persist IMMEDIATELY on drop — no debounce: an edit must be written
    // the moment it happens, so a crash or a forced kill right after a drop
    // cannot lose the new order. Each drop fires one request; the poll guard
    // below keeps the refresh from overwriting the local order while the
    // write is in flight.
    pendingPersistsRef.current++;
    const providerCounts: Record<number, number> = {};
    const payload = ordered.map(k => {
      const idx = providerCounts[k.provider_id] ?? 0;
      providerCounts[k.provider_id] = idx + 1;
      return { id: k.id, sort_order: idx };
    });
    reorderKeys(payload)
      .catch(() => message.error('Failed to save order'))
      .finally(() => {
        pendingPersistsRef.current--;
        persistGenRef.current++;
      });
  }, []);

  const keyColumns = [
    {
      title: '', key: 'drag', width: 40,
      render: () => <span data-drag-handle style={{ cursor: 'grab', display: 'inline-block', touchAction: 'none' }}><HolderOutlined style={{ color: '#999' }} /></span>,
    },
    {
      title: 'Name', dataIndex: 'name', key: 'name',
      render: (n: string, r: Key) => (
        <Space size={6}>
          <Text strong>{n || '(unnamed)'}</Text>
          <Text type="secondary" style={{ fontSize: 12 }}>{r.key_value?.substring(0, 12)}...</Text>
        </Space>
      ),
    },
    {
      title: 'Status', dataIndex: 'status', key: 'status',
      render: (s: string, r: Key) => (
        <Space size={4}>
          <Tag color={statusColors[s] || 'default'}>{s}</Tag>
          {s === 'disabled' && r.disabled_reason && (
            <Tag color="red">{reasonLabels[r.disabled_reason] || r.disabled_reason}</Tag>
          )}
          {/* Only for active keys: a rate_limited/disabled key already has
              a tag explaining its state — this one explains why an ACTIVE
              key is being skipped (window budget exhausted). */}
          {s === 'active' && r.limited_windows && r.limited_windows.length > 0 && (
            <Tag
              color="orange"
              title="Window budget(s) exhausted — this key is skipped until usage drops below the limit"
            >
              Limit hit: {r.limited_windows.map(w => windowTypes.find(t => t.key === w)?.label ?? w).join(', ')}
            </Tag>
          )}
        </Space>
      ),
    },
    { title: 'Strategy', dataIndex: 'recovery_strategy', key: 'recovery_strategy', width: 90 },
    {
      title: 'Actions', key: 'actions', width: 120,
      render: (_: unknown, r: Key) => (
        <Space>
          <Button icon={<EyeOutlined />} size="small" onClick={() => showDetail(r)} title="Detail" />
          <Button icon={<EditOutlined />} size="small" onClick={() => openEditKey(r)} title="Edit" />
          <Popconfirm title="Delete?" onConfirm={() => deleteKey(r.id)}>
            <Button icon={<DeleteOutlined />} size="small" danger title="Delete" />
          </Popconfirm>
        </Space>
      ),
    },
  ];

  // Lifetime-budget figures for the detail panel (micro-USD from the API).
  const budget = detailData?.key
    ? { spent: detailData.key.total_spent || 0, limit: detailData.key.total_spend_limit || 0 }
    : null;

  return (
    <div>
      <Title level={3}>Providers</Title>
      <Typography.Paragraph type="secondary">
        Each provider owns its API keys (a key belongs to exactly one provider). Expand a
        provider to manage its keys — drag to reorder (order = call order), set rate limits
        and recovery strategy per key. Limits apply only to traffic through KeyRouter.
      </Typography.Paragraph>
      {/* Both add buttons live at the top (per-page style). Add Key
          pre-selects the first expanded provider, else the first provider. */}
      <Space style={{ marginBottom: 16 }}>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => { setEditingProv(null); provForm.resetFields(); setProvModal(true); }}>
          Add Provider
        </Button>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => {
          setEditingKey(null); keyForm.resetFields();
          const firstId = activeProviders.length
            ? parseInt(activeProviders[0], 10)
            : (providers[0]?.id || undefined);
          keyForm.setFieldsValue({ provider_id: firstId, recovery_strategy: 'lazy', status: 'active' });
          setKeyModal(true);
        }}>
          Add Key
        </Button>
      </Space>

      {providers.length === 0 && !loading && (
        <Typography.Text type="secondary">No providers yet. Add a provider to get started.</Typography.Text>
      )}

      {providers.map(p => {
        const provKeys = keys.filter(k => k.provider_id === p.id);
        return (
          <Collapse
            key={p.id}
            style={{ marginBottom: 12 }}
            activeKey={activeProviders}
            onChange={(k: any) => setActiveProviders(Array.isArray(k) ? k.map(String) : [k].map(String))}
            items={[{
              key: String(p.id),
              label: (
                <Space>
                  <Text strong>{p.name}</Text>
                  <Tag>{({ openai: 'OpenAI', anthropic: 'Anthropic' })[p.type] || p.type}</Tag>
                  <Text type="secondary" style={{ fontSize: 12 }}>{p.base_url}</Text>
                  <Text type="secondary" style={{ fontSize: 12 }}>{provKeys.length} key{provKeys.length === 1 ? '' : 's'}</Text>
                  {routes.filter(r => r.provider_id === p.id && r.enabled).length === 0 && (
                    <Tag color="orange">no enabled routes</Tag>
                  )}
                </Space>
              ),
              extra: (
                <Space>
                  <Button size="small" icon={<EditOutlined />} onClick={(e) => { e.stopPropagation(); setEditingProv(p); provForm.setFieldsValue(p); setProvModal(true); }} title="Edit provider" />
                  <Popconfirm title="Delete provider?" onConfirm={() => deleteProvider(p.id)}>
                    <Button size="small" danger icon={<DeleteOutlined />} onClick={(e) => e.stopPropagation()} title="Delete provider" />
                  </Popconfirm>
                </Space>
              ),
              children: (
                <Table
                  dataSource={provKeys}
                  columns={keyColumns}
                  rowKey="id"
                  size="small"
                  pagination={false}
                  locale={{ emptyText: 'No keys yet — use the Add Key button above.' }}
                  onRow={(_, index) => {
                    const globalIndex = keys.indexOf(provKeys[index!]);
                    return {
                      style: { cursor: 'default', ...drag.rowStyle(globalIndex) },
                      onPointerDown: (e: React.PointerEvent) => drag.onPointerDown(e, globalIndex),
                      onPointerMove: drag.onPointerMove,
                      onPointerUp: drag.onPointerUp,
                      onPointerCancel: drag.onPointerCancel,
                    };
                  }}
                />
              ),
            }]}
          />
        );
      })}

      {/* Provider modal */}
      <Modal title={editingProv ? 'Edit Provider' : 'Add Provider'} open={provModal} onOk={saveProvider} onCancel={() => { setProvModal(false); setEditingProv(null); }}>
        <Form form={provForm} layout="vertical">
          <Form.Item name="name" label="Name" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="type" label="Type" rules={[{ required: true }]}>
            <Select options={[{ value: 'openai', label: 'OpenAI' }, { value: 'anthropic', label: 'Anthropic' }]} />
          </Form.Item>
          <Form.Item name="base_url" label="Base URL" rules={[{ required: true }]} extra="Do not include /v1 — it is appended automatically">
            <Input placeholder="https://api.openai.com" />
          </Form.Item>
          <Form.Item name="extra_headers" label="Extra Headers (JSON)">
            <Input.TextArea rows={3} placeholder='{"Organization":"org-xxx"}' />
          </Form.Item>
        </Form>
      </Modal>

      {/* Key modal */}
      <Modal title={editingKey ? 'Edit Key' : 'Add Key'} open={keyModal} onOk={saveKey} onCancel={() => { setKeyModal(false); setEditingKey(null); }} width={680}>
        <Form form={keyForm} layout="vertical" onValuesChange={(changed) => { if ('status' in changed) statusTouched.current = true; }}>
          <Form.Item name="provider_id" label="Provider" rules={[{ required: true }]}>
            <Select placeholder="Select a provider" showSearch options={providers.map(p => ({ value: p.id, label: `${p.name} (${p.type})` }))} />
          </Form.Item>
          <Form.Item name="name" label="Display Name"><Input placeholder="My OpenAI Key 1" /></Form.Item>
          <Form.Item name="key_value" label="API Key" rules={[{ required: true }]}><Input.Password placeholder="sk-..." /></Form.Item>
          <Form.Item name="status" label="Status" tooltip="Set to Disabled to take the key out of rotation manually. The relay and health checker manage this field automatically otherwise.">
            <Select options={[{ value: 'active', label: 'Active' }, { value: 'disabled', label: 'Disabled' }]} />
          </Form.Item>
          <Form.Item name="recovery_strategy" label="Recovery Strategy" initialValue="lazy" tooltip="Immediate keys are always preferred over Lazy keys regardless of the drag order; within the same strategy, drag order decides.">
            <Select options={[
              { value: 'immediate', label: 'Immediate — use as soon as recovered' },
              { value: 'lazy', label: 'Lazy — only use when no immediate keys available' },
            ]} />
          </Form.Item>
          <div style={{ marginTop: 8 }}>
            <Typography.Text strong>Rate Limits</Typography.Text>
            <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
              Leave a field empty to remove the limit (0 = unlimited). Windows use requests, tokens, or cost (USD).
            </Typography.Paragraph>
            {/* Window limits: fixed 3-column grid, no auto-wrap */}
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: '0 16px' }}>
              <Form.Item name="rpm_limit" label="RPM (requests)"><InputNumber min={0} placeholder="500" style={{ width: '100%' }} /></Form.Item>
              <Form.Item name="tpm_limit" label="TPM (tokens)"><InputNumber min={0} placeholder="200000" style={{ width: '100%' }} /></Form.Item>
              <div />{/* spacer for row 1 col 3 */}
              <Form.Item name="rp5h_limit" label="5-Hour Limit"><InputNumber min={0} placeholder="5000" style={{ width: '100%' }} /></Form.Item>
              <Form.Item name="rp5h_metric" label="5-Hour Metric"><Select style={{ width: '100%' }} options={metricOptions} /></Form.Item>
              <div />
              <Form.Item name="rpd_limit" label="Daily Limit"><InputNumber min={0} placeholder="10000" style={{ width: '100%' }} /></Form.Item>
              <Form.Item name="rpd_metric" label="Daily Metric"><Select style={{ width: '100%' }} options={metricOptions} /></Form.Item>
              <div />
              <Form.Item name="rpw_limit" label="Weekly Limit"><InputNumber min={0} placeholder="50000" style={{ width: '100%' }} /></Form.Item>
              <Form.Item name="rpw_metric" label="Weekly Metric"><Select style={{ width: '100%' }} options={metricOptions} /></Form.Item>
              <div />
              <Form.Item name="rpm_month_limit" label="Monthly Limit"><InputNumber min={0} placeholder="200000" style={{ width: '100%' }} /></Form.Item>
              <Form.Item name="rpm_metric" label="Monthly Metric"><Select style={{ width: '100%' }} options={metricOptions} /></Form.Item>
            </div>

            {/* Lifetime budget (spend cap) */}
            <Typography.Text strong style={{ display: 'block', marginTop: 12 }}>Lifetime Budget</Typography.Text>
            <Typography.Paragraph type="secondary" style={{ fontSize: 12, marginBottom: 8 }}>
              A one-time total spend cap (USD). Once the key has served this much cost it is disabled and stays
              disabled until you reset it. 0 = no budget.
            </Typography.Paragraph>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr auto', gap: 16, alignItems: 'end' }}>
              <Form.Item name="total_spend_limit" label="Total Budget (USD)">
                <InputNumber min={0} step={1} placeholder="50" style={{ width: '100%' }} />
              </Form.Item>
              {editingKey && (
                <Form.Item label="Spent">
                  <Text style={{ lineHeight: '32px', fontVariantNumeric: 'tabular-nums' }}>
                    ${(((editingKey.total_spent || 0) / 1e6).toFixed(2))}
                  </Text>
                </Form.Item>
              )}
              {editingKey && editingKey.total_spent > 0 && (
                <Popconfirm
                  title="Reset lifetime budget?"
                  description="This resets the spent amount to $0 and re-enables the key (undoes spend_limit_exhausted)."
                  okText="Reset"
                  okButtonProps={{ danger: true }}
                  onConfirm={() => handleResetSpend(editingKey.id)}
                >
                  <Button style={{ marginBottom: 24 }} danger>Reset Spent</Button>
                </Popconfirm>
              )}
            </div>
          </div>
        </Form>
      </Modal>

      {/* Key detail modal */}
      <Modal title={`Key Detail: ${detailData?.key?.name || ''}`} open={detailOpen} onCancel={() => setDetailOpen(false)} footer={null} width={700}>
        {detailData && (
          <div>
            <Descriptions column={2} bordered size="small">
              <Descriptions.Item label="Status">
                <Tag color={statusColors[detailData.key?.status] || 'default'}>{detailData.key?.status}</Tag>
                {detailData.key?.status === 'disabled' && detailData.key?.disabled_reason && (
                  <Tag color="red">{reasonLabels[detailData.key.disabled_reason] || detailData.key.disabled_reason}</Tag>
                )}
              </Descriptions.Item>
              <Descriptions.Item label="Strategy">{detailData.key?.recovery_strategy}</Descriptions.Item>
              <Descriptions.Item label="Provider">{detailData.key?.provider?.name}</Descriptions.Item>
              <Descriptions.Item label="Total Cost">${detailData.total_cost?.toFixed(6)}</Descriptions.Item>
            </Descriptions>
            <Title level={5} style={{ marginTop: 16 }}>Window Counters</Title>
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 16, maxWidth: 480 }}>
              {windowTypes.map(wt => {
                const c = detailData.counts?.[wt.key];
                if (!c) return null;
                const limit = detailData.key?.[wt.limitField] || 0;
                let metricField: string | undefined;
                if (wt.key === 'tpm') metricField = 'tokens';
                else if (wt.key === 'rpmo') metricField = detailData.key?.rpm_metric;
                else if (wt.key !== 'rpm') metricField = detailData.key?.[`${wt.key}_metric`];
                const isCost = metricField === 'cost';
                const used = isCost ? (c.cost ?? 0) / 1e6 : metricField === 'tokens' ? c.token_count : c.count;
                const limitShown = isCost ? limit / 1e6 : limit;
                const percent = limit > 0 ? Math.min(100, used / limitShown * 100) : 0;
                const fmt = (v: number) => isCost ? `$${v.toFixed(2)}` : String(v);
                return (
                  <div key={wt.key} style={{ textAlign: 'center' }}>
                    <Progress type="circle" size={72} percent={percent} strokeWidth={8} format={(p) => <span style={{ fontSize: 16, fontWeight: 600 }}>{limit > 0 ? fmtPercent(p ?? 0) : '—'}</span>} />
                    <div style={{ marginTop: 4 }}><Text strong>{wt.label}</Text></div>
                    <Text type="secondary" style={{ fontSize: 12 }}>
                      {limit > 0 ? `${fmt(used)} / ${fmt(limitShown)}` : `${fmt(used)} (unlimited)`}
                    </Text>
                    {!isCost && c.token_count > 0 && (
                      <div><Text type="secondary" style={{ fontSize: 11 }}>{c.token_count} t</Text></div>
                    )}
                  </div>
                );
              })}
            </div>
            {/* Lifetime budget: a one-time spend cap, not a sliding window.
                Shown on the panel so the full limit story is visible — the
                key disables permanently once spent reaches the cap. */}
            {budget && (
              <div style={{ maxWidth: 480, marginTop: 12 }}>
                <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                  <Text strong>Lifetime Budget</Text>
                  <Text type="secondary" style={{ fontSize: 12 }}>
                    {budget.limit > 0
                      ? `$${microUsdToUsd(budget.spent).toFixed(2)} / $${microUsdToUsd(budget.limit).toFixed(2)}`
                      : `$${microUsdToUsd(budget.spent).toFixed(2)} (no budget)`}
                  </Text>
                </div>
                <Progress
                  size="small"
                  percent={budget.limit > 0 ? Math.min(100, budget.spent / budget.limit * 100) : 0}
                  status={budget.limit > 0 && budget.spent >= budget.limit ? 'exception' : undefined}
                />
              </div>
            )}
          </div>
        )}
      </Modal>
    </div>
  );
};

export default Providers;
