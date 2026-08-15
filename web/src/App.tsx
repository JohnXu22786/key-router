import React, { useEffect, useLayoutEffect, useState } from 'react';
import { BrowserRouter, Routes, Route, Link, Navigate, useLocation, useNavigate } from 'react-router-dom';
import {
  Layout, Menu, Typography, theme, ConfigProvider, Alert, Button, Space, message,
} from 'antd';
import {
  CloudServerOutlined,
  AppstoreOutlined,
  BarChartOutlined,
  SettingOutlined,
  QuestionCircleOutlined,
  DownloadOutlined,
} from '@ant-design/icons';

import Providers from './pages/Providers';
import Models from './pages/Models';
import Activity from './pages/Activity';
import Settings from './pages/Settings';
import Help from './pages/Help';
import ErrorBoundary from './components/ErrorBoundary';
import { ThemeProvider, useThemeMode } from './ThemeContext';
import { getAutoCheckState, getHealth, applyUpdate, UpdateInfo } from './api/client';

const { Sider, Content } = Layout;
const { Title } = Typography;

// The desktop shell loads this page from http://localhost:<port> — surface
// that address in the sidebar footer so clients know where to point.
const port = window.location.port || '9998';
const serverUrl = `http://localhost:${port}`;

// LayoutWithRouter is rendered inside BrowserRouter context so useLocation() works
const LayoutWithRouter: React.FC = () => {
  const [collapsed, setCollapsed] = useState(false);
  const [updateInfo, setUpdateInfo] = useState<UpdateInfo | null>(null);
  // Running app version (injected at build time, reported by /api/health)
  const [version, setVersion] = useState('');
  const [applying, setApplying] = useState(false);
  const location = useLocation();
  const navigate = useNavigate();

  // On load: write the build version into the sidebar badge, and surface the
  // last auto-check result (startup + daily, backend) — when a new version
  // exists the badge spot becomes an update button.
  useEffect(() => {
    getHealth()
      .then(res => setVersion(res.data?.version || ''))
      .catch(() => {});
    getAutoCheckState()
      .then(res => { if (res.data?.update_available) setUpdateInfo(res.data); })
      .catch(() => {});
  }, []);

  const handleApplyUpdate = async () => {
    setApplying(true);
    // The apply call blocks while the installer downloads (minutes) and the
    // UAC prompt is answered — show a persistent spinner instead of a
    // timeout error (the per-request timeout is 30 minutes, see client.ts).
    const hideLoading = message.loading(
      updateInfo?.install_mode === 'installed'
        ? 'Downloading installer and preparing update…'
        : 'Downloading and applying update…',
      0,
    );
    try {
      const res = await applyUpdate();
      message.success(res.data.install_mode === 'installed'
        ? 'Installer launched — finish the setup wizard to complete the update and restart KeyRouter.'
        : 'Update applied — KeyRouter will restart automatically.');
      setUpdateInfo(null);
    } catch (err: any) {
      message.error(err?.response?.data?.error || 'Failed to apply update');
    } finally {
      hideLoading();
      setApplying(false);
    }
  };

  const menuItems = [
    { key: '/', icon: <BarChartOutlined />, label: <Link to="/">Activity</Link> },
    { type: 'divider' as const },
    { key: '/providers', icon: <CloudServerOutlined />, label: <Link to="/providers">Providers</Link> },
    { key: '/models', icon: <AppstoreOutlined />, label: <Link to="/models">Models</Link> },
    { type: 'divider' as const },
    { key: '/settings', icon: <SettingOutlined />, label: <Link to="/settings">Settings</Link> },
    { key: '/help', icon: <QuestionCircleOutlined />, label: <Link to="/help">Help</Link> },
  ];

  return (
    <Layout style={{ minHeight: '100vh' }}>
      {/* Sider is fixed; only the content column scrolls */}
      <Sider
        collapsible
        collapsed={collapsed}
        onCollapse={setCollapsed}
        style={{
          position: 'fixed', left: 0, top: 0, bottom: 0, height: '100vh',
          display: 'flex', flexDirection: 'column', overflow: 'hidden', zIndex: 10,
          // The app has no global box-sizing reset (antd's reset.css is opt-in),
          // so the sider opts into border-box: only then does antd's -has-trigger
          // padding-bottom (48px) shrink the content box and leave room for the
          // footer above the trigger.
          boxSizing: 'border-box',
        }}
        // body = the sider's children container; column flex keeps the menu
        // scrollable while the footer stays pinned above the collapse trigger
        // (with border-box, antd's -has-trigger rule ends the body 48px above
        // the bottom — exactly at the trigger's top edge).
        styles={{ body: { display: 'flex', flexDirection: 'column', minHeight: 0, flex: 1 } }}
      >
        <div style={{ height: 64, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 8 }}>
          <img src="/app-icon-512.png" alt="KeyRouter" style={{ width: 28, height: 28 }} />
          {!collapsed && (
            <Title level={4} style={{ color: '#fff', margin: 0 }}>
              KeyRouter
            </Title>
          )}
        </div>
        <div style={{ flex: 1, minHeight: 0, overflow: 'auto' }}>
          <Menu
            theme="dark"
            mode="inline"
            selectedKeys={[location.pathname]}
            items={menuItems}
          />
        </div>
        {/* Version badge + listen address, above the collapse trigger. The
            badge carries the update button when the auto-check found a newer
            version. Collapsed, the button shows icon only. */}
        <div style={{ borderTop: '1px solid rgba(255, 255, 255, 0.08)', padding: '8px 12px 0' }}>
          {updateInfo?.update_available ? (
            <Button
              type="primary"
              size="small"
              block
              loading={applying}
              icon={<DownloadOutlined />}
              onClick={handleApplyUpdate}
              aria-label={`Update to v${updateInfo.latest_version}`}
              title={updateInfo.install_mode === 'installed'
                ? `Download installer and update to v${updateInfo.latest_version}`
                : `Download and update to v${updateInfo.latest_version}`}
              style={{ marginBottom: 8 }}
            >
              {collapsed ? '' : `Update v${updateInfo.latest_version}`}
            </Button>
          ) : (
            version && (
              <div
                title={`KeyRouter v${version}`}
                style={{
                  textAlign: 'center',
                  fontSize: 12,
                  lineHeight: '16px',
                  color: 'rgba(255, 255, 255, 0.45)',
                  whiteSpace: 'nowrap',
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  marginBottom: 8,
                }}
              >
                v{version}
              </div>
            )
          )}
          <div
            title={serverUrl}
            style={{
              textAlign: 'center',
              fontSize: 12,
              lineHeight: '16px',
              color: 'rgba(255, 255, 255, 0.65)',
              whiteSpace: 'nowrap',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
              paddingBottom: 8,
            }}
          >
            {collapsed ? `:${port}` : serverUrl}
          </div>
        </div>
      </Sider>
      <Layout style={{ marginLeft: collapsed ? 80 : 200, minHeight: '100vh' }}>
        <Content style={{ margin: 24 }}>
          {updateInfo && (
            <Alert
              type="info"
              showIcon
              style={{ marginBottom: 16 }}
              message={`KeyRouter ${updateInfo.latest_version} is available`}
              description={
                <Space>
                  <span>You are running {updateInfo.current_version}. Update is not applied automatically.</span>
                  <Button size="small" type="primary" icon={<DownloadOutlined />} onClick={() => navigate('/settings')}>
                    Go to Settings to update
                  </Button>
                </Space>
              }
            />
          )}
          <Routes>
            <Route path="/" element={<ErrorBoundary><Activity /></ErrorBoundary>} />
            {/* /stats was the Activity page's URL before it became the home page */}
            <Route path="/stats" element={<Navigate to="/" replace />} />
            <Route path="/providers" element={<ErrorBoundary><Providers /></ErrorBoundary>} />
            <Route path="/models" element={<ErrorBoundary><Models /></ErrorBoundary>} />
            <Route path="/settings" element={<ErrorBoundary><Settings /></ErrorBoundary>} />
            <Route path="/help" element={<ErrorBoundary><Help /></ErrorBoundary>} />
          </Routes>
        </Content>
      </Layout>
    </Layout>
  );
};

