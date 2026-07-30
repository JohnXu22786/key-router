import React, { useEffect, useState } from 'react';
import { Table, Button, Modal, Form, Input, Switch, InputNumber, message, Space, Typography, Popconfirm, Tag } from 'antd';
import { PlusOutlined, EditOutlined, DeleteOutlined } from '@ant-design/icons';
import { getModelGroups, createModelGroup, updateModelGroup, deleteModelGroup, ModelGroup } from '../api/client';

const { Title } = Typography;

const ModelGroups: React.FC = () => {
  const [groups, setGroups] = useState<ModelGroup[]>([]);
  const [loading, setLoading] = useState(false);
  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<ModelGroup | null>(null);
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

  const handleSave = async () => {
    try {
      const values = await form.validateFields();
      if (editing) { await updateModelGroup(editing.id, values); message.success('Updated'); }
      else { await createModelGroup(values); message.success('Created'); }
      setModalOpen(false); setEditing(null); form.resetFields(); fetch();
    } catch { message.error('Failed to save model group'); }
  };

  const handleDelete = async (id: number) => {
    try {
      await deleteModelGroup(id); message.success('Deleted'); fetch();
    } catch { message.error('Failed to delete model group'); }
  };

  const columns = [
    { title: 'ID', dataIndex: 'id', key: 'id', width: 60 },
    { title: 'Group ID', dataIndex: 'group_id', key: 'group_id', render: (g: string) => <Tag color="blue">{g}</Tag> },
    { title: 'Name', dataIndex: 'name', key: 'name' },
    { title: 'Enabled', dataIndex: 'enabled', key: 'enabled', render: (e: boolean) => e ? <Tag color="green">Yes</Tag> : <Tag color="red">No</Tag> },
    { title: 'Retry Times', dataIndex: 'retry_times', key: 'retry_times' },
    {
      title: 'Actions', key: 'actions',
      render: (_: unknown, r: ModelGroup) => (
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
      <Title level={3}>Model Groups</Title>
      <Button type="primary" icon={<PlusOutlined />} onClick={() => { setEditing(null); form.resetFields(); setModalOpen(true); }} style={{ marginBottom: 16 }}>
        Add Model Group
      </Button>
      <Table dataSource={groups} columns={columns} rowKey="id" loading={loading} />
      <Modal title={editing ? 'Edit Model Group' : 'Add Model Group'} open={modalOpen} onOk={handleSave} onCancel={() => { setModalOpen(false); setEditing(null); }}>
        <Form form={form} layout="vertical">
          <Form.Item name="group_id" label="Group ID (matches incoming model name)" rules={[{ required: true }]}>
            <Input placeholder="gpt-4o" />
          </Form.Item>
          <Form.Item name="name" label="Display Name"><Input placeholder="GPT-4o Pool" /></Form.Item>
          <Form.Item name="enabled" label="Enabled" valuePropName="checked"><Switch defaultChecked /></Form.Item>
          <Form.Item name="retry_times" label="Retry Times"><InputNumber min={0} max={20} /></Form.Item>
        </Form>
      </Modal>
    </div>
  );
};

export default ModelGroups;
