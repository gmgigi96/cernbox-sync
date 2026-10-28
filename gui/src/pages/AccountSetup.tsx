import { useEffect, useRef, useState } from "react";
import { AlertCircle, ArrowRight, Copy, ExternalLink, Check } from "lucide-react";
import { openUrl } from "@tauri-apps/plugin-opener";
import { ipc } from "../ipc";

interface AccountSetupProps {
  /** CERNBox server the login flow runs against. */
  serverUrl: string;
  onDone: () => void;
  /**
   * Account to sign in with, e.g. the one used by a detected desktop client.
   * The account is chosen in the browser, so this is only shown as a hint.
   */
  suggestedUsername?: string;
  /** Rendered inside the setup wizard, which provides the window frame. */
  embedded?: boolean;
}

/** Delay between two polls; the server also holds each poll for a few seconds. */
export const POLL_INTERVAL_MS = 3_000;
/** Matches the server's default login flow lifetime (20 minutes). */
export const LOGIN_TIMEOUT_MS = 20 * 60 * 1000;

type State =
  | { step: "idle" }
  | { step: "starting" }
  | { step: "waiting"; loginUrl: string };

/**
 * Connects the account through the browser (Nextcloud Login Flow V2): the user
 * grants access on the CERNBox web page and the server hands out an app
 * password, which the backend stores in the daemon. No password is typed here.
 */