const App: React.FC = () => {
  return (
    <ThemeProvider>
      <ThemedApp />
    </ThemeProvider>
  );
};

// ThemedApp sits inside ThemeProvider so it can read the resolved theme;
// ConfigProvider hands every antd component the matching algorithm, and the
// body/document get painted to match (antd tokens only cover its own
// components, not the area behind them or native scrollbars).
const ThemedApp: React.FC = () => {
  const { isDark } = useThemeMode();
  const algorithm = isDark ? theme.darkAlgorithm : theme.defaultAlgorithm;
  // getDesignToken computes tokens directly from the algorithm. useToken()
  // here would resolve against the nearest ConfigProvider ABOVE this
  // component — there is none (ThemeProvider renders no provider) — so it
  // would always return the default light tokens.
  const { colorBgLayout } = theme.getDesignToken({ algorithm });

  // useLayoutEffect: paint the body before first paint so a dark-mode start
  // never flashes white (the stored mode is already known synchronously).
  useLayoutEffect(() => {
    document.documentElement.style.colorScheme = isDark ? 'dark' : 'light';
    document.body.style.backgroundColor = colorBgLayout;
  }, [isDark, colorBgLayout]);

  return (
    <ConfigProvider theme={{ algorithm }}>
      <BrowserRouter>
        <LayoutWithRouter />
      </BrowserRouter>
    </ConfigProvider>
  );
};

export default App;
