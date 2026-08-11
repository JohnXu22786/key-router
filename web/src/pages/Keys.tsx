import React, { useEffect, useState, useRef, useCallback } from 'react';
import {
  Table, Button, Modal, Form, Input, Select, InputNumber, message, Space, Alert,
  Typography, Popconfirm, Tag, Descriptions, Progress, Collapse,
} from 'antd';
import { PlusOutlined, EditOutlined, DeleteOutlined, EyeOutlined, HolderOutlined } from '@ant-design/icons';
import { getKeys, createKey, updateKey, deleteKey, getKeyDetail, getProviders, reorderKeys, Key, Provider } from '../api/client';

const { Title, Text } = Typography;

const statusColors: Record<string, string> = {
  active: 'green',
  rate_limited: 'orange',
  disabled: 'red',
  testing: 'blue',
};

// Human-readable labels for the failure reasons the relay/health checker
// write into disabled_reason (user-visible error feedback: 欠费/鉴权失败/...)
const reasonLabels: Record<string, string> = {
  auth_failed: 'Auth failed',
  insufficient_quota: 'Insufficient quota',
  rate_limited: 'Rate limited',
  upstream_error: 'Upstream error',
};

const metricOptions = [
  { value: 'requests', label: 'Requests' },
  { value: 'tokens', label: 'Tokens' },
  { value: 'cost', label: 'Cost (USD)' },
];

// fmtPercent renders a percentage with few digits: >=100 → integer,
// >=1 → 1 decimal, else 2 decimals. Storage keeps full precision; only the
// display is shortened (nobody reads 0.03149166666666666%).
const fmtPercent = (v: number): string => {
  if (v >= 100) return `${Math.round(v)}%`;
  if (v >= 1) return `${v.toFixed(1)}%`;
  return `${v.toFixed(2)}%`;
};

// Metric fields for the 5h/daily/weekly/monthly windows. The limit field
// stores micro-USD when the metric is cost, so the UI converts on display.
const windowTypes = [
  { key: 'rpm', label: 'RPM', limitField: 'rpm_limit' },
  { key: 'tpm', label: 'TPM', limitField: 'tpm_limit' },
  { key: 'rp5h', label: '5 Hour', limitField: 'rp5h_limit', metricField: 'rp5h_metric' },
  { key: 'rpd', label: 'Daily', limitField: 'rpd_limit', metricField: 'rpd_metric' },
  { key: 'rpw', label: 'Weekly', limitField: 'rpw_limit', metricField: 'rpw_metric' },
  { key: 'rpmo', label: 'Monthly', limitField: 'rpm_month_limit', metricField: 'rpm_metric' },
];

