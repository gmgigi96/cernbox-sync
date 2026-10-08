import "@testing-library/jest-dom";

// Mock @tauri-apps/api/core so tests don't need a Tauri runtime
vi.mock("@tauri-apps/api/core", () => ({
  invoke: vi.fn(),
}));

// jsdom doesn't implement ResizeObserver; provide a no-op stub.
global.ResizeObserver = class {
  observe() {}
  unobserve() {}
  disconnect() {}
};

// Node >= 25 ships an experimental global localStorage that shadows jsdom's
// and is undefined unless --localstorage-file is passed. Provide an in-memory
// Storage so code persisting UI preferences works under test.
if (typeof globalThis.localStorage?.getItem !== "function") {
  const data = new Map<string, string>();
  const storage: Storage = {
    get length() { return data.size; },
    clear: () => data.clear(),
    getItem: (k) => data.get(k) ?? null,
    key: (i) => [...data.keys()][i] ?? null,
    removeItem: (k) => { data.delete(k); },
    setItem: (k, v) => { data.set(k, String(v)); },
  };
  Object.defineProperty(globalThis, "localStorage", { value: storage, configurable: true });
}
