import React, { useEffect, useState, useCallback } from 'react';
import { Card, Form, Input, InputNumber, Button, message, Typography, Spin, Modal, Space } from 'antd';
import { KeyOutlined, ReloadOutlined } from '@ant-design/icons';
import { getSettings, updateSettings, reloadConfig } from '../api/client';

const { Title } = Typography;

const Settings: React.FC = () => {
  const [form] = Form.useForm();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [genOpen, setGenOpen] = useState(false);
  const [pendingToken, setPendingToken] = useState('');

  const generateToken = useCallback(() => {
    const chars = 'abcdefghijklmnopqrstuvwxyz0123456789';
    const array = new Uint8Array(48);
    crypto.getRandomValues(array);
    // Rejection sampling: avoid the modulo bias (chars 0-3 would otherwise
    // be drawn 8/256 times vs 7/256 for the rest)
    let token = 'sk-';
    for (let i = 0; i < 48; i++) {
      let idx = 256;
      while (idx >= chars.length * Math.floor(256 / chars.length)) {
        crypto.getRandomValues(array);
        idx = array[0];
      }
      token += chars.charAt(idx % chars.length);
    }
    return token;
  }, []);

  const handleGenerateClick = () => {
    const token = generateToken();
    setPendingToken(token);
    setGenOpen(true);
  };

  const handleConfirmToken = () => {
    form.setFieldsValue({ 'server.auth_token': pendingToken });
    setGenOpen(false);
    message.info('Token generated. Click "Save Settings" to apply.');
  };

  useEffect(() => {
    const fetchData = async () => {
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
    fetchData();
  }, [form]);

  const handleSave = async () => {
    setSaving(true);
    try {
      const values = await form.validateFields();
      const strValues: Record<string, string> = {};
      for (const [k, v] of Object.entries(values)) {
        strValues[k] = String(v);
      }
      await updateSettings(strValues);
      message.success('Settings saved');
    } catch (err) {
      if (err && typeof err === 'object' && 'errorFields' in err) {
        // validation error, form will show field errors
      } else {
        message.error('Failed to save settings');
      }
    } finally { setSaving(false); }
  };

  const handleReload = async () => {
    try {
      await reloadConfig();
      message.success('Config reloaded');
    } catch { message.error('Failed to reload config'); }
  };

  if (loading) return <Spin size="large" style={{ display: 'block', margin: '100px auto' }} />;

  return (
    <div>
      <Title level={3}>Settings</Title>
      <Card>
        <Form form={form} layout="vertical">
          <Form.Item name="server.port" label="Server Port" tooltip="Takes effect after restarting the app.">
            <InputNumber min={1024} max={65535} style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="server.auth_token" label="Auth Token (leave empty to disable)">
            <Input.Password
              placeholder="sk-xxx"
              addonAfter={<Button size="small" type="text" onClick={handleGenerateClick}><KeyOutlined /> Generate</Button>}
            />
          </Form.Item>
          <Form.Item name="server.retry_times" label="Max Retry Times" tooltip="0 disables retries (single attempt). Groups with a per-group retry_times > 0 override this.">
            <InputNumber min={0} max={20} style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="server.health_check_interval" label="Health Check Interval (seconds)">
            <InputNumber min={10} max={3600} style={{ width: '100%' }} />
          </Form.Item>
          <Button type="primary" loading={saving} onClick={handleSave}>Save Settings</Button>
          <Button style={{ marginLeft: 12 }} onClick={handleReload}>Reload Config</Button>
        </Form>
      </Card>

      <Modal
        title="Generate New Auth Token?"
        open={genOpen}
        onOk={handleConfirmToken}
        onCancel={() => setGenOpen(false)}
        okText="Use This Token"
        cancelText="Cancel"
      >
        <p>A new token will be generated:</p>
        <Typography.Text code copyable style={{ fontSize: 12, wordBreak: 'break-all' }}>
          {pendingToken}
        </Typography.Text>
        <p style={{ marginTop: 12, color: '#ff4d4f' }}>
          ⚠️ The new token takes effect immediately and will reject any clients still using the old token.
        </p>
      </Modal>
    </div>
  );
};

export default Settings;