const Keys: React.FC = () => {
  const [keys, setKeys] = useState<Key[]>([]);
  const [providers, setProviders] = useState<Provider[]>([]);
  const [loading, setLoading] = useState(false);
  const [modalOpen, setModalOpen] = useState(false);
  const [detailOpen, setDetailOpen] = useState(false);
  const [detailData, setDetailData] = useState<any>(null);
  const [editing, setEditing] = useState<Key | null>(null);
  const [activeGroups, setActiveGroups] = useState<string[]>([]);
  const [form] = Form.useForm();
  // True once the user explicitly changed the status field — only then is
  // "status" included in the save payload. Without this, every edit would
  // echo the page-load snapshot's status and silently override what the
  // relay/health checker did since (e.g. re-enabling a key the relay just
  // disabled, or permanently disabling an auto-recovered one).
  const statusTouched = useRef(false);
  const dragItem = useRef<number | null>(null);
  const persistTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  // Always-current keys for drag handling — avoids stale-closure reorders
  const keysRef = useRef<Key[]>([]);
  keysRef.current = keys;

  const fetch = async () => {
    setLoading(true);
    try {
      const [keysRes, provRes] = await Promise.all([getKeys(), getProviders()]);
      setKeys(keysRes.data);
      setProviders(provRes.data);
    } catch { message.error('Failed to load keys'); }
    finally { setLoading(false); }
  };

  useEffect(() => { fetch(); }, []);

  const handleSave = async () => {
    try {
      const values = await form.validateFields();
      if (!statusTouched.current) delete values.status;
      // Rate-limit cancellation: an emptied limit input (null/undefined)
      // means "no limit" — normalize to 0 so the backend actually clears the
      // stored value instead of keeping the old one. Cost-metric windows are
      // entered in USD but stored in micro-USD (1e6 per $1).
      for (const wt of windowTypes) {
        if (values[wt.limitField] == null) {
          values[wt.limitField] = 0;
        } else if (wt.metricField && values[wt.metricField] === 'cost') {
          values[wt.limitField] = Math.round(values[wt.limitField] * 1e6);
        }
      }
      if (editing) { await updateKey(editing.id, values); message.success('Key updated'); }
      else { await createKey(values); message.success('Key created'); }
      statusTouched.current = false;
      setModalOpen(false); setEditing(null); form.resetFields(); fetch();
    } catch (err) { message.error('Failed to save key'); }
  };

  const handleDelete = async (id: number) => {
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

  const openEdit = (k: Key) => {
    setEditing(k);
    statusTouched.current = false;
    // Convert micro-USD limit fields back to USD for the form when the
    // window's metric is cost.
    const values: any = { ...k, provider_id: k.provider_id };
    for (const wt of windowTypes) {
      if (wt.metricField) {
        const metric = (k as any)[wt.metricField];
        if (metric === 'cost') {
          values[wt.limitField] = (k as any)[wt.limitField] / 1e6;
        }
      }
    }
    form.setFieldsValue(values);
    setModalOpen(true);
  };

  const persistOrder = useCallback((ordered: Key[]) => {
    // Debounce: cancel previous pending save
    if (persistTimer.current) clearTimeout(persistTimer.current);
    persistTimer.current = setTimeout(async () => {
      // sort_order is PER-PROVIDER: the table mixes providers, so a global
      // index would silently renumber every other provider's keys.
      const providerCounts: Record<number, number> = {};
      const payload = ordered.map(k => {
        const idx = providerCounts[k.provider_id] ?? 0;
        providerCounts[k.provider_id] = idx + 1;
        return { id: k.id, sort_order: idx };
      });
      try {
        await reorderKeys(payload);
      } catch { message.error('Failed to save order'); }
    }, 300);
  }, []);

  const handleDragStart = (e: React.DragEvent, index: number) => {
    dragItem.current = index;
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', String(index));
  };

  const handleDragOver = (e: React.DragEvent) => {
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
  };

  const handleDrop = (e: React.DragEvent, dropIndex: number) => {
    e.preventDefault();
    const dragIndex = dragItem.current;
    if (dragIndex === null || dragIndex === dropIndex) return;

    const current = keysRef.current;
    if (current[dragIndex].provider_id !== current[dropIndex].provider_id) {
      message.warning('Keys can only be reordered within the same provider');
      dragItem.current = null;
      return;
    }

    const newKeys = [...current];
    const [removed] = newKeys.splice(dragIndex, 1);
    newKeys.splice(dropIndex, 0, removed);
    setKeys(newKeys);
    dragItem.current = null;
    persistOrder(newKeys);
  };

  // Group keys by provider for the collapsible per-provider lists.
  const providersWithKeys = providers
    .map(p => ({ provider: p, keys: keys.filter(k => k.provider_id === p.id) }))
    .filter(g => g.keys.length > 0);

  const columns = [
    {
      title: '', key: 'drag', width: 40,
      render: () => <HolderOutlined style={{ cursor: 'grab', color: '#999' }} />,
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
        </Space>
      ),
    },
    { title: 'Strategy', dataIndex: 'recovery_strategy', key: 'recovery_strategy', width: 90 },
    {
      title: 'Rate Limits', key: 'limits',
      render: (_: unknown, r: Key) => (
        <Space size={4} wrap>
          {r.rpm_limit > 0 && <Tag>{r.rpm_limit} rpm</Tag>}
          {r.tpm_limit > 0 && <Tag>{r.tpm_limit} tpm</Tag>}
          {r.rpd_limit > 0 && <Tag>{r.rpd_metric === 'cost' ? `$${(r.rpd_limit / 1e6).toFixed(2)}/d` : `${r.rpd_limit}/d`}</Tag>}
        </Space>
      ),
    },
    {
      title: 'Actions', key: 'actions', width: 120,
      render: (_: unknown, record: Key) => (
        <Space>
          <Button icon={<EyeOutlined />} size="small" onClick={() => showDetail(record)} title="Detail" />
          <Button icon={<EditOutlined />} size="small" onClick={() => openEdit(record)} title="Edit" />
          <Popconfirm title="Delete?" onConfirm={() => handleDelete(record.id)}>
            <Button icon={<DeleteOutlined />} size="small" danger title="Delete" />
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <div>
      <Title level={3}>Keys</Title>
      <Button type="primary" icon={<PlusOutlined />} onClick={() => { setEditing(null); form.resetFields(); setModalOpen(true); }} style={{ marginBottom: 16 }}>
        Add Key
      </Button>
      <Typography.Paragraph type="secondary">
        Keys are grouped by provider. Drag keys to reorder them — the order you
        arrange them is the order they are called in (per provider). Immediate
        strategy keys are always preferred over Lazy keys.
      </Typography.Paragraph>

      {providersWithKeys.length === 0 && !loading && (
        <Typography.Text type="secondary">No keys yet. Add a key to get started.</Typography.Text>
      )}

      {providersWithKeys.map(g => (
        <Collapse
          key={g.provider.id}
          style={{ marginBottom: 12 }}
          activeKey={activeGroups}
          onChange={(keys: any) => setActiveGroups(Array.isArray(keys) ? keys : [keys])}
          items={[{
            key: String(g.provider.id),
            label: (
              <Space>
                <Text strong>{g.provider.name}</Text>
                <Tag>{g.provider.type}</Tag>
                <Text type="secondary">{g.keys.length} key{g.keys.length > 1 ? 's' : ''}</Text>
              </Space>
            ),
            children: (
              <Table
                dataSource={g.keys}
                columns={columns}
                rowKey="id"
                size="small"
                pagination={false}
                onRow={(_, index) => ({
                  draggable: true,
                  onDragStart: (e) => handleDragStart(e, keys.indexOf(g.keys[index!])),
                  onDragOver: handleDragOver,
                  onDrop: (e) => handleDrop(e, keys.indexOf(g.keys[index!])),
                  style: { cursor: 'default' },
                })}
              />
            ),
          }]}
        />
      ))}

      <Modal title={editing ? 'Edit Key' : 'Add Key'} open={modalOpen} onOk={handleSave} onCancel={() => { setModalOpen(false); setEditing(null); }} width={680}>
        <Form
          form={form}
          layout="vertical"
          onValuesChange={(changed) => {
            if ('status' in changed) statusTouched.current = true;
          }}
        >
          <Form.Item name="provider_id" label="Provider" rules={[{ required: true }]}>
            <Select
              placeholder="Select a provider"
              showSearch
              options={providers.map(p => ({ value: p.id, label: `${p.name} (${p.type})` }))}
            />
          </Form.Item>
          <Form.Item name="name" label="Display Name"><Input placeholder="My OpenAI Key 1" /></Form.Item>
          <Form.Item name="key_value" label="API Key" rules={[{ required: true }]}><Input.Password placeholder="sk-..." /></Form.Item>
          <Form.Item name="status" label="Status" tooltip="Set to Disabled to take the key out of rotation manually (keeps its history). The relay and health checker manage this field automatically otherwise.">
            <Select options={[
              { value: 'active', label: 'Active' },
              { value: 'disabled', label: 'Disabled' },
            ]} />
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
              Leave a field empty to remove the limit (0 = unlimited). For 5-hour/daily/weekly/monthly windows, the metric can be requests, tokens, or cost — cost limits are in USD.
            </Typography.Paragraph>
            <Alert
              type="info"
              showIcon
              style={{ marginBottom: 8 }}
              message="Limits apply to traffic through KeyRouter only"
              description="Rate limits are enforced on requests that flow through KeyRouter. Usage that bypasses KeyRouter — calling the upstream provider directly with the raw API key — is invisible to the gateway and does not count toward these limits."
            />
            <Space size="large" style={{ marginTop: 8, marginBottom: 8, display: 'flex' }}>
              <Form.Item name="rpm_limit" label="RPM (requests)"><InputNumber min={0} placeholder="500" /></Form.Item>
              <Form.Item name="tpm_limit" label="TPM (tokens)"><InputNumber min={0} placeholder="200000" /></Form.Item>
            </Space>
            <Space size="large" style={{ display: 'flex', flexWrap: 'wrap' }}>
              <Form.Item name="rp5h_limit" label="5-Hour Limit"><InputNumber min={0} placeholder="5000" style={{ width: 100 }} /></Form.Item>
              <Form.Item name="rp5h_metric" label="Metric">
                <Select style={{ width: 120 }} options={metricOptions} />
              </Form.Item>
              <Form.Item name="rpd_limit" label="Daily Limit"><InputNumber min={0} placeholder="10000" style={{ width: 100 }} /></Form.Item>
              <Form.Item name="rpd_metric" label="Metric">
                <Select style={{ width: 120 }} options={metricOptions} />
              </Form.Item>
              <Form.Item name="rpw_limit" label="Weekly Limit"><InputNumber min={0} placeholder="50000" style={{ width: 100 }} /></Form.Item>
              <Form.Item name="rpw_metric" label="Metric">
                <Select style={{ width: 120 }} options={metricOptions} />
              </Form.Item>
              <Form.Item name="rpm_month_limit" label="Monthly Limit"><InputNumber min={0} placeholder="200000" style={{ width: 100 }} /></Form.Item>
              <Form.Item name="rpm_metric" label="Metric">
                <Select style={{ width: 120 }} options={metricOptions} />
              </Form.Item>
            </Space>
          </div>
        </Form>
      </Modal>

      <Modal title={`Key Detail: ${detailData?.key?.name || detailData?.key?.key_value?.substring(0, 20)}`} open={detailOpen} onCancel={() => setDetailOpen(false)} footer={null} width={700}>
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
            {/* 2×3 grid: 6 windows, 3 per row. The circle shows only the
                percentage; the used/limit values sit underneath so the ring
                never gets cramped. */}
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 16, maxWidth: 480 }}>
              {windowTypes.map(wt => {
                const c = detailData.counts?.[wt.key];
                if (!c) return null;
                const limit = detailData.key?.[wt.limitField] || 0;
                // Token-budget windows (TPM always; 5h/daily/weekly/monthly when
                // metric is "tokens") show token_count; cost-metric windows show
                // the cost bucket (micro-USD → $). RPM always counts requests.
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
                    <div style={{ marginTop: 4 }}>
                      <Text strong>{wt.label}</Text>
                    </div>
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
            {detailData.consumptions?.length > 0 && (
              <>
                <Title level={5} style={{ marginTop: 16 }}>Recent Consumption</Title>
                <Table
                  dataSource={detailData.consumptions.slice(0, 10)}
                  columns={[
                    { title: 'Hour', dataIndex: 'hour_bucket', key: 'hour_bucket', render: (h: string) => new Date(h).toLocaleString() },
                    { title: 'Requests', dataIndex: 'request_count', key: 'request_count' },
                    { title: 'Input', dataIndex: 'input_tokens', key: 'input_tokens' },
                    { title: 'Output', dataIndex: 'output_tokens', key: 'output_tokens' },
                    { title: 'Cost', dataIndex: 'cost_usd', key: 'cost_usd', render: (c: number) => `$${c.toFixed(6)}` },
                  ]}
                  rowKey="id"
                  size="small"
                  pagination={false}
                />
              </>
            )}
          </div>
        )}
      </Modal>
    </div>
  );
};

export default Keys;
