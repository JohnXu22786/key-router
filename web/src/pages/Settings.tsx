import React, { useEffect, useState, useCallback } from 'react';
import { Card, Form, Input, InputNumber, Button, message, Typography, Spin, Modal, Space, Tag, Alert, Switch, Descriptions, Divider } from 'antd';
import { KeyOutlined, ReloadOutlined, DownloadOutlined, CheckCircleOutlined, RocketOutlined, GlobalOutlined, FileTextOutlined, BugOutlined, SafetyCertificateOutlined, UserOutlined, CopyrightOutlined } from '@ant-design/icons';
import { getSettings, updateSettings, reloadConfig, checkUpdate, applyUpdate, getHealth, getAutostart, setAutostart, getAutoCheckState, UpdateInfo } from '../api/client';

const { Title } = Typography;

// Project links shown in the About card (same repo the updater queries).
const REPO_URL = 'https://github.com/JohnXu22786/key-router';

// Open a link in the system browser. The desktop shell binds "openExternal"
// (a Go function — see main.go / openurl.go) because the embedded webview
// cannot open new windows itself; in a plain browser (vite dev) the binding
// doesn't exist and we fall back to a new tab.
const openExternal = (url: string) => {
  const opener = (window as any).openExternal as ((u: string) => Promise<void>) | undefined;
  if (opener) {
    opener(url);
    return;
  }
  window.open(url, '_blank', 'noopener');
};

// Shared for onClick (left-click / keyboard) and onAuxClick (middle-click):
// the shell must never let the webview open a new window for these links,
// so any click that would navigate is routed to the system browser instead.
// Right-click (auxclick button 2) is left alone so the context menu works.
const openLink = (url: string) => (e: React.MouseEvent<HTMLElement>) => {
  if (e.type === 'auxclick' && e.button !== 1) return;
  e.preventDefault();
  openExternal(url);
};

// Descriptions labels render inside a <span> in bordered mode, so the icon
// wrapper must stay inline — a <div> (what Space renders) would be invalid
// nesting inside that span.
const iconLabel = (icon: React.ReactNode, text: string) => (
  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>{icon}{text}</span>
);

