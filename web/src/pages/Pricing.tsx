import React, { useEffect, useState } from 'react';
import { Table, Button, Modal, Form, Input, InputNumber, message, Space, Typography, Popconfirm } from 'antd';
import { PlusOutlined, EditOutlined, DeleteOutlined } from '@ant-design/icons';
import { getPricings, createPricing, updatePricing, deletePricing, Pricing as PricingType } from '../api/client';

const { Title } = Typography;

const Pricing: React.FC = () => {
  const [pricings, setPricings] = useState<PricingType[]>([]);
  const [loading, setLoading] = useState(false);
  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<PricingType | null>(null);
  const [form] = Form.useForm();

  const fetch = async () => {
    setLoading(true);
    try {
      const res = await getPricings();
      setPricings(res.data);
    } catch { message.error('Failed to load pricing'); }
    finally { setLoading(false); }
  };

  useEffect(() => { fetch(); }, []);

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
    { title: 'Model Name', dataIndex: 'model_name', key: 'model_name' },
    { title: 'Prompt /1K', dataIndex: 'prompt_per_1k', key: 'prompt_per_1k', render: (v: number) => `$${v?.toFixed(6)}` },
    { title: 'Completion /1K', dataIndex: 'completion_per_1k', key: 'completion_per_1k', render: (v: number) => `$${v?.toFixed(6)}` },
    { title: 'Cache Read /1K', dataIndex: 'cache_read_per_1k', key: 'cache_read_per_1k', render: (v: number) => v ? `$${v.toFixed(6)}` : '-' },
    { title: 'Cache Write /1K', dataIndex: 'cache_write_per_1k', key: 'cache_write_per_1k', render: (v: number) => v ? `$${v.toFixed(6)}` : '-' },
    {
      title: 'Actions', key: 'actions',
      render: (_: unknown, r: PricingType) => (
        <Space>
          <Button icon={<EditOutlined />} size="small" onClick={() => { setEditing(r); form.setFieldsValue(r); setModalOpen(true); }}>Edit</Button>
          <Popconfirm title="Delete?" onConfirm={() => handleDelete(r.id)}>
            <Button icon={<DeleteOutlined />} size="small" danger>Delete</Button>
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
          <Form.Item name="model_name" label="Model Name" rules={[{ required: true }]}>
            <Input placeholder="gpt-4o" />
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
