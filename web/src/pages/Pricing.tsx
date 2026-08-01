import React, { useEffect, useState } from 'react';
import { Table, Button, Modal, Form, InputNumber, Select, message, Space, Typography, Popconfirm, Tag } from 'antd';
import { PlusOutlined, EditOutlined, DeleteOutlined } from '@ant-design/icons';
import { getPricings, createPricing, updatePricing, deletePricing, getRoutes, getModelGroups, Pricing as PricingType, Route, ModelGroup } from '../api/client';

const { Title } = Typography;

const Pricing: React.FC = () => {
  const [pricings, setPricings] = useState<PricingType[]>([]);
  const [routes, setRoutes] = useState<Route[]>([]);
  const [groups, setGroups] = useState<ModelGroup[]>([]);
  const [loading, setLoading] = useState(false);
  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<PricingType | null>(null);
  const [form] = Form.useForm();

  const fetch = async () => {
    setLoading(true);
    try {
      const [p, r, g] = await Promise.all([getPricings(), getRoutes(), getModelGroups()]);
      setPricings(p.data); setRoutes(r.data); setGroups(g.data);
    } catch { message.error('Failed to load pricing'); }
    finally { setLoading(false); }
  };

  useEffect(() => { fetch(); }, []);

  // Collect unique model names: target models from routes + all model group IDs
  const modelOptions = Array.from(new Set([
    ...groups.map(g => g.group_id).filter(Boolean),
    ...routes.map(r => r.target_model).filter(Boolean),
  ])).sort() as string[];

  const handleSave = async () => {
    try {
      const values = await form.validateFields();
      if (editing) { await updatePricing(editing.id, values); message.success('Updated'); }
      else { await createPricing(values); message.success('Created'); }
      setModalOpen(false); setEditing(null); form.resetFields(); fetch();
    } catch { message.error('Failed to save pricing'); }
  };

  const handleDelete = async (id: number) => {
    try {
      await deletePricing(id); message.success('Deleted'); fetch();
    } catch { message.error('Failed to delete pricing'); }
  };

  const columns = [
    { title: 'ID', dataIndex: 'id', key: 'id', width: 60 },
    { title: 'Model', dataIndex: 'model_name', key: 'model_name', render: (v: string) => <Tag color="blue">{v}</Tag> },
    { title: 'Prompt $/1K', dataIndex: 'prompt_per_1k', key: 'prompt_per_1k', render: (v: number) => `$${v?.toFixed(6)}` },
    { title: 'Completion $/1K', dataIndex: 'completion_per_1k', key: 'completion_per_1k', render: (v: number) => `$${v?.toFixed(6)}` },
    { title: 'Cache Read $/1K', dataIndex: 'cache_read_per_1k', key: 'cache_read_per_1k', render: (v: number) => v ? `$${v.toFixed(6)}` : '-' },
    { title: 'Cache Write $/1K', dataIndex: 'cache_write_per_1k', key: 'cache_write_per_1k', render: (v: number) => v ? `$${v.toFixed(6)}` : '-' },
    {
      title: 'Actions', key: 'actions', width: 80,
      render: (_: unknown, r: PricingType) => (
        <Space>
          <Button icon={<EditOutlined />} size="small" onClick={() => { setEditing(r); form.setFieldsValue(r); setModalOpen(true); }} title="Edit" />
          <Popconfirm title="Delete?" onConfirm={() => handleDelete(r.id)}>
            <Button icon={<DeleteOutlined />} size="small" danger title="Delete" />
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <div>
      <Title level={3}>Pricing</Title>
      <Button type="primary" icon={<PlusOutlined />} onClick={() => { setEditing(null); form.resetFields(); setModalOpen(true); }} style={{ marginBottom: 16 }}>
        Add Pricing
      </Button>
      <Table dataSource={pricings} columns={columns} rowKey="id" loading={loading} />
      <Modal title={editing ? 'Edit Pricing' : 'Add Pricing'} open={modalOpen} onOk={handleSave} onCancel={() => { setModalOpen(false); setEditing(null); }}>
        <Form form={form} layout="vertical">
          <Form.Item
            name="model_name"
            label="Model"
            extra='Use "*" for a wildcard default covering any unlisted model'
            rules={[{ required: true }]}
          >
            <Select
              showSearch
              placeholder="Select a target model, or choose * for a wildcard rule"
              options={[
                { value: '*', label: '* (wildcard — any unlisted model)' },
                ...modelOptions.map(m => ({ value: m, label: m })),
              ]}
            />
          </Form.Item>
          <Space size="large">
            <Form.Item name="prompt_per_1k" label="Prompt $/1K"><InputNumber min={0} step={0.0001} style={{ width: 150 }} /></Form.Item>
            <Form.Item name="completion_per_1k" label="Completion $/1K"><InputNumber min={0} step={0.0001} style={{ width: 150 }} /></Form.Item>
          </Space>
          <Space size="large">
            <Form.Item name="cache_read_per_1k" label="Cache Read $/1K"><InputNumber min={0} step={0.0001} style={{ width: 150 }} /></Form.Item>
            <Form.Item name="cache_write_per_1k" label="Cache Write $/1K"><InputNumber min={0} step={0.0001} style={{ width: 150 }} /></Form.Item>
          </Space>
        </Form>
      </Modal>
    </div>
  );
};

export default Pricing;
