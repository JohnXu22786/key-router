import React, { useEffect, useState } from 'react';
import { BrowserRouter, Routes, Route, Link, Navigate, useLocation, useNavigate } from 'react-router-dom';
import {
  Layout, Menu, Typography, theme, ConfigProvider, Alert, Button, Space,
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
import { getAutoCheckState } from './api/client';

const { Sider, Content } = Layout;
const { Title } = Typography;

// The desktop shell loads this page from http://localhost:<port> — surface
// that address in the sidebar footer so clients know where to point.
const port = window.location.port || '9998';
const serverUrl = `http://localhost:${port}`;

// LayoutWithRouter is rendered inside BrowserRouter context so useLocation() works
const LayoutWithRouter: React.FC = () => {
  const [collapsed, setCollapsed] = useState(false);
  const [updateInfo, setUpdateInfo] = useState<any>(null);
  const location = useLocation();
  const navigate = useNavigate();

  // Auto-update check result: the app checks on startup + daily (backend);
  // here we surface a banner when a new version exists — no auto-apply.
  useEffect(() => {
    getAutoCheckState()
      .then(res => { if (res.data?.update_available) setUpdateInfo(res.data); })
      .catch(() => {});
  }, []);

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
        {/* Listen address, above the collapse trigger. Collapsed, only the
            port fits. */}
        <div
          title={serverUrl}
          style={{
            padding: '8px 12px',
            textAlign: 'center',
            fontSize: 12,
            lineHeight: '16px',
            color: 'rgba(255, 255, 255, 0.65)',
            whiteSpace: 'nowrap',
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            borderTop: '1px solid rgba(255, 255, 255, 0.08)',
          }}
        >
          {collapsed ? `:${port}` : serverUrl}
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
    <ConfigProvider theme={{ algorithm: theme.defaultAlgorithm }}>
      <BrowserRouter>
        <LayoutWithRouter />
      </BrowserRouter>
    </ConfigProvider>
  );
};

export default App;
