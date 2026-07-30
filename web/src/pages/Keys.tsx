import React, { useEffect, useState } from 'react';
import {
  Table, Button, Modal, Form, Input, Select, InputNumber, message, Space,
  Typography, Popconfirm, Tag, Descriptions, Progress, Collapse,
} from 'antd';
import { PlusOutlined, EditOutlined, DeleteOutlined, EyeOutlined } from '@ant-design/icons';
import { getKeys, createKey, updateKey, deleteKey, getKeyDetail, getProviders, Key, Provider } from '../api/client';

const { Title, Text } = Typography;

const statusColors: Record<string, string> = {
  active: 'green',
  rate_limited: 'orange',
  disabled: 'red',
  testing: 'blue',
};

const Keys: React.FC = () => {
  const [keys, setKeys] = useState<Key[]>([]);
  const [providers, setProviders] = useState<Provider[]>([]);
  const [loading, setLoading] = useState(false);
  const [modalOpen, setModalOpen] = useState(false);
  const [detailOpen, setDetailOpen] = useState(false);
  const [detailData, setDetailData] = useState<any>(null);
  const [editing, setEditing] = useState<Key | null>(null);
  const [form] = Form.useForm();

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
      if (editing) { await updateKey(editing.id, values); message.success('Key updated'); }
      else { await createKey(values); message.success('Key created'); }
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
    form.setFieldsValue({
      ...k,
      provider_id: k.provider_id,
    });
    setModalOpen(true);
  };

  const windowTypes = [
    { key: 'rpm', label: 'RPM', limitField: 'rpm_limit' },
    { key: 'tpm', label: 'TPM', limitField: 'tpm_limit' },
    { key: 'rp5h', label: '5 Hour', limitField: 'rp5h_limit' },
    { key: 'rpd', label: 'Daily', limitField: 'rpd_limit' },
    { key: 'rpw', label: 'Weekly', limitField: 'rpw_limit' },
    { key: 'rpmo', label: 'Monthly', limitField: 'rpm_month_limit' },
  ];

  const columns = [
    { title: 'ID', dataIndex: 'id', key: 'id', width: 60 },
    {
      title: 'Name', dataIndex: 'name', key: 'name',
      render: (n: string, r: Key) => n || r.key_value?.substring(0, 12) + '...',
    },
    {
      title: 'Status', dataIndex: 'status', key: 'status',
      render: (s: string) => <Tag color={statusColors[s] || 'default'}>{s}</Tag>,
    },
    { title: 'Strategy', dataIndex: 'recovery_strategy', key: 'recovery_strategy', width: 90 },
    { title: 'Provider', key: 'provider', render: (_: unknown, r: Key) => r.provider?.name || `#${r.provider_id}` },
    {
      title: 'Rate Limits', key: 'limits',
      render: (_: unknown, r: Key) => (
        <Space size={4} wrap>
          {r.rpm_limit > 0 && <Tag>{r.rpm_limit} rpm</Tag>}
          {r.tpm_limit > 0 && <Tag>{r.tpm_limit} tpm</Tag>}
          {r.rpd_limit > 0 && <Tag>{r.rpd_limit}/d</Tag>}
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
      <Table dataSource={keys} columns={columns} rowKey="id" loading={loading} scroll={{ x: 900 }} />

      <Modal title={editing ? 'Edit Key' : 'Add Key'} open={modalOpen} onOk={handleSave} onCancel={() => { setModalOpen(false); setEditing(null); }} width={680}>
        <Form form={form} layout="vertical">
          <Form.Item name="provider_id" label="Provider" rules={[{ required: true }]}>
            <Select
              placeholder="Select a provider"
              showSearch
              options={providers.map(p => ({ value: p.id, label: `${p.name} (${p.type})` }))}
            />
          </Form.Item>
          <Form.Item name="name" label="Display Name"><Input placeholder="My OpenAI Key 1" /></Form.Item>
          <Form.Item name="key_value" label="API Key" rules={[{ required: true }]}><Input.Password placeholder="sk-..." /></Form.Item>
          <Form.Item name="recovery_strategy" label="Recovery Strategy" initialValue="lazy">
            <Select options={[
              { value: 'immediate', label: 'Immediate — use as soon as recovered' },
              { value: 'lazy', label: 'Lazy — only use when no immediate keys available' },
            ]} />
          </Form.Item>
          <div style={{ marginTop: 8 }}>
            <Typography.Text strong>Rate Limits</Typography.Text>
            <Space size="large" style={{ marginTop: 8, marginBottom: 8, display: 'flex' }}>
              <Form.Item name="rpm_limit" label="RPM (requests)"><InputNumber min={0} placeholder="500" /></Form.Item>
              <Form.Item name="tpm_limit" label="TPM (tokens)"><InputNumber min={0} placeholder="200000" /></Form.Item>
            </Space>
            <Space size="large" style={{ display: 'flex', flexWrap: 'wrap' }}>
              <Form.Item name="rp5h_limit" label="5-Hour Limit"><InputNumber min={0} placeholder="5000" style={{ width: 100 }} /></Form.Item>
              <Form.Item name="rp5h_metric" label="Metric">
                <Select style={{ width: 100 }} options={[
                  { value: 'requests', label: 'Requests' },
                  { value: 'tokens', label: 'Tokens' },
                ]} />
              </Form.Item>
              <Form.Item name="rpd_limit" label="Daily Limit"><InputNumber min={0} placeholder="10000" style={{ width: 100 }} /></Form.Item>
              <Form.Item name="rpd_metric" label="Metric">
                <Select style={{ width: 100 }} options={[
                  { value: 'requests', label: 'Requests' },
                  { value: 'tokens', label: 'Tokens' },
                ]} />
              </Form.Item>
              <Form.Item name="rpw_limit" label="Weekly Limit"><InputNumber min={0} placeholder="50000" style={{ width: 100 }} /></Form.Item>
              <Form.Item name="rpw_metric" label="Metric">
                <Select style={{ width: 100 }} options={[
                  { value: 'requests', label: 'Requests' },
                  { value: 'tokens', label: 'Tokens' },
                ]} />
              </Form.Item>
              <Form.Item name="rpm_month_limit" label="Monthly Limit"><InputNumber min={0} placeholder="200000" style={{ width: 100 }} /></Form.Item>
              <Form.Item name="rpm_metric" label="Metric">
                <Select style={{ width: 100 }} options={[
                  { value: 'requests', label: 'Requests' },
                  { value: 'tokens', label: 'Tokens' },
                ]} />
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
              </Descriptions.Item>
              <Descriptions.Item label="Strategy">{detailData.key?.recovery_strategy}</Descriptions.Item>
              <Descriptions.Item label="Provider">{detailData.key?.provider?.name}</Descriptions.Item>
              <Descriptions.Item label="Total Cost">${detailData.total_cost?.toFixed(6)}</Descriptions.Item>
            </Descriptions>
            <Title level={5} style={{ marginTop: 16 }}>Window Counters</Title>
            <Space wrap size="large">
              {windowTypes.map(wt => {
                const c = detailData.counts?.[wt.key];
                return c ? (
                  <div key={wt.key} style={{ textAlign: 'center' }}>
                    <Text strong>{wt.label}</Text>
                    <Progress type="circle" size={60} percent={Math.min(100, c.count / (detailData.key?.[wt.limitField] || 1) * 100)} format={() => `${c.count}`} />
                    <div><Text type="secondary">{c.token_count > 0 ? `${c.token_count} t` : ''}</Text></div>
                  </div>
                ) : null;
              })}
            </Space>
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
