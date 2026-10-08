import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { render, screen, act, fireEvent } from "@testing-library/react";
import { invoke } from "@tauri-apps/api/core";
import { openUrl } from "@tauri-apps/plugin-opener";
import { AccountSetup, POLL_INTERVAL_MS, LOGIN_TIMEOUT_MS } from "../pages/AccountSetup";

vi.mock("@tauri-apps/plugin-opener", () => ({
  openUrl: vi.fn(),
}));

const SERVER = "https://cernbox.example.org";
const LOGIN_URL = `${SERVER}/index.php/login/v2/flow/login-token`;

/** Routes invoke() calls; `polls` are the successive login_flow_poll results. */
function mockBackend(polls: Array<string | null | Error>) {
  vi.mocked(invoke).mockImplementation(async (cmd: string) => {
    switch (cmd) {
      case "login_flow_start":
        return LOGIN_URL;
      case "login_flow_poll": {
        const next = polls.length > 1 ? polls.shift() : polls[0];
        if (next instanceof Error) throw next;
        return next;
      }
      case "login_flow_cancel":
        return undefined;
      default:
        throw new Error(`unexpected command ${cmd}`);
    }
  });
}

function invokedCommands(): string[] {
  return vi.mocked(invoke).mock.calls.map((c) => c[0] as string);
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.mocked(invoke).mockReset();
  vi.mocked(openUrl).mockReset();
  vi.mocked(openUrl).mockResolvedValue(undefined);
});

afterEach(() => {
  vi.useRealTimers();
});

async function signIn() {
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: /Sign in with browser/i }));
  });
}

describe("AccountSetup", () => {
  it("starts the login flow and opens the login URL in the browser", async () => {
    mockBackend([null]);
    render(<AccountSetup serverUrl={SERVER} onDone={vi.fn()} />);

    await signIn();

    expect(invoke).toHaveBeenCalledWith("login_flow_start", { serverUrl: SERVER });
    expect(openUrl).toHaveBeenCalledWith(LOGIN_URL);
    expect(screen.getByText(/Waiting for you to grant access/i)).toBeInTheDocument();
    expect(screen.getByText(LOGIN_URL)).toBeInTheDocument();
  });

  it("polls until access is granted, then calls onDone", async () => {
    mockBackend([null, null, "einstein"]);
    const onDone = vi.fn();
    render(<AccountSetup serverUrl={SERVER} onDone={onDone} />);

    await signIn();
    expect(onDone).not.toHaveBeenCalled();

    await act(async () => { await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS); });
    expect(onDone).not.toHaveBeenCalled();

    await act(async () => { await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS); });
    expect(onDone).toHaveBeenCalledTimes(1);
    expect(invokedCommands().filter((c) => c === "login_flow_poll")).toHaveLength(3);
  });

  it("shows the error and returns to the start screen when starting fails", async () => {
    vi.mocked(invoke).mockRejectedValue("The server does not support browser login.");
    render(<AccountSetup serverUrl={SERVER} onDone={vi.fn()} />);

    await signIn();

    expect(screen.getByRole("alert")).toHaveTextContent("does not support browser login");
    expect(screen.getByRole("button", { name: /Sign in with browser/i })).toBeEnabled();
  });

  it("shows the error when polling fails", async () => {
    mockBackend([new Error("Cannot reach the server")]);
    render(<AccountSetup serverUrl={SERVER} onDone={vi.fn()} />);

    await signIn();

    expect(screen.getByRole("alert")).toHaveTextContent("Cannot reach the server");
    expect(screen.getByRole("button", { name: /Sign in with browser/i })).toBeInTheDocument();
  });

  it("cancel discards the flow and stops polling", async () => {
    mockBackend([null]);
    render(<AccountSetup serverUrl={SERVER} onDone={vi.fn()} />);

    await signIn();
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: /Cancel/i }));
    });

    expect(invokedCommands()).toContain("login_flow_cancel");
    const polls = invokedCommands().filter((c) => c === "login_flow_poll").length;
    await act(async () => { await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS * 3); });
    expect(invokedCommands().filter((c) => c === "login_flow_poll")).toHaveLength(polls);
    expect(screen.getByRole("button", { name: /Sign in with browser/i })).toBeInTheDocument();
  });

  it("gives up when access is not granted in time", async () => {
    mockBackend([null]);
    render(<AccountSetup serverUrl={SERVER} onDone={vi.fn()} />);

    await signIn();
    await act(async () => { await vi.advanceTimersByTimeAsync(LOGIN_TIMEOUT_MS + POLL_INTERVAL_MS); });

    expect(screen.getByRole("alert")).toHaveTextContent(/expired/i);
    expect(invokedCommands()).toContain("login_flow_cancel");
  });
});