const Settings: React.FC = () => {
  const [form] = Form.useForm();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [genOpen, setGenOpen] = useState(false);
  const [pendingToken, setPendingToken] = useState('');
  // Auto-update state
  const [updateInfo, setUpdateInfo] = useState<UpdateInfo | null>(null);
  const [checking, setChecking] = useState(false);
  const [applying, setApplying] = useState(false);
  // Install mode from /updates/state: a local fact, so the Installed/Portable
  // label is correct even before the first "Check for Updates".
  const [installMode, setInstallMode] = useState<'portable' | 'installed' | null>(null);
  // Running app version from /api/health (injected at build time via ldflags)
  const [appVersion, setAppVersion] = useState<string>('');
  // Launch-at-login state
  const [autostart, setAutostartState] = useState<{ enabled: boolean; supported: boolean } | null>(null);
  const [autostartSaving, setAutostartSaving] = useState(false);

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
        // Read the build-injected version (reported by /api/health) once
        try {
          const health = await getHealth();
          setAppVersion(health.data.version || '');
        } catch { /* version is optional; the update card still works */ }
        // Launch-at-login state (Windows)
        try {
          const as = await getAutostart();
          setAutostartState(as.data);
        } catch { /* unsupported platform — hide the switch */ }
        // Install mode + last auto-check result (local endpoint, no network):
        // lets the update card label this copy correctly before the first check.
        try {
          const st = await getAutoCheckState();
          setInstallMode(st.data.install_mode);
          if (st.data.checked) setUpdateInfo(st.data);
        } catch { /* the card still works via "Check for Updates" */ }
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

  const handleAutostartChange = async (checked: boolean) => {
    setAutostartSaving(true);
    try {
      await setAutostart(checked);
      setAutostartState({ ...autostart!, enabled: checked });
      message.success(checked ? 'KeyRouter will launch when you sign in' : 'Launch at login disabled');
    } catch { message.error('Failed to update autostart setting'); }
    finally { setAutostartSaving(false); }
  };

  const handleCheckUpdate = async () => {
    setChecking(true);
    try {
      const res = await checkUpdate();
      setUpdateInfo(res.data);
      if (!res.data.update_available && !res.data.error) {
        message.success(`Already on the latest version (${res.data.latest_version})`);
      }
    } catch { message.error('Failed to check for updates'); }
    finally { setChecking(false); }
  };

  const handleApplyUpdate = async () => {
    setApplying(true);
    try {
      const res = await applyUpdate();
      message.success(res.data.install_mode === 'installed'
        ? 'Installer launched. Complete the install, then restart KeyRouter.'
        : 'Update applied — KeyRouter will restart automatically.');
      setUpdateInfo(null);
    } catch (err: any) {
      message.error(err?.response?.data?.error || 'Failed to apply update');
    }
    finally { setApplying(false); }
  };

  if (loading) return <Spin size="large" style={{ display: 'block', margin: '100px auto' }} />;

  // Install mode known from the last check (if any) or from the local
  // /updates/state facts fetched on mount.
  const mode = updateInfo?.install_mode ?? installMode;

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
          {autostart && autostart.supported && (
            <Form.Item
              label={
                <Space>
                  <RocketOutlined />
                  Launch at Login
                </Space>
              }
              tooltip="Starts KeyRouter (hidden to the tray) when you sign in to Windows. Uses the per-user registry Run key — no admin rights needed."
            >
              <Switch checked={autostart.enabled} loading={autostartSaving} onChange={handleAutostartChange} />
            </Form.Item>
          )}
          <Button type="primary" loading={saving} onClick={handleSave}>Save Settings</Button>
          <Button style={{ marginLeft: 12 }} onClick={handleReload}>Reload Config</Button>
        </Form>
      </Card>

      <Card title="Software Update" style={{ marginTop: 16 }}
        extra={
          <Space>
            <Tag color={updateInfo?.update_available ? 'gold' : 'default'}>
              {updateInfo ? `v${updateInfo.current_version}${updateInfo.update_available ? ` → v${updateInfo.latest_version}` : ' (latest)'}` : appVersion ? `v${appVersion}` : '—'}
            </Tag>
            <Tag>{mode === 'installed' ? 'Installed' : mode === 'portable' ? 'Portable' : '—'}</Tag>
          </Space>
        }
      >
        <Space direction="vertical" style={{ width: '100%' }}>
          {mode && (
            <Typography.Paragraph type="secondary" style={{ marginBottom: 8 }}>
              {mode === 'installed'
                ? 'This is an installed copy: updates download and launch the setup installer.'
                : 'This is a portable copy: updates download and replace the executable directly.'}
            </Typography.Paragraph>
          )}
          {updateInfo?.error && (
            <Alert type="warning" showIcon message={`Update check failed: ${updateInfo.error}`} />
          )}
          {updateInfo?.update_available && (
            <Alert
              type="info"
              showIcon
              message={`Version ${updateInfo.latest_version} is available`}
              description={updateInfo.asset_name ? `Asset: ${updateInfo.asset_name}${updateInfo.asset_size ? ` (${(updateInfo.asset_size / 1024 / 1024).toFixed(1)} MB)` : ''}` : undefined}
            />
          )}
          <Space>
            <Button icon={<ReloadOutlined />} loading={checking} onClick={handleCheckUpdate}>
              Check for Updates
            </Button>
            {updateInfo?.update_available && (
              <Button type="primary" icon={<DownloadOutlined />} loading={applying} onClick={handleApplyUpdate}>
                {updateInfo.install_mode === 'installed' ? 'Download & Launch Installer' : 'Download & Update Now'}
              </Button>
            )}
            {updateInfo && !updateInfo.update_available && !updateInfo.error && (
              <Typography.Text type="secondary"><CheckCircleOutlined /> Up to date</Typography.Text>
            )}
          </Space>
        </Space>
      </Card>

      <Card title="About" style={{ marginTop: 16 }}>
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          <Space>
            <Typography.Text strong>KeyRouter</Typography.Text>
            {appVersion && <Tag color="blue">v{appVersion}</Tag>}
          </Space>
          <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
            Local AI API gateway — multi-key management, failover, format conversion, rate limiting and billing.
          </Typography.Paragraph>
          <Divider style={{ margin: 0 }} />
          <Descriptions column={1} size="small" bordered>
            <Descriptions.Item label={iconLabel(<CopyrightOutlined />, 'License')}>
              <Tag color="green">AGPL-3.0</Tag>{' '}
              <Typography.Link
                href={`${REPO_URL}/blob/main/LICENSE`}
                target="_blank"
                rel="noreferrer"
                onClick={openLink(`${REPO_URL}/blob/main/LICENSE`)}
                onAuxClick={openLink(`${REPO_URL}/blob/main/LICENSE`)}
              >
                View license text
              </Typography.Link>
            </Descriptions.Item>
            <Descriptions.Item label={iconLabel(<GlobalOutlined />, 'Homepage')}>
              <Typography.Link
                href={REPO_URL}
                target="_blank"
                rel="noreferrer"
                onClick={openLink(REPO_URL)}
                onAuxClick={openLink(REPO_URL)}
              >
                {REPO_URL}
              </Typography.Link>
            </Descriptions.Item>
            <Descriptions.Item label={iconLabel(<DownloadOutlined />, 'Releases')}>
              <Typography.Link
                href={`${REPO_URL}/releases`}
                target="_blank"
                rel="noreferrer"
                onClick={openLink(`${REPO_URL}/releases`)}
                onAuxClick={openLink(`${REPO_URL}/releases`)}
              >
                Download the latest version
              </Typography.Link>
            </Descriptions.Item>
            <Descriptions.Item label={iconLabel(<FileTextOutlined />, 'Documentation')}>
              <Typography.Link
                href={`${REPO_URL}/blob/main/README.md`}
                target="_blank"
                rel="noreferrer"
                onClick={openLink(`${REPO_URL}/blob/main/README.md`)}
                onAuxClick={openLink(`${REPO_URL}/blob/main/README.md`)}
              >
                README — quick start & usage guide
              </Typography.Link>
            </Descriptions.Item>
            <Descriptions.Item label={iconLabel(<BugOutlined />, 'Issues & Feedback')}>
              <Typography.Link
                href={`${REPO_URL}/issues`}
                target="_blank"
                rel="noreferrer"
                onClick={openLink(`${REPO_URL}/issues`)}
                onAuxClick={openLink(`${REPO_URL}/issues`)}
              >
                Report bugs or request features
              </Typography.Link>
            </Descriptions.Item>
            <Descriptions.Item label={iconLabel(<SafetyCertificateOutlined />, 'Security')}>
              <Typography.Link
                href={`${REPO_URL}/security/advisories/new`}
                target="_blank"
                rel="noreferrer"
                onClick={openLink(`${REPO_URL}/security/advisories/new`)}
                onAuxClick={openLink(`${REPO_URL}/security/advisories/new`)}
              >
                Report a vulnerability privately
              </Typography.Link>
            </Descriptions.Item>
            <Descriptions.Item label={iconLabel(<UserOutlined />, 'Maintainer')}>
              <Typography.Link
                href="https://github.com/JohnXu22786"
                target="_blank"
                rel="noreferrer"
                onClick={openLink('https://github.com/JohnXu22786')}
                onAuxClick={openLink('https://github.com/JohnXu22786')}
              >
                John Tsui
              </Typography.Link>
            </Descriptions.Item>
          </Descriptions>
        </Space>
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
