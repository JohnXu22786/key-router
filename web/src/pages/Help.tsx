import React, { useEffect, useLayoutEffect, useRef } from 'react';
import { Card, Typography, Descriptions, Tag, Space } from 'antd';
import { ApiOutlined, KeyOutlined, BranchesOutlined, DollarOutlined } from '@ant-design/icons';

const { Title, Paragraph, Text, Link } = Typography;

// Section anchors and their card titles, defined once: the Contents list is
// rendered from this array and the cards below look their titles (and jump
// ids) up here, so the clickable names always match the block titles and a
// renamed section id fails to compile everywhere it is referenced.
const SECTIONS = [
  { id: 'about', title: 'About KeyRouter' },
  { id: 'getting-started', title: 'Getting Started' },
  { id: 'key-concepts', title: 'Key Concepts' },
  { id: 'format-conversion', title: 'Format Conversion' },
  { id: 'recovery-strategies', title: 'Recovery Strategies' },
] as const;

type Section = (typeof SECTIONS)[number];
// Indexing by a section id yields THAT section (Extract keeps the mapping),
// so a renamed or removed id fails to compile at every lookup site.
const SECTION_BY_ID = Object.fromEntries(SECTIONS.map(s => [s.id, s])) as {
  [K in Section['id']]: Extract<Section, { id: K }>;
};

// Help keeps its scroll position while the user visits other pages: the SPA
// never reloads, so this module-level value survives the component's
// unmount/remount across route switches.
let lastScrollY = 0;

const Help: React.FC = () => {
  // Track the position continuously while Help is mounted. Saving in the
  // unmount cleanup would read the value AFTER the next page's DOM replaced
  // this one — already clamped to the next page's height, which is not
  // where the user was. A scroll listener sees Help's own document; events
  // dispatched after the swap (clamped reflows on the new page) are ignored
  // because the ref is already detached.
  const rootRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const onScroll = () => {
      if (!rootRef.current) return;
      lastScrollY = window.scrollY;
    };
    window.addEventListener('scroll', onScroll, { passive: true });
    return () => {
      window.removeEventListener('scroll', onScroll);
      // Cancel any in-flight smooth Contents jump — an instant no-op scroll
      // stops the animation, so it doesn't keep running on the next page.
      window.scrollTo(0, window.scrollY);
    };
  }, []);

  // Land exactly where we left off on return — before paint, so there is no
  // flash of the wrong offset (the first visit restores to the top).
  useLayoutEffect(() => {
    window.scrollTo(0, lastScrollY);
  }, []);

  const jumpTo = (id: Section['id']) => {
    const el = document.getElementById(id);
    if (!el) return;
    const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    el.scrollIntoView({ behavior: reduceMotion ? 'auto' : 'smooth', block: 'start' });
    // Move focus to the target (like native anchor navigation) so keyboard
    // users continue from the section instead of the Contents link.
    el.focus({ preventScroll: true });
  };

  return (
    <div ref={rootRef} style={{ maxWidth: 800 }}>
      <Title level={3}>Help</Title>

      <Card title="Contents (Page Navigator)" style={{ marginBottom: 16 }}>
        <Space direction="vertical" size={4}>
          {SECTIONS.map(s => (
            <Link
              key={s.id}
              href={`#${s.id}`}
              onClick={e => { e.preventDefault(); jumpTo(s.id); }}
            >
              {s.title}
            </Link>
          ))}
        </Space>
      </Card>

      <Card id={SECTION_BY_ID['about'].id} title={SECTION_BY_ID['about'].title} tabIndex={-1} style={{ marginBottom: 16, scrollMarginTop: 16 }}>
        <Paragraph>
          KeyRouter is a local API aggregation gateway that sits between your AI client apps
          (Chatbox, LobeChat, Cherry Studio, etc.) and upstream AI providers (OpenAI, Anthropic, DeepSeek, etc.).
        </Paragraph>
        <Paragraph>
          It automatically manages multiple API keys, handles rate limits, performs failover,
          tracks token consumption, and converts between API formats.
        </Paragraph>
      </Card>

      <Card id={SECTION_BY_ID['getting-started'].id} title={SECTION_BY_ID['getting-started'].title} tabIndex={-1} style={{ marginBottom: 16, scrollMarginTop: 16 }}>
        <ol>
          <li><strong>Add a Provider</strong> — Configure your upstream AI service (type, base URL)</li>
          <li><strong>Add API Keys</strong> — Add your API keys to the provider</li>
          <li><strong>Create Model Groups</strong> — Define model names that your clients will use</li>
          <li><strong>Create Routes</strong> — Map model groups to providers with specific model names</li>
          <li><strong>Configure Pricing</strong> — Set token costs for billing (optional)</li>
          <li><strong>Connect Your Client</strong> — Point your AI client to <Text code>http://localhost:{window.location.port || 9998}/v1</Text></li>
        </ol>
      </Card>

      <Card id={SECTION_BY_ID['key-concepts'].id} title={SECTION_BY_ID['key-concepts'].title} tabIndex={-1} style={{ marginBottom: 16, scrollMarginTop: 16 }}>
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
              <Tag color="blue">lazy</Tag> keys are only used when no immediate keys are available
              at the same priority. Rate limits are tracked across 6 sliding windows (RPM, TPM, 5-hour, daily, weekly, monthly).
              A key can also carry a one-time lifetime spend budget: once the accumulated cost reaches
              the budget, the key is disabled (<Tag>spend_limit_exhausted</Tag>) until an admin resets it.
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
              Optional per-model pricing (per 1M tokens) for tracking token costs — prompt, completion,
              cache read, and cache write rates. A <Text code>*</Text> wildcard entry covers any model
              without an exact match, and individual routes can override the table with their own rates.
              Consumption records are written per key per hour for billing and analytics.
            </Paragraph>
          </div>
        </Space>
      </Card>

      <Card id={SECTION_BY_ID['format-conversion'].id} title={SECTION_BY_ID['format-conversion'].title} tabIndex={-1} style={{ marginBottom: 16, scrollMarginTop: 16 }}>
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
          <Descriptions.Item label="Images / Files">Images and files convert between all three formats; audio and video only between OpenAI chat completions and Anthropic</Descriptions.Item>
        </Descriptions>
      </Card>

      <Card id={SECTION_BY_ID['recovery-strategies'].id} title={SECTION_BY_ID['recovery-strategies'].title} tabIndex={-1} style={{ scrollMarginTop: 16 }}>
        <Paragraph>
          When a key recovers from being disabled or rate-limited, it can behave in two ways:
        </Paragraph>
        <Descriptions column={1} size="small" bordered>
          <Descriptions.Item label={<Tag color="green">immediate</Tag>}>
            The key immediately rejoins the selection pool and may be used on the next request.
          </Descriptions.Item>
          <Descriptions.Item label={<Tag color="orange">lazy</Tag>}>
            The key is marked as healthy but only used when no immediate-recovery
            keys remain at its priority level. This is useful for backup/spare keys.
          </Descriptions.Item>
        </Descriptions>
      </Card>
    </div>
  );
};

export default Help;
