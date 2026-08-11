import React, { useEffect, useState, useCallback, useRef } from 'react';
import {
  Table, Button, Modal, Form, Input, InputNumber, Select, Switch, message, Space,
  Typography, Popconfirm, Tag, Alert, Collapse,
} from 'antd';
import {
  PlusOutlined, EditOutlined, DeleteOutlined, HolderOutlined,
} from '@ant-design/icons';
import {
  getModelGroups, createModelGroup, updateModelGroup, deleteModelGroup,
  getRoutes, createRoute, updateRoute, deleteRoute, reorderRoutes,
  getProviders, ModelGroup, Route, Provider,
} from '../api/client';
import JsonEditor from '../components/JsonEditor';
import { useDragSort } from '../hooks/useDragSort';

const { Title, Text } = Typography;

// Models page: Model Groups are the top-level rows; each expands to manage
// that group's Routes (drag order = call order, per-route pricing and extra
// params). Both "Add Model Group" and "Add Route" live at the top of the
// page (blue buttons), matching the original per-page layout.
const Models: React.FC = () => {
  const [groups, setGroups] = useState<ModelGroup[]>([]);
  const [routes, setRoutes] = useState<Route[]>([]);
  const [providers, setProviders] = useState<Provider[]>([]);
  const [loading, setLoading] = useState(false);
  const [groupModal, setGroupModal] = useState(false);
  const [routeModal, setRouteModal] = useState(false);
  const [editingGroup, setEditingGroup] = useState<ModelGroup | null>(null);
  const [editingRoute, setEditingRoute] = useState<Route | null>(null);
  const [activeGroups, setActiveGroups] = useState<string[]>([]);
  const [extraError, setExtraError] = useState('');
  const [groupForm] = Form.useForm();
  const [routeForm] = Form.useForm();
  const persistTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const routesRef = useRef<Route[]>([]);
  routesRef.current = routes;

  // Drag-reorder with live preview animation (routes within a model group).
  const drag = useDragSort<Route>(
    routes,
    (from, to) => routes[from]?.model_group_id === routes[to]?.model_group_id,
    (next) => { setRoutes(next); persistOrder(next); },
  );

  const fetch = async () => {
    setLoading(true);
    try {
      const [g, r, p] = await Promise.all([getModelGroups(), getRoutes(), getProviders()]);
      setGroups(g.data); setRoutes(r.data); setProviders(p.data);
    } catch { message.error('Failed to load models'); }
    finally { setLoading(false); }
  };

  useEffect(() => { fetch(); }, []);

  // Poll for status changes (route/group state, key health). Keeps expanded
  // groups and scroll position — only row data updates.
  useEffect(() => {
    const t = setInterval(() => {
      Promise.all([getModelGroups(), getRoutes(), getProviders()])
        .then(([g, r, p]) => { setGroups(g.data); setRoutes(r.data); setProviders(p.data); })
        .catch(() => {});
    }, 10000);
    return () => clearInterval(t);
  }, []);

  // ---- Model Group CRUD ----
  const saveGroup = async () => {
    try {
      const values = await groupForm.validateFields();
      if (editingGroup) { await updateModelGroup(editingGroup.id, values); message.success('Updated'); }
      else { await createModelGroup(values); message.success('Created'); }
      setGroupModal(false); setEditingGroup(null); groupForm.resetFields(); fetch();
    } catch { message.error('Failed to save model group'); }
  };

  const deleteGroup = async (id: number) => {
    try {
      await deleteModelGroup(id); message.success('Deleted'); fetch();
    } catch { message.error('Failed to delete model group'); }
  };

  // ---- Route CRUD ----
  const saveRoute = async () => {
    try {
      const values = await routeForm.validateFields();
      if (values.extra_params != null) values.extra_params = values.extra_params.trim();
      if (values.extra_params && extraError) {
        message.error('Fix the JSON in Extra Params first');
        return;
      }
      if (editingRoute) { await updateRoute(editingRoute.id, values); message.success('Updated'); }
      else { await createRoute({ ...values, priority: 0 }); message.success('Created'); }
      setRouteModal(false); setEditingRoute(null); routeForm.resetFields(); setExtraError(''); fetch();
    } catch { message.error('Failed to save route'); }
  };

  const deleteRoute = async (id: number) => {
    try {
      await deleteRoute(id); message.success('Deleted'); fetch();
    } catch { message.error('Failed to delete route'); }
  };

  const persistOrder = useCallback((ordered: Route[]) => {
    if (persistTimer.current) clearTimeout(persistTimer.current);
    persistTimer.current = setTimeout(async () => {
      const groupCounts: Record<number, number> = {};
      const payload = ordered.map(r => {
        const idx = groupCounts[r.model_group_id] ?? 0;
        groupCounts[r.model_group_id] = idx + 1;
        return { id: r.id, priority: idx };
      });
      try { await reorderRoutes(payload); } catch { message.error('Failed to save order'); }
    }, 300);
  }, []);

  const routeColumns = [
    {
      title: '', key: 'drag', width: 40,
      render: () => <HolderOutlined style={{ cursor: 'grab', color: '#999' }} />,
    },
    {
      title: 'Provider', key: 'provider',
      render: (_: unknown, r: Route) => r.provider?.name || `#${r.provider_id}`,
    },
    { title: 'Target Model', dataIndex: 'target_model', key: 'target_model', render: (t: string) => t ? <Tag>{t}</Tag> : <Tag color="default">same</Tag> },
    {
      title: 'Pricing ($/1M)', key: 'pricing',
      render: (_: unknown, r: Route) => {
        const hasPrice = r.prompt_per_1m || r.completion_per_1m;
        return hasPrice
          ? <Tag color="blue">${r.prompt_per_1m.toFixed(4)} / ${r.completion_per_1m.toFixed(4)}</Tag>
          : <Tag>inherit</Tag>;
      },
    },
    { title: 'Enabled', dataIndex: 'enabled', key: 'enabled', render: (e: boolean) => e ? <Tag color="green">Yes</Tag> : <Tag color="red">No</Tag> },
    {
      title: 'Actions', key: 'actions', width: 80,
      render: (_: unknown, r: Route) => (
        <Space>
          <Button icon={<EditOutlined />} size="small" onClick={() => { setEditingRoute(r); setExtraError(''); routeForm.setFieldsValue(r); setRouteModal(true); }} title="Edit" />
          <Popconfirm title="Delete?" onConfirm={() => deleteRoute(r.id)}>
            <Button icon={<DeleteOutlined />} size="small" danger title="Delete" />
          </Popconfirm>
        </Space>
      ),
    },
  ];

  // ---- Top-level toolbar: BOTH add buttons live here (per-page style) ----
  const openAddRoute = () => {
    setEditingRoute(null);
    routeForm.resetFields();
    // Pre-select the first expanded group when there is one, else the first group.
    const firstId = activeGroups.length
      ? parseInt(activeGroups[0], 10)
      : (groups[0]?.id || undefined);
    routeForm.setFieldsValue({ model_group_id: firstId, enabled: true });
    setRouteModal(true);
  };

  const openAddGroup = () => {
    setEditingGroup(null);
    groupForm.resetFields();
    setGroupModal(true);
  };

  return (
    <div>
      <Title level={3}>Models</Title>
      <Typography.Paragraph type="secondary">
        Model groups are the models your API keys can serve. Expand a group to manage its
        routes — drag to reorder (drag order IS the call order), set per-route pricing and
        extra params.
      </Typography.Paragraph>
      <Space style={{ marginBottom: 16 }}>
        <Button type="primary" icon={<PlusOutlined />} onClick={openAddGroup}>Add Model Group</Button>
        <Button type="primary" icon={<PlusOutlined />} onClick={openAddRoute}>Add Route</Button>
      </Space>

      {groups.length === 0 && !loading && (
        <Typography.Text type="secondary">No model groups yet. Add a model group to get started.</Typography.Text>
      )}

      {groups.map(g => (
        <Collapse
          key={g.id}
          style={{ marginBottom: 12 }}
          activeKey={activeGroups}
          onChange={(keys: any) => setActiveGroups(Array.isArray(keys) ? keys.map(String) : [keys].map(String))}
          items={[{
            key: String(g.id),
            label: (
              <Space>
                <Tag color="blue">{g.group_id}</Tag>
                <Text strong>{g.name}</Text>
                {g.context_length > 0 && <Text type="secondary" style={{ fontSize: 12 }}>{g.context_length.toLocaleString()} ctx</Text>}
                <Tag>{g.enabled ? 'enabled' : 'disabled'}</Tag>
                <Text type="secondary" style={{ fontSize: 12 }}>{routes.filter(r => r.model_group_id === g.id).length} route{routes.filter(r => r.model_group_id === g.id).length === 1 ? '' : 's'}</Text>
              </Space>
            ),
            extra: (
              <Space>
                <Button size="small" icon={<EditOutlined />} onClick={(e) => { e.stopPropagation(); setEditingGroup(g); groupForm.setFieldsValue(g); setGroupModal(true); }} title="Edit group" />
                <Popconfirm title="Delete (removes its routes)?" onConfirm={() => deleteGroup(g.id)}>
                  <Button size="small" danger icon={<DeleteOutlined />} onClick={(e) => e.stopPropagation()} title="Delete group" />
                </Popconfirm>
              </Space>
            ),
            children: (
              <Table
                dataSource={routes.filter(r => r.model_group_id === g.id)}
                columns={routeColumns}
                rowKey="id"
                size="small"
                pagination={false}
                locale={{ emptyText: 'No routes yet — use the Add Route button above.' }}
                onRow={(_, index) => {
                  const all = routes.filter(r => r.model_group_id === g.id);
                  const globalIndex = routes.indexOf(all[index!]);
                  return {
                    draggable: true,
                    onDragStart: (e) => drag.onDragStart(e, globalIndex),
                    onDragOver: (e) => drag.onDragOver(e, globalIndex),
                    onDrop: drag.onDrop,
                    onDragEnd: drag.onDragEnd,
                    style: { cursor: 'default', ...drag.rowStyle(globalIndex) },
                  };
                }}
              />
            ),
          }]}
        />
      ))}

      {/* Model Group modal */}
      <Modal title={editingGroup ? 'Edit Model Group' : 'Add Model Group'} open={groupModal} onOk={saveGroup} onCancel={() => { setGroupModal(false); setEditingGroup(null); }} width={560}>
        <Form form={groupForm} layout="vertical">
          <Form.Item name="group_id" label="Group ID (matches incoming model name)" rules={[{ required: true }]}>
            <Input placeholder="gpt-4o" />
          </Form.Item>
          <Form.Item name="name" label="Display Name"><Input placeholder="GPT-4o Pool" /></Form.Item>
          <Form.Item name="enabled" label="Enabled" valuePropName="checked" initialValue={true}><Switch /></Form.Item>
          <Form.Item name="retry_times" label="Retry Times" tooltip="0 = use the global retry setting (Settings page). A positive value overrides it for this group.">
            <InputNumber min={0} max={20} />
          </Form.Item>
          <Space size="large" wrap>
            <Form.Item name="context_length" label="Context Length (tokens)" tooltip="Exposed via GET /v1/models. 0 = unknown.">
              <InputNumber min={0} step={1000} style={{ width: 180 }} placeholder="128000" />
            </Form.Item>
            <Form.Item name="max_output_tokens" label="Max Output (tokens)" tooltip="Exposed via GET /v1/models. 0 = unknown.">
              <InputNumber min={0} step={1000} style={{ width: 180 }} placeholder="16384" />
            </Form.Item>
          </Space>
        </Form>
      </Modal>

      {/* Route modal */}
      <Modal title={editingRoute ? 'Edit Route' : 'Add Route'} open={routeModal} onOk={saveRoute} onCancel={() => { setRouteModal(false); setEditingRoute(null); }} width={640}>
        <Form form={routeForm} layout="vertical">
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
          <Typography.Text strong>Per-Route Pricing ($/1M tokens)</Typography.Text>
          <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
            Leave all at 0 to inherit the Pricing table for this route's model.
          </Typography.Paragraph>
          <Space size="large" wrap style={{ marginBottom: 8 }}>
            <Form.Item name="prompt_per_1m" label="Prompt $/1M"><InputNumber min={0} step={0.01} style={{ width: 130 }} /></Form.Item>
            <Form.Item name="completion_per_1m" label="Completion $/1M"><InputNumber min={0} step={0.01} style={{ width: 130 }} /></Form.Item>
            <Form.Item name="cache_read_per_1m" label="Cache Read $/1M"><InputNumber min={0} step={0.01} style={{ width: 130 }} /></Form.Item>
            <Form.Item name="cache_write_per_1m" label="Cache Write $/1M"><InputNumber min={0} step={0.01} style={{ width: 130 }} /></Form.Item>
          </Space>
          <Form.Item
            name="extra_params"
            label="Extra Params (JSON object)"
            extra="Merged into every request this route serves. Overrides the client's values — e.g. {&quot;temperature&quot;: 0.2}. Auto-pairing quotes/brackets and indentation supported."
          >
            <JsonEditor
              rows={14}
              placeholder={'{\n  "temperature": 0.2\n}'}
              onValid={(valid) => setExtraError(valid ? '' : 'Fix the JSON in Extra Params first')}
            />
          </Form.Item>
          {extraError && <Alert type="error" showIcon message={extraError} style={{ marginBottom: 8 }} />}
        </Form>
      </Modal>
    </div>
  );
};

export default Models;
