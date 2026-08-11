import React from 'react';
import { Card, Typography, Button, Space } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';

const { Text } = Typography;

interface Props {
  children: React.ReactNode;
  // Optional: fallback UI to show instead of the generic error card.
  fallback?: React.ReactNode;
}
interface State {
  error: Error | null;
  info: React.ErrorInfo | null;
}

// ErrorBoundary wraps route pages so a rendering error in one page never
// blanks the whole app: the sidebar/navigation stay alive and the user can
// switch pages. The error is surfaced in-place with a retry button.
class ErrorBoundary extends React.Component<Props, State> {
  state: State = { error: null, info: null };

  static getDerivedStateFromError(error: Error): Partial<State> {
    return { error };
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    this.setState({ info });
    // eslint-disable-next-line no-console
    console.error('[ErrorBoundary]', error, info);
  }

  handleRetry = () => {
    this.setState({ error: null, info: null });
  };

  render() {
    if (this.state.error) {
      if (this.props.fallback) return this.props.fallback;
      return (
        <Card style={{ margin: 24, borderRadius: 12 }}>
          <Space direction="vertical">
            <Typography.Title level={4}>Something went wrong on this page</Typography.Title>
            <Text type="danger">
              {this.state.error.message || String(this.state.error)}
            </Text>
            {this.state.info?.componentStack && (
              <pre
                style={{
                  fontSize: 11,
                  maxHeight: 200,
                  overflow: 'auto',
                  background: '#fafafa',
                  padding: 8,
                  borderRadius: 6,
                }}
              >
                {this.state.info.componentStack.split('\n').slice(0, 12).join('\n')}
              </pre>
            )}
            <Space>
              <Button icon={<ReloadOutlined />} onClick={this.handleRetry}>Retry</Button>
            </Space>
          </Space>
        </Card>
      );
    }
    return this.props.children;
  }
}

export default ErrorBoundary;
