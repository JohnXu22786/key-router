import React, { useEffect, useState } from 'react';
import { Card, Form, Input, InputNumber, Button, message, Typography, Spin } from 'antd';
import { getSettings, updateSettings, reloadConfig } from '../api/client';

const { Title } = Typography;

const Settings: React.FC = () => {
  const [form] = Form.useForm();
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    const fetch = async () => {
      try {
        const res = await getSettings();
        form.setFieldsValue({
          'server.port': parseInt(res.data['server.port'] || '9998'),
          'server.auth_token': res.data['server.auth_token'] || '',
          'server.retry_times': parseInt(res.data['server.retry_times'] || '3'),
          'server.health_check_interval': parseInt(res.data['server.health_check_interval'] || '120'),
        });
      } catch { message.error('Failed to load settings'); }
      finally { setLoading(false); }
    };
    fetch();
  }, [form]);

  const handleSave = async () => {
    try {
      const values = await form.validateFields();
      const strValues: Record<string, string> = {};
      for (const [k, v] of Object.entries(values)) {
        strValues[k] = String(v);
      }
      await updateSettings(strValues);
      message.success('Settings saved');
    } catch { /* validation error */ }
  };

  if (loading) return <Spin size="large" style={{ display: 'block', margin: '100px auto' }} />;

  return (
    <div>
      <Title level={3}>Settings</Title>
      <Card style={{ maxWidth: 600 }}>
        <Form form={form} layout="vertical">
          <Form.Item name="server.port" label="Server Port">
            <InputNumber min={1024} max={65535} style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="server.auth_token" label="Auth Token (leave empty to disable)">
            <Input.Password placeholder="sk-xxx" />
          </Form.Item>
          <Form.Item name="server.retry_times" label="Max Retry Times">
            <InputNumber min={0} max={20} style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="server.health_check_interval" label="Health Check Interval (seconds)">
            <InputNumber min={10} max={3600} style={{ width: '100%' }} />
          </Form.Item>
          <Button type="primary" onClick={handleSave}>Save Settings</Button>
          <Button style={{ marginLeft: 12 }} onClick={async () => { try { await reloadConfig(); message.success('Config reloaded'); } catch { message.error('Failed to reload config'); } }}>
            Reload Config
          </Button>
        </Form>
      </Card>
    </div>
  );
};

export default Settings;
