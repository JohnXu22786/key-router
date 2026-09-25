// @vitest-environment jsdom
import React, { useState } from 'react';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useDragSort } from './useDragSort';

const initialItems = ['a', 'b', 'c'];

function Harness({ onCommit }: { onCommit: (items: string[]) => void }) {
  const [items, setItems] = useState(initialItems);
  const drag = useDragSort(items, () => true, (next) => {
    onCommit(next);
    setItems(next);
  });

  return (
    <>
      <table>
        <tbody data-testid="tbody">
          {items.map((item, index) => (
            <tr
              key={item}
              data-testid={`row-${item}`}
              onPointerDown={(e) => drag.onPointerDown(e, index)}
              onPointerMove={drag.onPointerMove}
              onPointerUp={drag.onPointerUp}
              onPointerCancel={drag.onPointerCancel}
            >
              <td><button data-drag-handle>{item}</button></td>
            </tr>
          ))}
        </tbody>
      </table>
      <output data-testid="over-index">{drag.overIndex ?? ''}</output>
      <output data-testid="dragging">{String(drag.dragging)}</output>
    </>
  );
}

function setRect(element: Element, top: number, height: number) {
  const rect = {
    x: 0, y: top, top, bottom: top + height, left: 0, right: 100,
    width: 100, height, toJSON: () => ({}),
  };
  Object.defineProperty(element, 'getBoundingClientRect', {
    configurable: true,
    value: () => rect,
  });
}

function setDragGeometry() {
  const rows = screen.getAllByRole('row');
  rows.forEach((row, index) => setRect(row, 100 + index * 40, 40));
  setRect(screen.getByTestId('tbody'), 100, 120);
}

function pointerEvent(type: string, pointerId: number, clientY = 0) {
  const event = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperties(event, {
    pointerId: { value: pointerId },
    clientY: { value: clientY },
  });
  return event;
}

function dragFirstRowToLastSlot() {
  setDragGeometry();
  fireEvent(screen.getByRole('button', { name: 'a' }), pointerEvent('pointerdown', 1, 110));
  fireEvent(screen.getByTestId('row-a'), pointerEvent('pointermove', 1, 190));
  expect(screen.getByTestId('over-index').textContent).toBe('2');
}

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe('useDragSort pointer cancellation', () => {
  it('cancels from the row without committing the previewed order', () => {
    vi.useFakeTimers();
    const onCommit = vi.fn();
    render(<Harness onCommit={onCommit} />);
    dragFirstRowToLastSlot();

    fireEvent(screen.getByTestId('row-a'), pointerEvent('pointercancel', 1, 190));
    act(() => { vi.advanceTimersByTime(150); });

    expect(onCommit).not.toHaveBeenCalled();
    expect(screen.getAllByRole('row').map(row => row.textContent)).toEqual(['a', 'b', 'c']);
    expect(screen.getByTestId('dragging').textContent).toBe('false');
  });

  it('cancels through the window fallback without committing the previewed order', () => {
    vi.useFakeTimers();
    const onCommit = vi.fn();
    render(<Harness onCommit={onCommit} />);
    dragFirstRowToLastSlot();

    fireEvent(window, pointerEvent('pointercancel', 1, 190));
    act(() => { vi.advanceTimersByTime(150); });

    expect(onCommit).not.toHaveBeenCalled();
    expect(screen.getAllByRole('row').map(row => row.textContent)).toEqual(['a', 'b', 'c']);
    expect(screen.getByTestId('dragging').textContent).toBe('false');
  });

  it('commits the previewed order on pointerup', () => {
    vi.useFakeTimers();
    const onCommit = vi.fn();
    render(<Harness onCommit={onCommit} />);
    dragFirstRowToLastSlot();

    fireEvent(screen.getByTestId('row-a'), pointerEvent('pointerup', 1, 190));
    expect(onCommit).not.toHaveBeenCalled();

    act(() => { vi.advanceTimersByTime(150); });

    expect(onCommit).toHaveBeenCalledOnce();
    expect(onCommit).toHaveBeenCalledWith(['b', 'c', 'a']);
    expect(screen.getAllByRole('row').map(row => row.textContent)).toEqual(['b', 'c', 'a']);
  });

  it('does not undo a pointerup commit when pointercancel follows during the glide', () => {
    vi.useFakeTimers();
    const onCommit = vi.fn();
    render(<Harness onCommit={onCommit} />);
    dragFirstRowToLastSlot();

    fireEvent(screen.getByTestId('row-a'), pointerEvent('pointerup', 1, 190));
    fireEvent(screen.getByTestId('row-a'), pointerEvent('pointercancel', 1, 190));
    act(() => { vi.advanceTimersByTime(150); });

    expect(onCommit).toHaveBeenCalledOnce();
    expect(onCommit).toHaveBeenCalledWith(['b', 'c', 'a']);
    expect(screen.getAllByRole('row').map(row => row.textContent)).toEqual(['b', 'c', 'a']);
  });
});
