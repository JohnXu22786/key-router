import React from 'react';
import { Card, Typography, Descriptions, Tag, Space, Divider } from 'antd';
import { ApiOutlined, KeyOutlined, BranchesOutlined, DollarOutlined } from '@ant-design/icons';

const { Title, Paragraph, Text } = Typography;

const Help: React.FC = () => {
  return (
    <div style={{ maxWidth: 800 }}>
      <Title level={3}>Help</Title>

      <Card title="About KeyRouter" style={{ marginBottom: 16 }}>
        <Paragraph>
          KeyRouter is a local API aggregation gateway that sits between your AI client apps
          (Chatbox, LobeChat, Cherry Studio, etc.) and upstream AI providers (OpenAI, Anthropic, DeepSeek, etc.).
        </Paragraph>
        <Paragraph>
          It automatically manages multiple API keys, handles rate limits, performs failover,
          tracks token consumption, and converts between API formats.
        </Paragraph>
      </Card>

      <Card title="Getting Started" style={{ marginBottom: 16 }}>
        <ol>
          <li><strong>Add a Provider</strong> — Configure your upstream AI service (type, base URL)</li>
          <li><strong>Add API Keys</strong> — Add your API keys to the provider</li>
          <li><strong>Create Model Groups</strong> — Define model names that your clients will use</li>
          <li><strong>Create Routes</strong> — Map model groups to providers with specific model names</li>
          <li><strong>Configure Pricing</strong> — Set token costs for billing (optional)</li>
          <li><strong>Connect Your Client</strong> — Point your AI client to <Text code>http://localhost:{window.location.port || 9998}/v1</Text></li>
        </ol>
      </Card>

      <Card title="Key Concepts" style={{ marginBottom: 16 }}>
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          <div>
            <Text strong><ApiOutlined /> Provider</Text>
            <Paragraph type="secondary">
              An upstream API service. Each provider has a type (<Tag>openai</Tag> or <Tag>anthropic</Tag>)
              that determines the request/response format. Format conversion happens automatically
              when the client and provider use different formats.
            </Paragraph>
          </div>
          <div>
            <Text strong><KeyOutlined /> Key</Text>
            <Paragraph type="secondary">
              An API key with optional rate limits. Keys can be tagged with a <Tag>recovery_strategy</Tag>:
              <Tag color="blue">immediate</Tag> keys re-enter rotation as soon as they recover;
              <Tag color="blue">lazy</Tag> keys are only used when no immediate keys are available.
              Rate limits are tracked across 6 sliding windows (RPM, TPM, 5-hour, daily, weekly, monthly).
            </Paragraph>
          </div>
          <div>
            <Text strong><BranchesOutlined /> Route</Text>
            <Paragraph type="secondary">
              A route maps a Model Group to a Provider. Routes are ordered by drag-and-drop —
              the top route is tried first. If a route's keys are all exhausted, the next route is tried.
            </Paragraph>
          </div>
          <div>
            <Text strong><DollarOutlined /> Pricing</Text>
            <Paragraph type="secondary">
              Optional per-model pricing for tracking token costs. Consumption records are written
              per key per hour for billing and analytics.
            </Paragraph>
          </div>
        </Space>
      </Card>

      <Card title="Format Conversion" style={{ marginBottom: 16 }}>
        <Paragraph>
          KeyRouter supports the OpenAI chat completions API, the OpenAI Responses API
          (<Text code>/v1/responses</Text>), and the Anthropic API format. Your client can use
          any of them, and the router will convert to the provider's format automatically.
          Gateways without a native <Text code>/v1/responses</Text> endpoint are converted to
          chat completions automatically.
        </Paragraph>
        <Descriptions column={2} size="small" bordered>
          <Descriptions.Item label="Client → Router">OpenAI, Responses API, or Anthropic format</Descriptions.Item>
          <Descriptions.Item label="Router → Provider">OpenAI (chat or Responses) or Anthropic format (per provider type)</Descriptions.Item>
          <Descriptions.Item label="Streaming">SSE chunks converted in real-time</Descriptions.Item>
          <Descriptions.Item label="Tools / Functions">Converted between formats</Descriptions.Item>
        </Descriptions>
      </Card>

      <Card title="Recovery Strategies">
        <Paragraph>
          When a key recovers from being disabled or rate-limited, it can behave in two ways:
        </Paragraph>
        <Descriptions column={1} size="small" bordered>
          <Descriptions.Item label={<Tag color="green">immediate</Tag>}>
            The key immediately rejoins the selection pool and may be used on the next request.
          </Descriptions.Item>
          <Descriptions.Item label={<Tag color="orange">lazy</Tag>}>
            The key is marked as healthy but only used when all <Tag>immediate</Tag> keys
            in the same provider are exhausted. This is useful for backup/spare keys.
          </Descriptions.Item>
        </Descriptions>
      </Card>
    </div>
  );
};

export default Help;
