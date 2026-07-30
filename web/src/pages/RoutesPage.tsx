import React, { useEffect, useState } from 'react';
import { Table, Button, Modal, Form, Input, Select, InputNumber, Switch, message, Space, Typography, Popconfirm } from 'antd';
import { PlusOutlined, EditOutlined, DeleteOutlined } from '@ant-design/icons';
import { getRoutes, createRoute, updateRoute, deleteRoute, getProviders, getModelGroups, Route, Provider, ModelGroup } from '../api/client';

const { Title } = Typography;

const RoutesPage: React.FC = () => {
  const [routes, setRoutes] = useState<Route[]>([]);
  const [providers, setProviders] = useState<Provider[]>([]);
  const [groups, setGroups] = useState<ModelGroup[]>([]);
  const [loading, setLoading] = useState(false);
  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<Route | null>(null);
  const [form] = Form.useForm();

  const fetch = async () => {
    setLoading(true);
    try {
      const [r, p, g] = await Promise.all([getRoutes(), getProviders(), getModelGroups()]);
      setRoutes(r.data); setProviders(p.data); setGroups(g.data);
    } catch { message.error('Failed to load routes'); }
    finally { setLoading(false); }
  };

  useEffect(() => { fetch(); }, []);

  const handleSave = async () => {
    try {
      const values = await form.validateFields();
      if (editing) { await updateRoute(editing.id, values); message.success('Updated'); }
      else { await createRoute(values); message.success('Created'); }
      setModalOpen(false); setEditing(null); form.resetFields(); fetch();
    } catch { message.error('Failed to save route'); }
  };

  const handleDelete = async (id: number) => {
    try {
      await deleteRoute(id); message.success('Deleted'); fetch();
    } catch { message.error('Failed to delete route'); }
  };

  const columns = [
    { title: 'ID', dataIndex: 'id', key: 'id', width: 60 },
    {
      title: 'Model Group', key: 'group',
      render: (_: unknown, r: Route) => r.model_group?.group_id || `#${r.model_group_id}`,
    },
    {
      title: 'Provider', key: 'provider',
      render: (_: unknown, r: Route) => r.provider?.name || `#${r.provider_id}`,
    },
    { title: 'Target Model', dataIndex: 'target_model', key: 'target_model', render: (t: string) => t || '(same)' },
    { title: 'Priority', dataIndex: 'priority', key: 'priority' },
    { title: 'Weight', dataIndex: 'weight', key: 'weight' },
    { title: 'Enabled', dataIndex: 'enabled', key: 'enabled', render: (e: boolean) => e ? 'Yes' : 'No' },
    {
      title: 'Actions', key: 'actions',
      render: (_: unknown, r: Route) => (
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
      <Title level={3}>Routes</Title>
      <Button type="primary" icon={<PlusOutlined />} onClick={() => { setEditing(null); form.resetFields(); setModalOpen(true); }} style={{ marginBottom: 16 }}>
        Add Route
      </Button>
      <Table dataSource={routes} columns={columns} rowKey="id" loading={loading} />
      <Modal title={editing ? 'Edit Route' : 'Add Route'} open={modalOpen} onOk={handleSave} onCancel={() => { setModalOpen(false); setEditing(null); }}>
        <Form form={form} layout="vertical">
          <Form.Item name="model_group_id" label="Model Group" rules={[{ required: true }]}>
            <Select options={groups.map(g => ({ value: g.id, label: `${g.group_id} (${g.name})` }))} />
          </Form.Item>
          <Form.Item name="provider_id" label="Provider" rules={[{ required: true }]}>
            <Select options={providers.map(p => ({ value: p.id, label: p.name }))} />
          </Form.Item>
          <Form.Item name="target_model" label="Target Model (leave empty to use incoming model name)">
            <Input placeholder="gpt-4o-2024-08-06" />
          </Form.Item>
          <Space size="large">
            <Form.Item name="priority" label="Priority (lower=higher)"><InputNumber min={1} /></Form.Item>
            <Form.Item name="weight" label="Weight" initialValue={10}><InputNumber min={1} /></Form.Item>
          </Space>
          <Form.Item name="enabled" label="Enabled" valuePropName="checked"><Switch defaultChecked /></Form.Item>
        </Form>
      </Modal>
    </div>
  );
};

export default RoutesPage;
