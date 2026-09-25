// @vitest-environment jsdom
import React, { StrictMode, useState } from 'react';
import { describe, it, expect, vi, afterEach } from 'vitest';
import { act, cleanup, render, fireEvent } from '@testing-library/react';
import JsonEditor from './JsonEditor';

// Mirrors how Models.tsx / RoutesPage.tsx wire JsonEditor: onValid calls
// setState on the parent (setExtraError -> the "guard"). A regression test
// for the bug where JsonEditor invoked onValid *inside render*, which made
// the parent update during a child's render (React forbids this and, under
// React 19 + StrictMode, can defer/drop the update so the guard goes stale
// relative to the value on screen).
function Shell() {
  const [value, setValue] = useState('{}');
  const [error, setError] = useState('');
  return (
    <div>
      <JsonEditor
        value={value}
        onChange={setValue}
        onValid={(valid) => setError(valid ? '' : 'Fix the JSON in Extra Params first')}
      />
      <span data-testid="guard">{error}</span>
    </div>
  );
}

function ControlledEditor({ initialValue }: { initialValue: string }) {
  const [value, setValue] = useState(initialValue);
  return <JsonEditor value={value} onChange={setValue} />;
}

const spies: ReturnType<typeof vi.spyOn>[] = [];

afterEach(() => {
  cleanup();
  for (const s of spies) s.mockRestore();
  spies.length = 0;
  vi.unstubAllGlobals();
});

describe('JsonEditor - Enter with empty pairs', () => {
  it.each([
    { value: '{}', closer: '}' },
    { value: '[]', closer: ']' },
  ])('expands $value with one closer and a valid JSON value', ({ value, closer }) => {
    let frame: FrameRequestCallback | undefined;
    vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => {
      frame = callback;
      return 1;
    });

    const { getByRole } = render(<ControlledEditor initialValue={value} />);
    const textarea = getByRole('textbox') as HTMLTextAreaElement;
    textarea.setSelectionRange(1, 1);

    fireEvent.keyDown(textarea, { key: 'Enter', code: 'Enter' });
    const expected = `${value[0]}\n  \n${closer}`;
    expect(textarea.value).toBe(expected);

    act(() => frame?.(0));
    expect(textarea.selectionStart).toBe(expected.indexOf(`\n${closer}`));
    expect(textarea.selectionEnd).toBe(textarea.selectionStart);
    expect(textarea.value.split(closer).length - 1).toBe(1);
    expect(() => JSON.parse(textarea.value)).not.toThrow();
  });
});

describe('JsonEditor — validity notification', () => {
  it('updates the guard after commit and never performs a render-phase setState on the parent', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {});
    spies.push(spy);

    const { getByRole, getByTestId } = render(
      <StrictMode><Shell /></StrictMode>,
    );

    // Initially valid: guard clear.
    expect(getByTestId('guard').textContent).toBe('');

    // Type an invalid char -> valid -> invalid transition. After commit the
    // guard must reflect the value now on screen; the render-phase dispatch
    // this protects against is what violated React's render rules here.
    fireEvent.change(getByRole('textbox'), { target: { value: '{"a":}' } });
    expect(getByTestId('guard').textContent).toBe('Fix the JSON in Extra Params first');

    // Fixing it clears the guard again.
    fireEvent.change(getByRole('textbox'), { target: { value: '{"a": 1}' } });
    expect(getByTestId('guard').textContent).toBe('');

    // The whole interaction must never trip React's render-phase setState
    // warning (the exact symptom of the bug). React logs it as
    // console.error(template, componentName, ...), so join every call's args
    // and look for the distinctive phrase.
    const renderPhaseWarning = spy.mock.calls
      .map((args) => args.map(String).join(' '))
      .some((m) => m.includes('while rendering a different component'));
    expect(renderPhaseWarning).toBe(false);
  });

  it('fires onValid on valid -> invalid -> valid transitions, once each, and not on re-renders', () => {
    const onValid = vi.fn();
    const { rerender } = render(<JsonEditor value="{}" onValid={onValid} />);

    // Mount with a valid value: no spurious initial call.
    expect(onValid).not.toHaveBeenCalled();

    // valid -> invalid
    rerender(<JsonEditor value="{" onValid={onValid} />);
    expect(onValid).toHaveBeenCalledTimes(1);
    expect(onValid).toHaveBeenLastCalledWith(false);

    // Same invalid value re-rendered: no repeat call (guarded by lastValid).
    rerender(<JsonEditor value="{" onValid={onValid} />);
    expect(onValid).toHaveBeenCalledTimes(1);

    // invalid -> valid
    rerender(<JsonEditor value="{}" onValid={onValid} />);
    expect(onValid).toHaveBeenCalledTimes(2);
    expect(onValid).toHaveBeenLastCalledWith(true);
  });

  it('reports an initially-invalid value on mount', () => {
    const onValid = vi.fn();
    render(<JsonEditor value="{" onValid={onValid} />);
    expect(onValid).toHaveBeenCalledTimes(1);
    expect(onValid).toHaveBeenLastCalledWith(false);
  });
});
