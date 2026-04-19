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
