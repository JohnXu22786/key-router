import { useRef, useState, useCallback, useEffect } from 'react';

// useDragSort implements pointer-based drag-and-drop reordering with a live
// preview: while dragging, the dragged row lifts out of the list (position +
// zIndex, full opacity) and follows the pointer 1:1, and the rows between it
// and the target slot slide to show where it will land. On release the row
// glides into the now-empty target slot, then the order is committed with no
// visual jump.
//
// Pointer events are used instead of HTML5 DnD because the native drag on
// antd table rows is unreliable across browsers (drop/dragover often don't
// fire), which made sorting appear to "not save".
//
// The dragged row is raised above the sliding rows — a plain dimmed row in
// flow gets painted over by the row sliding into its slot and looks
// invisible. The target index is derived from the pointer delta against the
// rows' measured original positions (not elementFromPoint), because the
// dragged row follows the pointer and would always win a coordinate
// hit-test.
//
// Contract: each drag table's rows must be a contiguous slice of `items`
// (the pages render per-group filtered slices of one API-fetched array, and
// the API orders by group first). The hook maps the dragged row's position
// within its table to a global index by offset; non-contiguous slices would
// land on another group's row and be rejected by canReorder.
export interface DragHandlers {
  onPointerDown: (e: React.PointerEvent, index: number) => void;
  onPointerMove: (e: React.PointerEvent) => void;
  onPointerUp: (e: React.PointerEvent) => void;
  onPointerCancel: (e: React.PointerEvent) => void;
  dragIndex: number | null;
  overIndex: number | null;
  rowStyle: (index: number) => React.CSSProperties;
  dragging: boolean;
  // draggingRef mirrors `dragging` synchronously (set inside the pointer
  // handlers, not on a render commit) so callers can check "am I dragging
  // right now" from event handlers and promise resolutions without waiting
  // for React to re-render.
  draggingRef: { current: boolean };
}

const SLIDE_MS = 150;

