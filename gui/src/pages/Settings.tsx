import { useState, useEffect } from "react";
import { Save, RotateCcw, AlertCircle, CheckCircle2, Settings as SettingsIcon } from "lucide-react";
import { ipc } from "../ipc";

export function Settings() {
  const [logRotateMaxAge, setLogRotateMaxAge] = useState("");
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    ipc.getSettings()
      .then((v) => { setLogRotateMaxAge(v ?? ""); })
      .catch(() => {})
      .finally(() => setLoading(false));
  }, []);

  async function save() {
    setError(null);
    try {
      await ipc.setSettings(logRotateMaxAge.trim() || null);
      setSaved(true);
      setTimeout(() => setSaved(false), 2500);
    } catch (e) {
      setError(String(e));
    }
  }

  return (
    <div style={styles.root}>
      <div style={styles.pageHeader}>
        <h1 style={styles.title}>Settings</h1>
        <p style={styles.subtitle}>Configure daemon behaviour</p>
      </div>

      <div style={styles.section}>
        <div style={styles.sectionHeader}>
          <SettingsIcon size={15} strokeWidth={1.5} style={{ color: "var(--primary)" }} />
          <span style={styles.sectionTitle}>Sync Logs</span>
        </div>
        <div style={styles.field}>
          <label style={styles.label}>Log Rotation Max Age</label>
          <p style={styles.hint}>
            Duration string for how long per-folder log entries are kept (e.g.{" "}
            <code style={styles.code}>168h</code>, <code style={styles.code}>720h</code>). Leave blank to disable rotation.
          </p>
          <input
            style={styles.input}
            placeholder="e.g. 720h"
            value={logRotateMaxAge}
            onChange={(e) => setLogRotateMaxAge(e.target.value)}
            disabled={loading}
          />
        </div>

        {error && (
          <div style={styles.errorBox}>
            <AlertCircle size={14} strokeWidth={1.5} />
            {error}
          </div>
        )}

        {saved && (
          <div style={styles.successBox}>
            <CheckCircle2 size={14} strokeWidth={1.5} />
            Settings saved.
          </div>
        )}

        <div style={styles.actions}>
          <button className="btn-ghost" style={{ gap: "0.375rem", display: "flex", alignItems: "center" }} onClick={() => { setLogRotateMaxAge(""); setError(null); }}>
            <RotateCcw size={13} strokeWidth={1.5} /> Reset
          </button>
          <button className="btn-primary" onClick={save} disabled={loading}>
            <Save size={13} strokeWidth={1.5} /> Save
          </button>
        </div>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    flex: 1,
    display: "flex",
    flexDirection: "column",
    padding: "1.5rem",
    gap: "1.25rem",
    overflow: "hidden auto",
  },
  pageHeader: { marginBottom: "0.25rem" },
  title: {
    fontSize: "1.375rem",
    fontWeight: 300,
    letterSpacing: "-0.02em",
    color: "var(--on-surface)",
    marginBottom: "0.25rem",
  },
  subtitle: { fontSize: "0.8125rem", color: "var(--on-surface-variant)" },
  section: {
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-xl)",
    padding: "1.25rem",
    display: "flex",
    flexDirection: "column",
    gap: "1rem",
    maxWidth: 560,
  },
  sectionHeader: {
    display: "flex",
    alignItems: "center",
    gap: "0.5rem",
    marginBottom: "0.25rem",
  },
  sectionTitle: {
    fontSize: "0.875rem",
    fontWeight: 600,
    color: "var(--on-surface)",
  },
  field: { display: "flex", flexDirection: "column", gap: "0.375rem" },
  label: { fontSize: "0.8125rem", fontWeight: 500, color: "var(--on-surface-variant)" },
  hint: { fontSize: "0.75rem", color: "var(--outline)", lineHeight: 1.5 },
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
  code: {
    background: "var(--surface-container-highest)",
    color: "var(--primary)",
    padding: "0.0625rem 0.3125rem",
    borderRadius: "var(--radius-sm)",
    fontSize: "0.75rem",
    fontFamily: "monospace",
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
  successBox: {
    display: "flex",
    alignItems: "center",
    gap: "0.5rem",
    background: "rgba(107,217,160,0.1)",
    borderRadius: "var(--radius-md)",
    padding: "0.5rem 0.75rem",
    fontSize: "0.8125rem",
    color: "var(--success)",
  },
  actions: {
    display: "flex",
    gap: "0.5rem",
    justifyContent: "flex-end",
    marginTop: "0.25rem",
  },
};
