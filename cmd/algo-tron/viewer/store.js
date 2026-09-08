// Tiny viewer invalidation hub. State still lives in gameState.js by design;
// this keeps render scheduling independent without adding a second state
// model during the incremental module migration.

const listeners = new Set();

export const viewerStore = {
  subscribe(listener) {
    listeners.add(listener);
    return () => listeners.delete(listener);
  },
  publish(type, payload = null) {
    const event = { type, payload };
    for (const listener of listeners) listener(event);
  },
};

// Classic scripts still need to publish events while the viewer is migrated
// incrementally to modules.
globalThis.viewerStore = viewerStore;
