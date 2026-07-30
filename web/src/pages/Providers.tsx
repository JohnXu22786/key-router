import React, { useEffect, useState } from 'react';
import { Table, Button, Modal, Form, Input, Select, message, Space, Typography, Popconfirm } from 'antd';
import { PlusOutlined, EditOutlined, DeleteOutlined } from '@ant-design/icons';
import { getProviders, createProvider, updateProvider, deleteProvider, Provider } from '../api/client';

const { Title } = Typography;

const Providers: React.FC = () => {
  const [providers, setProviders] = useState<Provider[]>([]);
  const [loading, setLoading] = useState(false);
  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<Provider | null>(null);
  const [form] = Form.useForm();

  const fetch = async () => {
    setLoading(true);
    try {
      const res = await getProviders();
      setProviders(res.data);
    } catch (err) {
      message.error('Failed to load providers');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { fetch(); }, []);

  const handleSave = async () => {
    try {
      const values = await form.validateFields();
      if (editing) {
        await updateProvider(editing.id, values);
        message.success('Provider updated');
      } else {
        await createProvider(values);
        message.success('Provider created');
      }
      setModalOpen(false);
      setEditing(null);
      form.resetFields();
      fetch();
    } catch (err) {
      if (err instanceof Error) message.error(err.message);
    }
  };

  const handleDelete = async (id: number) => {
    try {
      await deleteProvider(id);
      message.success('Provider deleted');
      fetch();
    } catch (err) {
      message.error('Failed to delete provider');
    }
  };

  const openEdit = (provider: Provider) => {
    setEditing(provider);
    form.setFieldsValue(provider);
    setModalOpen(true);
  };

  const columns = [
    { title: 'ID', dataIndex: 'id', key: 'id', width: 60 },
    { title: 'Name', dataIndex: 'name', key: 'name' },
    { title: 'Type', dataIndex: 'type', key: 'type', render: (t: string) => ({ openai: 'OpenAI', anthropic: 'Anthropic' })[t] || t },
    { title: 'Base URL', dataIndex: 'base_url', key: 'base_url', ellipsis: true },
    {
      title: 'Actions', key: 'actions', width: 80,
      render: (_: unknown, record: Provider) => (
        <Space>
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
      <Title level={3}>Providers</Title>
      <Button type="primary" icon={<PlusOutlined />} onClick={() => { setEditing(null); form.resetFields(); setModalOpen(true); }} style={{ marginBottom: 16 }}>
        Add Provider
      </Button>
      <Table dataSource={providers} columns={columns} rowKey="id" loading={loading} />

      <Modal
        title={editing ? 'Edit Provider' : 'Add Provider'}
        open={modalOpen}
        onOk={handleSave}
        onCancel={() => { setModalOpen(false); setEditing(null); }}
      >
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="Name" rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="type" label="Type" rules={[{ required: true }]}>
            <Select options={[
              { value: 'openai', label: 'OpenAI' },
              { value: 'anthropic', label: 'Anthropic' },
            ]} />
          </Form.Item>
          <Form.Item name="base_url" label="Base URL" rules={[{ required: true }]}>
            <Input placeholder="https://api.openai.com/v1" />
          </Form.Item>
          <Form.Item name="extra_headers" label="Extra Headers (JSON)">
            <Input.TextArea rows={3} placeholder='{"Organization":"org-xxx"}' />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
};

export default Providers;
