import React, { useEffect, useState } from 'react';
import { BrowserRouter, Routes, Route, Link, useLocation } from 'react-router-dom';
import {
  Layout, Menu, Typography, theme, ConfigProvider, Alert, Button, Space,
} from 'antd';
import {
  DashboardOutlined,
  CloudServerOutlined,
  AppstoreOutlined,
  DollarOutlined,
  BarChartOutlined,
  SettingOutlined,
  QuestionCircleOutlined,
  DownloadOutlined,
} from '@ant-design/icons';

import Dashboard from './pages/Dashboard';
import Providers from './pages/Providers';
import Models from './pages/Models';
import Pricing from './pages/Pricing';
import Activity from './pages/Activity';
import Settings from './pages/Settings';
import Help from './pages/Help';
import { getAutoCheckState } from './api/client';

const { Sider, Content } = Layout;
const { Title } = Typography;

// LayoutWithRouter is rendered inside BrowserRouter context so useLocation() works
const LayoutWithRouter: React.FC = () => {
  const [collapsed, setCollapsed] = useState(false);
  const [updateInfo, setUpdateInfo] = useState<any>(null);
  const location = useLocation();

  // Auto-update check result: the app checks on startup + daily (backend);
  // here we surface a banner when a new version exists — no auto-apply.
  useEffect(() => {
    getAutoCheckState()
      .then(res => { if (res.data?.update_available) setUpdateInfo(res.data); })
      .catch(() => {});
  }, []);

  const menuItems = [
    { key: '/', icon: <DashboardOutlined />, label: <Link to="/">Dashboard</Link> },
    { type: 'divider' as const },
    { key: '/providers', icon: <CloudServerOutlined />, label: <Link to="/providers">Providers</Link> },
    { key: '/models', icon: <AppstoreOutlined />, label: <Link to="/models">Models</Link> },
    { key: '/pricing', icon: <DollarOutlined />, label: <Link to="/pricing">Pricing</Link> },
    { type: 'divider' as const },
    { key: '/stats', icon: <BarChartOutlined />, label: <Link to="/stats">Activity</Link> },
    { key: '/settings', icon: <SettingOutlined />, label: <Link to="/settings">Settings</Link> },
    { key: '/help', icon: <QuestionCircleOutlined />, label: <Link to="/help">Help</Link> },
  ];

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Sider collapsible collapsed={collapsed} onCollapse={setCollapsed}>
        <div style={{ height: 64, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          <Title level={4} style={{ color: '#fff', margin: 0 }}>
            {collapsed ? 'KR' : 'KeyRouter'}
          </Title>
        </div>
        <Menu
          theme="dark"
          mode="inline"
          selectedKeys={[location.pathname]}
          items={menuItems}
        />
      </Sider>
      <Layout>
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
                  <Button size="small" type="primary" icon={<DownloadOutlined />} onClick={() => window.location.hash = '#/settings'}>
                    Go to Settings to update
                  </Button>
                </Space>
              }
            />
          )}
          <Routes>
            <Route path="/" element={<Dashboard />} />
            <Route path="/providers" element={<Providers />} />
            <Route path="/models" element={<Models />} />
            <Route path="/pricing" element={<Pricing />} />
            <Route path="/settings" element={<Settings />} />
            <Route path="/stats" element={<Activity />} />
            <Route path="/help" element={<Help />} />
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