export function useDragSort<T>(
  items: T[],
  canReorder: (from: number, to: number) => boolean,
  onCommit: (next: T[]) => void,
): DragHandlers {
  const dragIndex = useRef<number | null>(null);
  const overIndexRef = useRef<number | null>(null);
  const [overIndex, setOverIndex] = useState<number | null>(null);
  const [dragging, setDragging] = useState(false);
  // Synchronous mirror of `dragging` (see DragHandlers.draggingRef).
  const draggingRef = useRef(false);
  // True during the drop glide: the dragged row animates into its target
  // slot before the array is spliced.
  const [settling, setSettling] = useState(false);
  // The dragged row's translateY in px while dragging / settling.
  const [dy, setDy] = useState(0);
  const itemsRef = useRef<T[]>(items);
  itemsRef.current = items;
  const canReorderRef = useRef(canReorder);
  canReorderRef.current = canReorder;
  const onCommitRef = useRef(onCommit);
  onCommitRef.current = onCommit;

  // Drag geometry, cached at pointerdown: original top offset and height of
  // every row in the dragged row's table (rows can wrap to two lines), the
  // dragged row's local index and height, and its table body's vertical
  // bounds.
  const rowTops = useRef<number[]>([]);
  const rowHeights = useRef<number[]>([]);
  const localFrom = useRef(0);
  const rowHeight = useRef(0);
  const minDy = useRef(-Infinity);
  const maxDy = useRef(Infinity);
  const startClientY = useRef(0);
  const settleTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  // Guards against a second commit from pointerup + pointercancel racing.
  const committedRef = useRef(false);
  // The pointer that owns the active drag: other pointers (multi-touch) must
  // not end it.
  const activePointer = useRef(-1);

  useEffect(() => () => {
    if (settleTimer.current) clearTimeout(settleTimer.current);
  }, []);

  const reset = useCallback(() => {
    dragIndex.current = null;
    overIndexRef.current = null;
    committedRef.current = false;
    activePointer.current = -1;
    draggingRef.current = false;
    setOverIndex(null);
    setDragging(false);
    setSettling(false);
    setDy(0);
  }, []);

  const onPointerDown = useCallback((e: React.PointerEvent, index: number) => {
    if (dragIndex.current !== null) return;
    // Only start from the drag handle (prevents hijacking row clicks and
    // button interactions). The handle renders with data-drag-handle.
    const target = e.target as HTMLElement;
    if (!target.closest('[data-drag-handle]')) return;
    e.preventDefault();
    const tr = e.currentTarget as HTMLElement;
    const tbody = tr.parentElement as HTMLElement | null;
    const rowRect = tr.getBoundingClientRect();
    const bodyRect = tbody?.getBoundingClientRect();
    // Measure the table's rows up-front so target detection and the settle
    // offset stay exact even when rows have different heights.
    const tops: number[] = [];
    const heights: number[] = [];
    let local = 0;
    if (tbody) {
      for (const row of Array.from(tbody.children)) {
        const r = row as HTMLElement;
        const rect = r.getBoundingClientRect();
        tops.push(rect.top);
        heights.push(rect.height);
        if (r === tr) local = tops.length - 1;
      }
    }
    rowTops.current = tops;
    rowHeights.current = heights;
    localFrom.current = local;
    rowHeight.current = rowRect.height;
    startClientY.current = e.clientY;
    // Clamp the row inside its own table (rows of other tables — e.g. keys
    // of another provider — are not valid targets anyway).
    minDy.current = bodyRect ? -(rowRect.top - bodyRect.top) : -Infinity;
    maxDy.current = bodyRect ? bodyRect.bottom - rowRect.bottom : Infinity;
    dragIndex.current = index;
    overIndexRef.current = index;
    activePointer.current = e.pointerId;
    draggingRef.current = true;
    setOverIndex(index);
    setDy(0);
    setSettling(false);
    setDragging(true);
    try {
      e.currentTarget.setPointerCapture(e.pointerId);
    } catch { /* not critical */ }
  }, []);

  const onPointerMove = useCallback((e: React.PointerEvent) => {
    if (dragIndex.current === null || committedRef.current) return;
    e.preventDefault();
    const from = dragIndex.current;
    // The dragged row tracks the pointer 1:1 (clamped to its table body).
    const raw = e.clientY - startClientY.current;
    const nextDy = Math.min(Math.max(raw, minDy.current), maxDy.current);
    setDy(nextDy);
    // Target slot: the dragged row's edges against the rows' measured
    // midpoints — the target advances when the dragged row overlaps the next
    // row by more than half, and retreats when it crosses back above the
    // previous row's midpoint (symmetric 50%-overlap rule, robust to rows of
    // different heights).
    const tops = rowTops.current;
    let next: number;
    if (tops.length === 0) {
      // Fallback (no measured rows): uniform-height rounding.
      next = Math.max(0, Math.min(from + Math.round(nextDy / (rowHeight.current || 1)), itemsRef.current.length - 1));
    } else {
      const h = rowHeight.current;
      const draggedTop = tops[localFrom.current] + nextDy;
      let localTo = localFrom.current;
      for (let j = 0; j < tops.length; j++) {
        if (j === localFrom.current) continue;
        if (j > localFrom.current && draggedTop + h > tops[j] + rowHeights.current[j] / 2) localTo++;
        if (j < localFrom.current && draggedTop < tops[j] + rowHeights.current[j] / 2) localTo--;
      }
      next = Math.max(0, Math.min(from + (localTo - localFrom.current), itemsRef.current.length - 1));
    }
    if (next !== overIndexRef.current && canReorderRef.current(from, next)) {
      overIndexRef.current = next;
      setOverIndex(next);
    }
  }, []);

  const commit = useCallback(() => {
    const from = dragIndex.current;
    const to = overIndexRef.current;
    if (dragIndex.current === null || committedRef.current) return;
    committedRef.current = true;
    if (from === null || to === null || from === to || !canReorderRef.current(from, to)) {
      // Aborted (e.g. released without crossing a slot): glide the row back
      // to its slot instead of snapping it.
      setSettling(true);
      setDy(0);
      settleTimer.current = setTimeout(() => { reset(); }, SLIDE_MS);
      return;
    }
    // Rows between from..to already slid out of the way, so the target slot
    // is empty: glide the dragged row into it, then splice with no jump.
    const tops = rowTops.current;
    const glideTo = tops.length > 0
      ? tops[localFrom.current + (to - from)] - tops[localFrom.current]
      : (to - from) * (rowHeight.current || 0);
    setSettling(true);
    setDy(glideTo);
    settleTimer.current = setTimeout(() => {
      const next = [...itemsRef.current];
      const [moved] = next.splice(from, 1);
      next.splice(to, 0, moved);
      onCommitRef.current(next);
      reset();
    }, SLIDE_MS);
  }, [reset]);

  const endDrag = useCallback((e: React.PointerEvent) => {
    if (e.pointerId !== activePointer.current) return;
    commit();
  }, [commit]);
  const onPointerUp = endDrag;
  const onPointerCancel = endDrag;

  // Fallback so a pointerup/pointercancel that never reaches the row (e.g.
  // capture failed and the pointer is released elsewhere) cannot leave the
  // table stuck in drag state. No-op after the row's own handler already
  // committed (committedRef / pointer id).
  useEffect(() => {
    if (!dragging) return;
    const finish = (ev: PointerEvent) => {
      if (ev.pointerId !== activePointer.current) return;
      commit();
    };
    window.addEventListener('pointerup', finish);
    window.addEventListener('pointercancel', finish);
    return () => {
      window.removeEventListener('pointerup', finish);
      window.removeEventListener('pointercancel', finish);
    };
  }, [dragging, commit]);

  const rowStyle = useCallback((index: number): React.CSSProperties => {
    if (!dragging || dragIndex.current === null || overIndex === null) {
      // No transition outside a drag: after a drop the layout shifts and the
      // transforms are removed in the same render, and the two already match
      // visually — a transition here would animate the transform removal and
      // bounce every shifted row back.
      return {};
    }
    const from = dragIndex.current;
    if (index === from) {
      const style: React.CSSProperties = {
        position: 'relative',
        zIndex: 10,
        transform: `translateY(${dy}px)`,
        boxShadow: '0 4px 12px rgba(0, 0, 0, 0.18)',
        cursor: 'grabbing',
        touchAction: 'none',
      };
      // While the pointer is down the row tracks 1:1 (no transition);
      // during the drop glide it animates into the empty target slot.
      if (settling) style.transition = `transform ${SLIDE_MS}ms ease`;
      return style;
    }
    const to = overIndex;
    // The table's rows are a contiguous slice of items starting at
    // from - localFrom; map the global index back to the measured geometry.
    const g0 = from - localFrom.current;
    const local = index - g0;
    const tops = rowTops.current;
    let shift: string | undefined;
    if (from < to && index > from && index <= to) {
      // Slide up into the slot vacated by the dragged row — by the measured
      // distance, so mixed-height rows land exactly where the commit places
      // them.
      shift = tops.length > 0 && local >= 1
        ? `translateY(${tops[local - 1] - tops[local]}px)`
        : 'translateY(-100%)';
    } else if (from > to && index >= to && index < from) {
      shift = tops.length > 0 && local + 1 < tops.length
        ? `translateY(${tops[local + 1] - tops[local]}px)`
        : 'translateY(100%)';
    }
    return {
      transform: shift,
      transition: `transform ${SLIDE_MS}ms ease`,
    };
  }, [dragging, overIndex, settling, dy]);

  return {
    onPointerDown,
    onPointerMove,
    onPointerUp,
    onPointerCancel,
    dragIndex: dragging ? dragIndex.current : null,
    overIndex,
    rowStyle,
    dragging,
    draggingRef,
  };
}
