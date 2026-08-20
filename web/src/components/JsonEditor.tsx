import React, { useRef, useCallback, useEffect } from 'react';
import { Typography, theme } from 'antd';

const { Text } = Typography;

// JsonEditor — a Sublime-style JSON editing textarea:
//   - typing " or ' auto-inserts the closing quote with the cursor in the middle
//   - typing { [ ( auto-inserts the closing bracket with the cursor inside
//   - Enter inside {} / [] / between quotes auto-indents to the right depth
//   - Backspace on an empty pair removes both characters
//   - live validation (error line shown), monospace, with line numbers
interface JsonEditorProps {
  // value/onChange are injected by antd Form.Item when used inside a form.
  value?: string;
  onChange?: (v: string) => void;
  onValid?: (valid: boolean) => void;
  rows?: number;
  placeholder?: string;
  style?: React.CSSProperties;
}

// Indentation unit (2 spaces like Sublime's default for JSON).
const INDENT = '  ';

// Auto-close pairs: opening -> closing.
const PAIRS: Record<string, string> = { '{': '}', '[': ']', '"': '"', "'": "'" };

function lineOf(text: string, pos: number): number {
  return text.slice(0, pos).split('\n').length;
}

function parseJSON(text: string): { ok: boolean; error?: string; line?: number } {
  const trimmed = text.trim();
  if (trimmed === '') return { ok: true };
  try {
    JSON.parse(trimmed);
    return { ok: true };
  } catch (e: any) {
    // Extract the position from the message if possible ("at position N").
    const m = /position (\d+)/.exec(String(e?.message ?? ''));
    const pos = m ? parseInt(m[1], 10) : null;
    return {
      ok: false,
      error: String(e?.message ?? 'Invalid JSON'),
      line: pos != null ? lineOf(text, Math.min(pos, text.length)) : undefined,
    };
  }
}

const JsonEditor: React.FC<JsonEditorProps> = ({
  value = '', onChange, onValid, rows = 12, placeholder, style,
}) => {
  const ref = useRef<HTMLTextAreaElement>(null);
  const lastValid = useRef(true);
  // Theme tokens: the editor's custom chrome (border, gutter, text) is drawn
  // by hand, so it must follow light/dark instead of hardcoding light colors.
  const { token } = theme.useToken();

  // Insert text at the cursor, replacing the selection; returns new caret pos.
  const insertAtCaret = useCallback((insert: string, caretOffset = insert.length) => {
    const el = ref.current;
    if (!el) return;
    const start = el.selectionStart;
    const end = el.selectionEnd;
    const next = value.slice(0, start) + insert + value.slice(end);
    onChange?.(next);
    // Set the caret after the DOM updates.
    requestAnimationFrame(() => {
      el.focus();
      el.setSelectionRange(start + caretOffset, start + caretOffset);
    });
  }, [value, onChange]);

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    const el = ref.current;
    if (!el) return;
    const start = el.selectionStart;
    const end = el.selectionEnd;
    const sel = value.slice(start, end);

    // --- Enter: auto-indent to the current depth ---
    if (e.key === 'Enter') {
      e.preventDefault();
      // Find the line start and its indentation.
      const lineStart = value.lastIndexOf('\n', start - 1) + 1;
      const line = value.slice(lineStart, start);
      const indentMatch = /^[ \t]*/.exec(line);
      let indent = indentMatch ? indentMatch[0] : '';
      // If the previous non-space char is an opener, indent one level deeper.
      const trimmed = line.trimEnd();
      const lastChar = trimmed[trimmed.length - 1];
      let extra = '';
      if (lastChar === '{' || lastChar === '[' || lastChar === ',') {
        extra = INDENT;
      }
      // If the char at the caret closes the current block, add a dedented
      // line below (Sublime behavior for {} / []).
      let suffix = '';
      if (lastChar === '{' || lastChar === '[') {
        const closing = PAIRS[lastChar];
        if (value.slice(start, start + 1) === closing) {
          suffix = '\n' + indent + closing;
        }
      }
      const insert = '\n' + indent + extra + suffix;
      insertAtCaret(insert, '\n'.length + indent.length + extra.length);
      return;
    }

    // --- Backspace on an empty pair: remove both ---
    if (e.key === 'Backspace' && start === end && start > 0 && start < value.length) {
      const prev = value[start - 1];
      const next = value[start];
      if (PAIRS[prev] === next) {
        e.preventDefault();
        onChange?.(value.slice(0, start - 1) + value.slice(start + 1));
        requestAnimationFrame(() => {
          el.focus();
          el.setSelectionRange(start - 1, start - 1);
        });
        return;
      }
    }

    // --- Auto-close pairs on typing openers ---
    if (e.key.length === 1 && PAIRS[e.key] && !e.ctrlKey && !e.metaKey && !e.altKey) {
      // Skip when there's a selection (replace it) — still wrap.
      e.preventDefault();
      const closing = PAIRS[e.key];
      const wrapped = sel.length > 0 ? e.key + sel + closing : e.key + closing;
      insertAtCaret(wrapped, e.key.length);
      return;
    }

    // --- Typing a closer that already exists: jump over it ---
    if (e.key.length === 1 && start === end) {
      const next = value[start];
      if (next === e.key && (e.key === '}' || e.key === ']' || e.key === '"' || e.key === "'")) {
        e.preventDefault();
        el.setSelectionRange(start + 1, start + 1);
        return;
      }
    }
  };

  // Live validation. `result` feeds both the inline error UI below and the
  // validity notification. The notification runs as an effect (never during
  // render): onValid hands the parent a setter (e.g. Models' extraError guard)
  // and React forbids updating a component while a different one renders, so
  // a render-phase call could defer/drop the update and leave that guard stale
  // relative to the value on screen. lastValid guards the effect so onValid
  // fires exactly once per valid/invalid transition.
  const result = parseJSON(value);
  useEffect(() => {
    if (result.ok !== lastValid.current) {
      lastValid.current = result.ok;
      onValid?.(result.ok);
    }
  }, [result.ok, onValid]);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      <div style={{ display: 'flex', border: `1px solid ${token.colorBorder}`, borderRadius: 6, overflow: 'hidden', background: token.colorBgContainer, ...style }}>
        {/* Line numbers gutter */}
        <div
          style={{
            padding: '4px 8px',
            borderRight: `1px solid ${token.colorSplit}`,
            background: token.colorFillAlter,
            textAlign: 'right',
            fontFamily: 'monospace',
            fontSize: 12,
            lineHeight: '20px',
            color: token.colorTextPlaceholder,
            userSelect: 'none',
            minWidth: 36,
            overflow: 'hidden',
          }}
        >
          {value.split('\n').map((_, i) => <div key={i}>{i + 1}</div>)}
        </div>
        <textarea
          ref={ref}
          value={value}
          onChange={(e) => onChange?.(e.target.value)}
          onKeyDown={handleKeyDown}
          rows={rows}
          placeholder={placeholder}
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          style={{
            flex: 1,
            border: 'none',
            outline: 'none',
            resize: 'vertical',
            padding: '4px 8px',
            fontFamily: 'monospace',
            fontSize: 12,
            lineHeight: '20px',
            color: token.colorText,
            background: 'transparent',
          }}
        />
      </div>
      {result.ok
        ? <Text type="secondary" style={{ fontSize: 12 }}>Valid JSON ✓</Text>
        : <Text type="danger" style={{ fontSize: 12 }}>
            {result.error}{result.line != null ? ` (line ${result.line})` : ''}
          </Text>}
    </div>
  );
};

export default JsonEditor;