export function AccountSetup({ serverUrl, onDone, suggestedUsername, embedded }: AccountSetupProps) {
  const [state, setState] = useState<State>({ step: "idle" });
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const onDoneRef = useRef(onDone);
  onDoneRef.current = onDone;

  async function start() {
    setError(null);
    setState({ step: "starting" });
    try {
      const loginUrl = await ipc.loginFlowStart(serverUrl);
      setState({ step: "waiting", loginUrl });
      openUrl(loginUrl).catch(() => {
        setError("Could not open the browser. Open the link below manually.");
      });
    } catch (err) {
      setError(String(err));
      setState({ step: "idle" });
    }
  }

  function cancel() {
    ipc.loginFlowCancel().catch(() => {});
    setError(null);
    setState({ step: "idle" });
  }

  async function copyLink(url: string) {
    try {
      await navigator.clipboard.writeText(url);
      setCopied(true);
      setTimeout(() => setCopied(false), 2_000);
    } catch {
      /* clipboard unavailable: the link is still visible */
    }
  }

  // Poll while waiting for the user to grant access in the browser.
  const waitingFor = state.step === "waiting" ? state.loginUrl : null;
  useEffect(() => {
    if (!waitingFor) return;
    let active = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const deadline = Date.now() + LOGIN_TIMEOUT_MS;

    async function poll() {
      try {
        const loginName = await ipc.loginFlowPoll();
        if (!active) return;
        if (loginName) {
          onDoneRef.current();
          return;
        }
        if (Date.now() >= deadline) {
          ipc.loginFlowCancel().catch(() => {});
          setError("The login request expired. Please try again.");
          setState({ step: "idle" });
          return;
        }
        timer = setTimeout(poll, POLL_INTERVAL_MS);
      } catch (err) {
        if (!active) return;
        setError(String(err));
        setState({ step: "idle" });
      }
    }
    poll();

    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [waitingFor]);

  // Drop a pending flow when the page goes away.
  useEffect(() => () => { ipc.loginFlowCancel().catch(() => {}); }, []);

  return (
    <div style={embedded ? styles.embeddedRoot : styles.root}>
      <div style={styles.card}>
        {/* Logo / icon */}
        <div style={styles.iconWrap}>
          <svg width="32" height="32" viewBox="0 0 32 32" fill="none" aria-hidden="true">
            <rect width="32" height="32" rx="10" fill="rgba(180,197,255,0.12)" />
            <path
              d="M16 7C11.03 7 7 11.03 7 16s4.03 9 9 9 9-4.03 9-9-4.03-9-9-9zm0 2c3.86 0 7 3.14 7 7s-3.14 7-7 7-7-3.14-7-7 3.14-7 7-7zm-1 3v5l4 2.4-.72 1.2L13 18v-6h2z"
              fill="var(--primary)"
            />
          </svg>
        </div>

        <h1 style={styles.title}>Connect your CERN account</h1>

        {state.step !== "waiting" ? (
          <>
            <p style={styles.subtitle}>
              Sign in to CERNBox in your browser and grant access to this computer.
              <br />
              Your password is never stored by CERNBox Sync.
            </p>
            {suggestedUsername && (
              <p style={styles.subtitle}>
                Sign in as <strong style={{ color: "var(--on-surface)" }}>{suggestedUsername}</strong>.
              </p>
            )}

            {error && <ErrorBox message={error} />}

            <button
              type="button"
              className="btn-primary"
              style={styles.submitBtn}
              onClick={start}
              disabled={state.step === "starting"}
            >
              {state.step === "starting" ? "Connecting…" : "Sign in with browser"}
              {state.step !== "starting" && <ArrowRight size={15} strokeWidth={1.5} />}
            </button>
          </>
        ) : (
          <>
            <div style={styles.waitingRow}>
              <div style={styles.spinner} aria-hidden="true" />
              <span>Waiting for you to grant access in the browser…</span>
            </div>
            <p style={styles.subtitle}>
              If the browser did not open, open this link to continue:
            </p>

            <div style={styles.linkBox}>
              <span style={styles.linkText} title={state.loginUrl}>{state.loginUrl}</span>
            </div>

            {error && <ErrorBox message={error} />}

            <div style={styles.actions}>
              <button type="button" className="btn-secondary" onClick={() => openUrl(state.loginUrl).catch(() => {})}>
                <ExternalLink size={14} strokeWidth={1.5} /> Open browser
              </button>
              <button type="button" className="btn-secondary" onClick={() => copyLink(state.loginUrl)}>
                {copied ? <Check size={14} strokeWidth={1.5} /> : <Copy size={14} strokeWidth={1.5} />}
                {copied ? "Copied" : "Copy link"}
              </button>
              <button type="button" className="btn-ghost" onClick={cancel}>
                Cancel
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}

function ErrorBox({ message }: { message: string }) {
  return (
    <div style={styles.errorBox} role="alert">
      <AlertCircle size={14} strokeWidth={1.5} style={{ flexShrink: 0 }} />
      {message}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    width: "100vw",
    height: "100vh",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    background: "var(--background)",
  },
  embeddedRoot: {
    flex: 1,
    minHeight: 0,
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
  },
  card: {
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-xl)",
    padding: "2.5rem",
    width: 440,
    maxWidth: "calc(100vw - 3rem)",
    boxShadow: "var(--shadow-float)",
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    gap: "0",
  },
  iconWrap: {
    marginBottom: "1.25rem",
  },
  title: {
    fontSize: "1.25rem",
    fontWeight: 600,
    color: "var(--on-surface)",
    marginBottom: "0.5rem",
    textAlign: "center" as const,
  },
  subtitle: {
    fontSize: "0.8125rem",
    color: "var(--on-surface-variant)",
    textAlign: "center" as const,
    lineHeight: 1.6,
    marginBottom: "1.25rem",
  },
  waitingRow: {
    display: "flex",
    alignItems: "center",
    gap: "0.625rem",
    fontSize: "0.875rem",
    color: "var(--on-surface)",
    margin: "0.75rem 0 0.75rem",
  },
  spinner: {
    width: 14,
    height: 14,
    border: "1.5px solid var(--surface-container-highest)",
    borderTopColor: "var(--primary)",
    borderRadius: "50%",
    animation: "spin 0.8s linear infinite",
    flexShrink: 0,
  },
  linkBox: {
    width: "100%",
    background: "var(--surface-container-lowest)",
    border: "1px solid rgba(68,71,90,0.10)",
    borderRadius: "var(--radius-md)",
    padding: "0.5rem 0.75rem",
    marginBottom: "1rem",
  },
  linkText: {
    display: "block",
    fontSize: "0.75rem",
    fontFamily: "var(--font-mono, monospace)",
    color: "var(--on-surface-variant)",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap" as const,
  },
  actions: {
    display: "flex",
    gap: "0.5rem",
    flexWrap: "wrap" as const,
    justifyContent: "center",
  },
  errorBox: {
    display: "flex",
    alignItems: "center",
    gap: "0.5rem",
    width: "100%",
    background: "rgba(255,107,107,0.1)",
    borderRadius: "var(--radius-md)",
    padding: "0.5rem 0.75rem",
    marginBottom: "1rem",
    fontSize: "0.8125rem",
    color: "var(--error)",
  },
  submitBtn: {
    width: "100%",
    justifyContent: "center",
    gap: "0.5rem",
  },
};
