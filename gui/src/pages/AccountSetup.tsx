import { useState } from "react";
import { User, Lock, AlertCircle, ArrowRight } from "lucide-react";
import { ipc } from "../ipc";

interface AccountSetupProps {
  onDone: () => void;
}

export function AccountSetup({ onDone }: AccountSetupProps) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!username.trim()) {
      setError("Username is required.");
      return;
    }
    if (!password) {
      setError("Password is required.");
      return;
    }
    setError(null);
    setSubmitting(true);
    try {
      await ipc.setAccount(username.trim(), password);
      onDone();
    } catch (err) {
      setError(String(err));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div style={styles.root}>
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
        <p style={styles.subtitle}>
          Enter your CERN credentials to start syncing with CERNBox.
          <br />
          These will be used for all sync folders.
        </p>

        <form onSubmit={handleSubmit} style={styles.form}>
          <div style={styles.field}>
            <label style={styles.label}>
              <User size={13} strokeWidth={1.5} style={{ color: "var(--outline)" }} />
              Username
            </label>
            <input
              style={styles.input}
              placeholder="your-cern-username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoFocus
              autoComplete="username"
              disabled={submitting}
            />
          </div>

          <div style={styles.field}>
            <label style={styles.label}>
              <Lock size={13} strokeWidth={1.5} style={{ color: "var(--outline)" }} />
              Password
            </label>
            <input
              style={styles.input}
              type="password"
              placeholder="••••••••"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="current-password"
              disabled={submitting}
            />
          </div>

          {error && (
            <div style={styles.errorBox}>
              <AlertCircle size={14} strokeWidth={1.5} style={{ flexShrink: 0 }} />
              {error}
            </div>
          )}

          <button
            type="submit"
            className="btn-primary"
            style={styles.submitBtn}
            disabled={submitting}
          >
            {submitting ? "Connecting…" : "Get Started"}
            {!submitting && <ArrowRight size={15} strokeWidth={1.5} />}
          </button>
        </form>

        <p style={styles.note}>
          Basic auth — SSO support coming soon.
        </p>
      </div>
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
  card: {
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-xl)",
    padding: "2.5rem",
    width: 400,
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
    marginBottom: "1.75rem",
  },
  form: {
    width: "100%",
    display: "flex",
    flexDirection: "column",
    gap: "1rem",
  },
  field: {
    display: "flex",
    flexDirection: "column",
    gap: "0.375rem",
  },
  label: {
    fontSize: "0.8125rem",
    fontWeight: 500,
    color: "var(--on-surface-variant)",
    display: "flex",
    alignItems: "center",
    gap: "0.375rem",
  },
  input: {
    background: "var(--surface-container-lowest)",
    color: "var(--on-surface)",
    border: "1px solid rgba(68,71,90,0.10)",
    borderRadius: "var(--radius-md)",
    padding: "0.5rem 0.75rem",
    fontSize: "0.875rem",
    outline: "none",
    fontFamily: "var(--font-family)",
    width: "100%",
  },
  errorBox: {
    display: "flex",
    alignItems: "center",
    gap: "0.5rem",
    background: "rgba(255,107,107,0.1)",
    borderRadius: "var(--radius-md)",
    padding: "0.5rem 0.75rem",
    fontSize: "0.8125rem",
    color: "var(--error)",
  },
  submitBtn: {
    width: "100%",
    justifyContent: "center",
    gap: "0.5rem",
    marginTop: "0.25rem",
  },
  note: {
    marginTop: "1.25rem",
    fontSize: "0.75rem",
    color: "var(--outline)",
    textAlign: "center" as const,
  },
};
