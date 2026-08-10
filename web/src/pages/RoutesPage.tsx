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
  // Always-current routes for drag handling — avoids stale-closure reorders
  // when a second drop or a background fetch lands before a re-render
  const routesRef = useRef<Route[]>([]);
  routesRef.current = routes;

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
      // Only new routes get defaults; editing must keep the stored
      // priority (drag reorder would otherwise be reset)
      if (editing) { await updateRoute(editing.id, values); message.success('Updated'); }
      else { await createRoute({ ...values, priority: 0 }); message.success('Created'); }
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
      // Priorities are PER-GROUP (0..n-1 within each model group): the
      // table mixes groups, so a global index would silently renumber every
      // other group's routes.
      const groupCounts: Record<number, number> = {};
      const payload = ordered.map(r => {
        const idx = groupCounts[r.model_group_id] ?? 0;
        groupCounts[r.model_group_id] = idx + 1;
        return { id: r.id, priority: idx };
      });
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

    // Priorities are per-group: crossing group boundaries can't be persisted,
    // so reject it instead of showing an order that reverts on fetch.
    const current = routesRef.current;
    if (current[dragIndex].model_group_id !== current[dropIndex].model_group_id) {
      message.warning('Routes can only be reordered within the same model group');
      dragItem.current = null;
      return;
    }

    const newRoutes = [...current];
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
      title: 'Actions', key: 'actions', width: 80,
      render: (_: unknown, r: Route) => (
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
      <Title level={3}>Routes</Title>
      <Typography.Paragraph type="secondary">
        Drag rows to reorder — the drag order IS the call order. Routes at the top are tried
        first when a request comes in. There is no weighting: position decides everything.
      </Typography.Paragraph>
      <Button type="primary" icon={<PlusOutlined />} onClick={() => { setEditing(null); form.resetFields(); setModalOpen(true); }} style={{ marginBottom: 16 }}>
        Add Route
      </Button>
      <Table
        dataSource={routes}
        columns={columns}
        rowKey="id"
        loading={loading}
        // No pagination: drag reorder persists page-relative indices as
        // priorities, which would collide across pages
        pagination={false}
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

          <Form.Item name="enabled" label="Enabled" valuePropName="checked" initialValue={true}><Switch /></Form.Item>
        </Form>
      </Modal>
    </div>
  );
};

export default RoutesPage;
