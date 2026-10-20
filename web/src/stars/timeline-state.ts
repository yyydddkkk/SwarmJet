import { useSyncExternalStore } from "react";
// A presentation-only clock. It never writes a ProjectStore or invents events.
const states = new WeakMap<object, ReturnType<typeof createTimeline>>();
function createTimeline() {
  let state = { fraction: 1, playing: false, anchor: Date.now() };
  const listeners = new Set<() => void>();
  return {
    getSnapshot: () => state,
    subscribe(fn: () => void) {
      listeners.add(fn);
      return () => {
        listeners.delete(fn);
      };
    },
    set(fraction: number, playing = false) {
      state = {
        ...state,
        fraction: Math.max(0, Math.min(1, fraction)),
        playing,
        anchor: fraction >= 1 && !playing ? Date.now() : state.anchor,
      };
      listeners.forEach((fn) => fn());
    },
    begin() {
      state = { fraction: 1, playing: false, anchor: Date.now() };
      listeners.forEach((fn) => fn());
    },
    reset() {
      this.set(1);
    },
  };
}
export function timelineFor(owner: object) {
  let clock = states.get(owner);
  if (!clock) {
    clock = createTimeline();
    states.set(owner, clock);
  }
  return clock;
}
export function useTimeline(owner: object) {
  const clock = timelineFor(owner);
  return useSyncExternalStore(
    clock.subscribe,
    clock.getSnapshot,
    clock.getSnapshot,
  );
}
