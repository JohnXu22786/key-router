import React, { useEffect, useState, useCallback, useRef } from 'react';
import {
  Table, Button, Modal, Form, Input, InputNumber, Select, message, Space,
  Typography, Popconfirm, Tag, Descriptions, Progress, Popover,
} from 'antd';
import {
  PlusOutlined, EditOutlined, DeleteOutlined, EyeOutlined, HolderOutlined,
} from '@ant-design/icons';
import {
  getProviders, createProvider, updateProvider, deleteProvider,
  getKeys, createKey, updateKey, deleteKey, reorderKeys, getKeyDetail,
  Provider, Key,
} from '../api/client';

const { Title, Text } = Typography;

const statusColors: Record<string, string> = {
  active: 'green', rate_limited: 'orange', disabled: 'red', testing: 'blue',
};

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
  const dragItem = useRef<number | null>(null);
  const persistTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const keysRef = useRef<Key[]>([]);
  keysRef.current = keys;

  const fetch = async () => {
    setLoading(true);
    try {
      const [p, k] = await Promise.all([getProviders(), getKeys()]);
      setProviders(p.data); setKeys(k.data);
    } catch { message.error('Failed to load providers'); }
    finally { setLoading(false); }
  };

  useEffect(() => { fetch(); }, []);

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
      if (!statusTouched.current) delete values.status;
      // Rate-limit cancellation: emptied input = 0 = unlimited. Cost-metric
      // windows are entered in USD but stored in micro-USD.
      for (const wt of windowTypes) {
        if (values[wt.limitField] == null) values[wt.limitField] = 0;
        else if (wt.metricField && values[wt.metricField] === 'cost') {
          values[wt.limitField] = Math.round(values[wt.limitField] * 1e6);
        }
      }
      if (editingKey) { await updateKey(editingKey.id, values); message.success('Key updated'); }
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
    keyForm.setFieldsValue(values);
    setKeyModal(true);
  };

  const persistOrder = useCallback((ordered: Key[]) => {
    if (persistTimer.current) clearTimeout(persistTimer.current);
    persistTimer.current = setTimeout(async () => {
      const providerCounts: Record<number, number> = {};
      const payload = ordered.map(k => {
        const idx = providerCounts[k.provider_id] ?? 0;
        providerCounts[k.provider_id] = idx + 1;
        return { id: k.id, sort_order: idx };
      });
      try { await reorderKeys(payload); } catch { message.error('Failed to save order'); }
    }, 300);
  }, []);

  const handleDragStart = (e: React.DragEvent, index: number) => {
    dragItem.current = index;
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', String(index));
  };
  const handleDragOver = (e: React.DragEvent) => { e.preventDefault(); e.dataTransfer.dropEffect = 'move'; };
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
    const next = [...current];
    const [removed] = next.splice(dragIndex, 1);
    next.splice(dropIndex, 0, removed);
    setKeys(next);
    dragItem.current = null;
    persistOrder(next);
  };

  const provColumns = [
    { title: 'Name', dataIndex: 'name', key: 'name' },
    { title: 'Type', dataIndex: 'type', key: 'type', render: (t: string) => ({ openai: 'OpenAI', anthropic: 'Anthropic' })[t] || t },
    { title: 'Base URL', dataIndex: 'base_url', key: 'base_url', ellipsis: true },
    {
      title: 'Keys', key: 'keyCount',
      render: (_: unknown, p: Provider) => <Tag>{keys.filter(k => k.provider_id === p.id).length}</Tag>,
    },
    {
      title: 'Actions', key: 'actions', width: 80,
      render: (_: unknown, p: Provider) => (
        <Space>
          <Button icon={<EditOutlined />} size="small" onClick={() => { setEditingProv(p); provForm.setFieldsValue(p); setProvModal(true); }} title="Edit" />
          <Popconfirm title="Delete provider?" onConfirm={() => deleteProvider(p.id)}>
            <Button icon={<DeleteOutlined />} size="small" danger title="Delete" />
          </Popconfirm>
        </Space>
      ),
    },
  ];

  const keyColumns = [
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

  return (
    <div>
      <Title level={3}>Providers</Title>
      <Typography.Paragraph type="secondary">
        Each provider owns its API keys (a key belongs to exactly one provider). Expand a
        provider to manage its keys — drag to reorder (order = call order), set rate limits
        and recovery strategy per key. Limits apply only to traffic through KeyRouter.
      </Typography.Paragraph>
      <Button type="primary" icon={<PlusOutlined />} onClick={() => { setEditingProv(null); provForm.resetFields(); setProvModal(true); }} style={{ marginBottom: 16 }}>
        Add Provider
      </Button>

      <Table
        dataSource={providers}
        columns={provColumns}
        rowKey="id"
        loading={loading}
        expandable={{
          expandedRowKeys: activeProviders,
          onExpandedRowsChange: (keys: any) => setActiveProviders(keys.map(String)),
          expandedRowRender: (p) => {
            const provKeys = keys.filter(k => k.provider_id === p.id);
            return (
              <div style={{ padding: '0 8px' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
                  <Text strong>Keys for {p.name}</Text>
                  <Button
                    size="small" type="primary" icon={<PlusOutlined />}
                    onClick={() => { setEditingKey(null); keyForm.resetFields(); keyForm.setFieldsValue({ provider_id: p.id, recovery_strategy: 'lazy', status: 'active' }); setKeyModal(true); }}
                  >
                    Add Key
                  </Button>
                </div>
                {provKeys.length === 0 && <Text type="secondary">No keys yet.</Text>}
                <Table
                  dataSource={provKeys}
                  columns={keyColumns}
                  rowKey="id"
                  size="small"
                  pagination={false}
                  onRow={(_, index) => ({
                    draggable: true,
                    onDragStart: (e) => handleDragStart(e, keys.indexOf(provKeys[index!])),
                    onDragOver: handleDragOver,
                    onDrop: (e) => handleDrop(e, keys.indexOf(provKeys[index!])),
                    style: { cursor: 'default' },
                  })}
                />
              </div>
            );
          },
        }}
      />

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
              Leave a field empty to remove the limit (0 = unlimited). For 5-hour/daily/weekly/monthly windows, the metric can be requests, tokens, or cost — cost limits are in USD.
            </Typography.Paragraph>
            <Space size="large" style={{ marginTop: 8, marginBottom: 8, display: 'flex' }}>
              <Form.Item name="rpm_limit" label="RPM (requests)"><InputNumber min={0} placeholder="500" /></Form.Item>
              <Form.Item name="tpm_limit" label="TPM (tokens)"><InputNumber min={0} placeholder="200000" /></Form.Item>
            </Space>
            <Space size="large" style={{ display: 'flex', flexWrap: 'wrap' }}>
              <Form.Item name="rp5h_limit" label="5-Hour Limit"><InputNumber min={0} placeholder="5000" style={{ width: 100 }} /></Form.Item>
              <Form.Item name="rp5h_metric" label="Metric"><Select style={{ width: 120 }} options={metricOptions} /></Form.Item>
              <Form.Item name="rpd_limit" label="Daily Limit"><InputNumber min={0} placeholder="10000" style={{ width: 100 }} /></Form.Item>
              <Form.Item name="rpd_metric" label="Metric"><Select style={{ width: 120 }} options={metricOptions} /></Form.Item>
              <Form.Item name="rpw_limit" label="Weekly Limit"><InputNumber min={0} placeholder="50000" style={{ width: 100 }} /></Form.Item>
              <Form.Item name="rpw_metric" label="Metric"><Select style={{ width: 120 }} options={metricOptions} /></Form.Item>
              <Form.Item name="rpm_month_limit" label="Monthly Limit"><InputNumber min={0} placeholder="200000" style={{ width: 100 }} /></Form.Item>
              <Form.Item name="rpm_metric" label="Metric"><Select style={{ width: 120 }} options={metricOptions} /></Form.Item>
            </Space>
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
          </div>
        )}
      </Modal>
    </div>
  );
};

export default Providers;
