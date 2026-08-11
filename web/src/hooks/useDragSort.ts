import { useRef, useState, useCallback } from 'react';

// useDragSort implements pointer-based drag-and-drop reordering with a live
// preview: while dragging, the rows slide (transform + transition) to show
// where the dragged item will land, then the order is committed on release.
// Pointer events are used instead of HTML5 DnD because the native drag on
// antd table rows is unreliable across browsers (drop/dragover often don't
// fire), which made sorting appear to "not save".
export interface DragHandlers {
  onPointerDown: (e: React.PointerEvent, index: number) => void;
  onPointerMove: (e: React.PointerEvent) => void;
  onPointerUp: () => void;
  dragIndex: number | null;
  overIndex: number | null;
  rowStyle: (index: number) => React.CSSProperties;
  dragging: boolean;
}

export function useDragSort<T>(
  items: T[],
  canReorder: (from: number, to: number) => boolean,
  onCommit: (next: T[]) => void,
): DragHandlers {
  const dragIndex = useRef<number | null>(null);
  const overIndexRef = useRef<number | null>(null);
  const [overIndex, setOverIndex] = useState<number | null>(null);
  const [dragging, setDragging] = useState(false);
  const itemsRef = useRef<T[]>(items);
  itemsRef.current = items;
  const canReorderRef = useRef(canReorder);
  canReorderRef.current = canReorder;
  const onCommitRef = useRef(onCommit);
  onCommitRef.current = onCommit;

  // Distance from the pointer to the dragged row's top, to compute the hover
  // row from the pointer Y position.
  const pointerY = useRef(0);

  const onPointerDown = useCallback((e: React.PointerEvent, index: number) => {
    // Only start from the drag handle (prevents hijacking row clicks and
    // button interactions). The handle renders with data-drag-handle.
    const target = e.target as HTMLElement;
    if (!target.closest('[data-drag-handle]')) return;
    e.preventDefault();
    dragIndex.current = index;
    overIndexRef.current = index;
    setOverIndex(index);
    setDragging(true);
    pointerY.current = e.clientY;
    try {
      e.currentTarget.setPointerCapture(e.pointerId);
    } catch { /* not critical */ }
  }, []);

  const onPointerMove = useCallback((e: React.PointerEvent) => {
    if (dragIndex.current === null) return;
    e.preventDefault();
    const from = dragIndex.current;
    // Hit-test the row under the pointer via data-row-index attributes.
    const el = document.elementFromPoint(e.clientX, e.clientY);
    const tr = el?.closest?.('tr[data-row-index]') as HTMLElement | null;
    if (!tr) return;
    const idx = Number(tr.getAttribute('data-row-index'));
    if (Number.isNaN(idx)) return;
    if (idx !== from && !canReorderRef.current(from, idx)) return;
    if (overIndexRef.current !== idx) {
      overIndexRef.current = idx;
      setOverIndex(idx);
    }
  }, []);

  const commit = useCallback(() => {
    const from = dragIndex.current;
    const to = overIndexRef.current;
    dragIndex.current = null;
    overIndexRef.current = null;
    setDragging(false);
    setOverIndex(null);
    if (from === null || to === null || from === to || !canReorderRef.current(from, to)) return;
    const next = [...itemsRef.current];
    const [moved] = next.splice(from, 1);
    next.splice(to, 0, moved);
    onCommitRef.current(next);
  }, []);

  const onPointerUp = useCallback(() => {
    commit();
  }, [commit]);

  const rowStyle = useCallback((index: number): React.CSSProperties => {
    if (!dragging || dragIndex.current === null || overIndex === null) {
      return { transition: 'transform 0.15s ease' };
    }
    const from = dragIndex.current;
    const to = overIndex;
    if (index === from) {
      return { opacity: 0.4, transition: 'transform 0.15s ease' };
    }
    let dy = 0;
    if (from < to && index > from && index <= to) dy = -1;
    if (from > to && index >= to && index < from) dy = 1;
    return {
      transform: dy ? `translateY(${dy * 100}%)` : undefined,
      transition: 'transform 0.15s ease',
    };
  }, [dragging, overIndex]);

  return {
    onPointerDown,
    onPointerMove,
    onPointerUp,
    dragIndex: dragging ? dragIndex.current : null,
    overIndex,
    rowStyle,
    dragging,
  };
}
