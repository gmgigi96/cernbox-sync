import "@testing-library/jest-dom";

// Mock @tauri-apps/api/core so tests don't need a Tauri runtime
vi.mock("@tauri-apps/api/core", () => ({
  invoke: vi.fn(),
}));
