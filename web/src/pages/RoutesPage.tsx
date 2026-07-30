import React, { useEffect, useState, useCallback, useRef } from 'react';
import { Table, Button, Modal, Form, Input, Select, Switch, message, Space, Typography, Popconfirm, Tag } from 'antd';
import { PlusOutlined, EditOutlined, DeleteOutlined, HolderOutlined } from '@ant-design/icons';
import { getRoutes, createRoute, updateRoute, deleteRoute, reorderRoutes, getProviders, getModelGroups, Route, Provider, ModelGroup } from '../api/client';

const { Title } = Typography;

const RoutesPage: React.FC = () => {
  const [routes, setRoutes] = useState<Route[]>([]);
  const [providers, setProviders] = useState<Provider[]>([]);
  const [groups, setGroups] = useState<ModelGroup[]>([]);
  const [loading, setLoading] = useState(false);
  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<Route | null>(null);
  const [form] = Form.useForm();
  const dragItem = useRef<number | null>(null);
  const persistTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

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
      const data = { ...values, priority: 0, weight: 10 };
      if (editing) { await updateRoute(editing.id, data); message.success('Updated'); }
      else { await createRoute(data); message.success('Created'); }
      setModalOpen(false); setEditing(null); form.resetFields(); fetch();
    } catch { message.error('Failed to save route'); }
  };

  const handleDelete = async (id: number) => {
    try {
      await deleteRoute(id); message.success('Deleted'); fetch();
    } catch { message.error('Failed to delete route'); }
  };

  const persistOrder = useCallback((ordered: Route[]) => {
    // Debounce: cancel previous pending save
    if (persistTimer.current) clearTimeout(persistTimer.current);
    persistTimer.current = setTimeout(async () => {
      const payload = ordered.map((r, i) => ({ id: r.id, priority: i }));
      try {
        await reorderRoutes(payload);
      } catch { message.error('Failed to save order'); }
    }, 300);
  }, []);

  // Drag handlers
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

    const newRoutes = [...routes];
    const [removed] = newRoutes.splice(dragIndex, 1);
    newRoutes.splice(dropIndex, 0, removed);
    setRoutes(newRoutes);
    dragItem.current = null;
    persistOrder(newRoutes);
  };

  const columns = [
    {
      title: '', key: 'drag', width: 40,
      render: () => <HolderOutlined style={{ cursor: 'grab', color: '#999' }} />,
    },
    { title: 'ID', dataIndex: 'id', key: 'id', width: 60 },
    {
      title: 'Model Group', key: 'group',
      render: (_: unknown, r: Route) => r.model_group?.group_id || `#${r.model_group_id}`,
    },
    {
      title: 'Provider', key: 'provider',
      render: (_: unknown, r: Route) => r.provider?.name || `#${r.provider_id}`,
    },
    { title: 'Target Model', dataIndex: 'target_model', key: 'target_model', render: (t: string) => t ? <Tag>{t}</Tag> : <Tag color="default">same</Tag> },
    { title: 'Enabled', dataIndex: 'enabled', key: 'enabled', render: (e: boolean) => e ? <Tag color="green">Yes</Tag> : <Tag color="red">No</Tag> },
    {
      title: 'Actions', key: 'actions', width: 140,
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
      <Typography.Paragraph type="secondary">
        Drag rows to reorder. Routes at the top are tried first when a request comes in.
      </Typography.Paragraph>
      <Button type="primary" icon={<PlusOutlined />} onClick={() => { setEditing(null); form.resetFields(); setModalOpen(true); }} style={{ marginBottom: 16 }}>
        Add Route
      </Button>
      <Table
        dataSource={routes}
        columns={columns}
        rowKey="id"
        loading={loading}
        onRow={(_, index) => ({
          draggable: true,
          onDragStart: (e) => handleDragStart(e, index!),
          onDragOver: handleDragOver,
          onDrop: (e) => handleDrop(e, index!),
          style: { cursor: 'default' },
        })}
      />
      <Modal title={editing ? 'Edit Route' : 'Add Route'} open={modalOpen} onOk={handleSave} onCancel={() => { setModalOpen(false); setEditing(null); }}>
        <Form form={form} layout="vertical">
          <Form.Item name="model_group_id" label="Model Group" rules={[{ required: true }]}>
            <Select placeholder="Select model group" options={groups.map(g => ({ value: g.id, label: `${g.group_id} (${g.name})` }))} />
          </Form.Item>
          <Form.Item name="provider_id" label="Provider" rules={[{ required: true }]}>
            <Select placeholder="Select provider" options={providers.map(p => ({ value: p.id, label: p.name }))} />
          </Form.Item>
          <Form.Item name="target_model" label="Target Model (leave empty to use incoming model name)">
            <Input placeholder="gpt-4o-2024-08-06" />
          </Form.Item>
          <Form.Item name="enabled" label="Enabled" valuePropName="checked"><Switch defaultChecked /></Form.Item>
        </Form>
      </Modal>
    </div>
  );
};

export default RoutesPage;
