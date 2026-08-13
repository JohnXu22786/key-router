import React, { useEffect, useLayoutEffect, useState } from 'react';
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
import { ThemeProvider, useThemeMode } from './ThemeContext';
import { getAutoCheckState } from './api/client';

const { Sider, Content } = Layout;
const { Title } = Typography;

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
        style={{ position: 'fixed', left: 0, top: 0, bottom: 0, height: '100vh', overflow: 'auto', zIndex: 10 }}
      >
        <div style={{ height: 64, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 8 }}>
          <img src="/app-icon-512.png" alt="KeyRouter" style={{ width: 28, height: 28 }} />
          {!collapsed && (
            <Title level={4} style={{ color: '#fff', margin: 0 }}>
              KeyRouter
            </Title>
          )}
        </div>
        <Menu
          theme="dark"
          mode="inline"
          selectedKeys={[location.pathname]}
          items={menuItems}
        />
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
