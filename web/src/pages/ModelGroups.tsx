import React, { useEffect, useState } from 'react';
import { Table, Button, Modal, Form, Input, Switch, InputNumber, message, Space, Typography, Popconfirm, Tag, Alert } from 'antd';
import { PlusOutlined, EditOutlined, DeleteOutlined } from '@ant-design/icons';
import { getModelGroups, createModelGroup, updateModelGroup, deleteModelGroup, ModelGroup } from '../api/client';

const { Title } = Typography;

const ModelGroups: React.FC = () => {
  const [groups, setGroups] = useState<ModelGroup[]>([]);
  const [loading, setLoading] = useState(false);
  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<ModelGroup | null>(null);
  const [extraError, setExtraError] = useState('');
  const [form] = Form.useForm();

  const fetch = async () => {
    setLoading(true);
    try {
      const res = await getModelGroups();
      setGroups(res.data);
    } catch { message.error('Failed to load model groups'); }
    finally { setLoading(false); }
  };

  useEffect(() => { fetch(); }, []);

  // JSON editor: validate as the user types, show inline feedback.
  const onExtraChange = (v: string) => {
    const s = (v || '').trim();
    if (s === '') { setExtraError(''); return; }
    try {
      const parsed = JSON.parse(s);
      if (Array.isArray(parsed) || typeof parsed !== 'object' || parsed === null) {
        setExtraError('Must be a JSON object, e.g. {"temperature": 0.2}');
      } else {
        setExtraError('');
      }
    } catch (e: any) {
      setExtraError(e.message || 'Invalid JSON');
    }
  };

  const handleSave = async () => {
    try {
      const values = await form.validateFields();
      // Strip trailing whitespace; empty string = no extra params.
      if (values.extra_params != null) values.extra_params = values.extra_params.trim();
      if (values.extra_params && extraError) {
        message.error('Fix the JSON in Extra Params first');
        return;
      }
      if (editing) { await updateModelGroup(editing.id, values); message.success('Updated'); }
      else { await createModelGroup(values); message.success('Created'); }
      setModalOpen(false); setEditing(null); form.resetFields(); setExtraError(''); fetch();
    } catch { message.error('Failed to save model group'); }
  };

  const handleDelete = async (id: number) => {
    try {
      await deleteModelGroup(id); message.success('Deleted'); fetch();
    } catch { message.error('Failed to delete model group'); }
  };

  const columns = [
    { title: 'Group ID', dataIndex: 'group_id', key: 'group_id', render: (g: string) => <Tag color="blue">{g}</Tag> },
    { title: 'Name', dataIndex: 'name', key: 'name' },
    { title: 'Enabled', dataIndex: 'enabled', key: 'enabled', render: (e: boolean) => e ? <Tag color="green">Yes</Tag> : <Tag color="red">No</Tag> },
    { title: 'Retry Times', dataIndex: 'retry_times', key: 'retry_times' },
    { title: 'Extra Params', dataIndex: 'extra_params', key: 'extra_params', ellipsis: true, render: (v: string) => v ? <Typography.Text code style={{ fontSize: 12 }}>{v}</Typography.Text> : <Typography.Text type="secondary">—</Typography.Text> },
    {
      title: 'Actions', key: 'actions', width: 80,
      render: (_: unknown, r: ModelGroup) => (
        <Space>
          <Button icon={<EditOutlined />} size="small" onClick={() => { setEditing(r); setExtraError(''); form.setFieldsValue(r); setModalOpen(true); }} title="Edit" />
          <Popconfirm title="Delete?" onConfirm={() => handleDelete(r.id)}>
            <Button icon={<DeleteOutlined />} size="small" danger title="Delete" />
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <div>
      <Title level={3}>Model Groups</Title>
      <Button type="primary" icon={<PlusOutlined />} onClick={() => { setEditing(null); form.resetFields(); setExtraError(''); setModalOpen(true); }} style={{ marginBottom: 16 }}>
        Add Model Group
      </Button>
      <Table dataSource={groups} columns={columns} rowKey="id" loading={loading} />
      <Modal title={editing ? 'Edit Model Group' : 'Add Model Group'} open={modalOpen} onOk={handleSave} onCancel={() => { setModalOpen(false); setEditing(null); }} width={640}>
        <Form form={form} layout="vertical">
          <Form.Item name="group_id" label="Group ID (matches incoming model name)" rules={[{ required: true }]}>
            <Input placeholder="gpt-4o" />
          </Form.Item>
          <Form.Item name="name" label="Display Name"><Input placeholder="GPT-4o Pool" /></Form.Item>
          <Form.Item name="enabled" label="Enabled" valuePropName="checked" initialValue={true}><Switch /></Form.Item>
          <Form.Item name="retry_times" label="Retry Times" tooltip="0 = use the global retry setting (Settings page). A positive value overrides it for this group.">
            <InputNumber min={0} max={20} />
          </Form.Item>
          <Space size="large" wrap>
            <Form.Item name="context_length" label="Context Length (tokens)" tooltip="Exposed via GET /v1/models so tools like opencode can auto-configure limits. 0 = unknown.">
              <InputNumber min={0} step={1000} style={{ width: 180 }} placeholder="128000" />
            </Form.Item>
            <Form.Item name="max_output_tokens" label="Max Output (tokens)" tooltip="Exposed via GET /v1/models. 0 = unknown.">
              <InputNumber min={0} step={1000} style={{ width: 180 }} placeholder="16384" />
            </Form.Item>
          </Space>
          <Form.Item
            name="extra_params"
            label="Extra Params (JSON object)"
            extra='Merged into every forwarded request body. Keys you set here OVERRIDE the client-sent values — e.g. {"temperature": 0.2} pins sampling temperature.'
          >
            <Input.TextArea
              rows={5}
              placeholder={'{\n  "temperature": 0.2\n}'}
              style={{ fontFamily: 'monospace', fontSize: 12 }}
              onChange={(e) => onExtraChange(e.target.value)}
            />
          </Form.Item>
          {extraError && <Alert type="error" showIcon message={extraError} style={{ marginBottom: 8 }} />}
        </Form>
      </Modal>
    </div>
  );
};

export default ModelGroups;
