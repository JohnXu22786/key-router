import React, { useState } from 'react';
import { BrowserRouter, Routes, Route, Link, useLocation } from 'react-router-dom';
import {
  Layout, Menu, Typography, theme, ConfigProvider,
} from 'antd';
import {
  DashboardOutlined,
  CloudServerOutlined,
  KeyOutlined,
  AppstoreOutlined,
  BranchesOutlined,
  DollarOutlined,
  SettingOutlined,
  BarChartOutlined,
} from '@ant-design/icons';

import Dashboard from './pages/Dashboard';
import Providers from './pages/Providers';
import Keys from './pages/Keys';
import ModelGroups from './pages/ModelGroups';
import RoutesPage from './pages/RoutesPage';
import Pricing from './pages/Pricing';
import Settings from './pages/Settings';
import Stats from './pages/Stats';

const { Header, Sider, Content } = Layout;
const { Title } = Typography;

const menuItems = [
  { key: '/', icon: <DashboardOutlined />, label: <Link to="/">Dashboard</Link> },
  { key: '/providers', icon: <CloudServerOutlined />, label: <Link to="/providers">Providers</Link> },
  { key: '/keys', icon: <KeyOutlined />, label: <Link to="/keys">Keys</Link> },
  { key: '/model-groups', icon: <AppstoreOutlined />, label: <Link to="/model-groups">Model Groups</Link> },
  { key: '/routes', icon: <BranchesOutlined />, label: <Link to="/routes">Routes</Link> },
  { key: '/pricing', icon: <DollarOutlined />, label: <Link to="/pricing">Pricing</Link> },
  { key: '/settings', icon: <SettingOutlined />, label: <Link to="/settings">Settings</Link> },
  { key: '/stats', icon: <BarChartOutlined />, label: <Link to="/stats">Stats</Link> },
];

const App: React.FC = () => {
  const [collapsed, setCollapsed] = useState(false);

  return (
    <ConfigProvider theme={{ algorithm: theme.defaultAlgorithm }}>
      <BrowserRouter>
        <Layout style={{ minHeight: '100vh' }}>
          <Sider collapsible collapsed={collapsed} onCollapse={setCollapsed}>
            <div style={{ height: 64, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
              <Title level={4} style={{ color: '#fff', margin: 0 }}>
                {collapsed ? 'LR' : 'LocalRouter'}
              </Title>
            </div>
            <Menu
              theme="dark"
              mode="inline"
              selectedKeys={[useLocation().pathname]}
              items={menuItems}
            />
          </Sider>
          <Layout>
            <Content style={{ margin: 24 }}>
              <Routes>
                <Route path="/" element={<Dashboard />} />
                <Route path="/providers" element={<Providers />} />
                <Route path="/keys" element={<Keys />} />
                <Route path="/model-groups" element={<ModelGroups />} />
                <Route path="/routes" element={<RoutesPage />} />
                <Route path="/pricing" element={<Pricing />} />
                <Route path="/settings" element={<Settings />} />
                <Route path="/stats" element={<Stats />} />
              </Routes>
            </Content>
          </Layout>
        </Layout>
      </BrowserRouter>
    </ConfigProvider>
  );
};

export default App;
