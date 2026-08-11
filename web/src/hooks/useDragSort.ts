import { useRef, useState, useCallback } from 'react';

// useDragSort provides HTML5 drag-and-drop reordering with a live preview:
// while dragging, the rows slide (transform + transition) to show where the
// dragged item will land, then the order is committed on drop.
export interface DragHandlers {
  onDragStart: (e: React.DragEvent, index: number) => void;
  onDragOver: (e: React.DragEvent, index: number) => void;
  onDrop: (e: React.DragEvent) => void;
  onDragEnd: () => void;
  dragIndex: number | null;
  overIndex: number | null;
  rowStyle: (index: number) => React.CSSProperties;
}

export function useDragSort<T>(
  items: T[],
  canReorder: (from: number, to: number) => boolean,
  onCommit: (next: T[]) => void,
): DragHandlers {
  const dragIndex = useRef<number | null>(null);
  const overIndexRef = useRef<number | null>(null);
  const committedRef = useRef(false);
  const [overIndex, setOverIndex] = useState<number | null>(null);
  const [dragging, setDragging] = useState(false);
  const itemsRef = useRef<T[]>(items);
  itemsRef.current = items;

  const onDragStart = useCallback((e: React.DragEvent, index: number) => {
    dragIndex.current = index;
    overIndexRef.current = index;
    committedRef.current = false;
    setDragging(true);
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', String(index));
    requestAnimationFrame(() => setOverIndex(index));
  }, []);

  const onDragOver = useCallback((e: React.DragEvent, index: number) => {
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    const from = dragIndex.current;
    if (from === null || from === index) return;
    if (!canReorder(from, index)) return;
    overIndexRef.current = index;
    setOverIndex(index);
  }, [canReorder]);

  const commit = useCallback(() => {
    const from = dragIndex.current;
    const to = overIndexRef.current;
    dragIndex.current = null;
    overIndexRef.current = null;
    setDragging(false);
    setOverIndex(null);
    if (committedRef.current) return;
    committedRef.current = true;
    if (from === null || to === null || from === to || !canReorder(from, to)) return;
    const next = [...itemsRef.current];
    const [moved] = next.splice(from, 1);
    next.splice(to, 0, moved);
    onCommit(next);
  }, [canReorder, onCommit]);

  const onDrop = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    commit();
  }, [commit]);

  // Browsers don't always fire onDrop (esp. in antd table rows); onDragEnd
  // always fires, so commit here too (guarded by committedRef).
  const onDragEnd = useCallback(() => {
    commit();
  }, [commit]);

  const rowStyle = useCallback((index: number): React.CSSProperties => {
    if (!dragging || dragIndex.current === null || overIndex === null) {
      return { transition: 'transform 0.15s ease' };
    }
    const from = dragIndex.current;
    const to = overIndex;
    if (index === from) {
      return { opacity: 0.3, transition: 'transform 0.15s ease' };
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
    onDragStart,
    onDragOver,
    onDrop,
    onDragEnd,
    dragIndex: dragging ? dragIndex.current : null,
    overIndex,
    rowStyle,
  };
}
