import { useState } from "react";
import { X, FolderOpen, Globe, User, Lock, Plus, AlertCircle } from "lucide-react";
import type { DaemonState } from "../hooks/useDaemon";

interface AddFolderModalProps {
  onClose: () => void;
  daemon: DaemonState;
}

interface FormState {
  name: string;
  localRoot: string;
  remoteBase: string;
  username: string;
  password: string;
}

const EMPTY: FormState = { name: "", localRoot: "", remoteBase: "", username: "", password: "" };

export function AddFolderModal({ onClose, daemon }: AddFolderModalProps) {
  const [form, setForm] = useState<FormState>(EMPTY);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  function set(key: keyof FormState) {
    return (e: React.ChangeEvent<HTMLInputElement>) =>
      setForm((f) => ({ ...f, [key]: e.target.value }));
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!form.name.trim() || !form.localRoot.trim() || !form.remoteBase.trim()) {
      setError("Name, local folder, and remote URL are required.");
      return;
    }
    setError(null);
    setSubmitting(true);
    try {
      await daemon.addFolder({
        Name: form.name.trim(),
        LocalRoot: form.localRoot.trim(),
        RemoteBase: form.remoteBase.trim(),
        Username: form.username.trim(),
        Password: form.password,
      });
      onClose();
    } catch (err) {
      setError(String(err));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div style={styles.overlay} onClick={onClose}>
      <div style={styles.modal} onClick={(e) => e.stopPropagation()}>
        {/* Header */}
        <div style={styles.modalHeader}>
          <div style={styles.modalIconWrap}>
            <Plus size={18} strokeWidth={1.5} style={{ color: "var(--primary)" }} />
          </div>
          <div>
            <h2 style={styles.modalTitle}>Add Sync Folder</h2>
            <p style={styles.modalSubtitle}>Register a new local ↔ remote sync pair</p>
          </div>
          <button className="btn-icon" style={{ marginLeft: "auto" }} onClick={onClose}>
            <X size={16} strokeWidth={1.5} />
          </button>
        </div>

        <form onSubmit={handleSubmit} style={styles.form}>
          <Field
            label="Folder Name"
            hint="A short identifier, e.g. Research_2024"
            icon={<FolderOpen size={14} strokeWidth={1.5} />}
          >
            <input
              placeholder="My Project"
              value={form.name}
              onChange={set("name")}
              autoFocus
            />
          </Field>

          <Field
            label="Local Path"
            hint="Absolute path to the local directory"
            icon={<FolderOpen size={14} strokeWidth={1.5} />}
          >
            <input
              placeholder="/home/user/Documents/MyProject"
              value={form.localRoot}
              onChange={set("localRoot")}
            />
          </Field>

          <Field
            label="Remote URL"
            hint="WebDAV URL of the remote CERNBox folder"
            icon={<Globe size={14} strokeWidth={1.5} />}
          >
            <input
              placeholder="https://cernbox.cern.ch/dav/files/user/MyProject"
              value={form.remoteBase}
              onChange={set("remoteBase")}
            />
          </Field>

          <div style={styles.row}>
            <Field
              label="Username"
              hint="CERN account username"
              icon={<User size={14} strokeWidth={1.5} />}
            >
              <input
                placeholder="jdoe"
                value={form.username}
                onChange={set("username")}
                autoComplete="username"
              />
            </Field>
            <Field
              label="Password"
              hint="CERN account password"
              icon={<Lock size={14} strokeWidth={1.5} />}
            >
              <input
                type="password"
                placeholder="••••••••"
                value={form.password}
                onChange={set("password")}
                autoComplete="current-password"
              />
            </Field>
          </div>

          {error && (
            <div style={styles.errorBox}>
              <AlertCircle size={14} strokeWidth={1.5} style={{ color: "var(--error)", flexShrink: 0 }} />
              <span>{error}</span>
            </div>
          )}

          <div style={styles.actions}>
            <button type="button" className="btn-secondary" onClick={onClose} disabled={submitting}>
              Cancel
            </button>
            <button type="submit" className="btn-primary" disabled={submitting || !daemon.daemonOnline}>
              {submitting ? "Adding…" : "Add Folder"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

// ── Field wrapper ──────────────────────────────────────────────────────────────

function Field({
  label,
  hint,
  icon,
  children,
}: {
  label: string;
  hint?: string;
  icon?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div style={fieldStyles.wrap}>
      <label style={fieldStyles.label}>
        {icon && <span style={{ color: "var(--outline)", lineHeight: 0 }}>{icon}</span>}
        {label}
      </label>
      {children}
      {hint && <p style={fieldStyles.hint}>{hint}</p>}
    </div>
  );
}

const fieldStyles: Record<string, React.CSSProperties> = {
  wrap: { display: "flex", flexDirection: "column", gap: "0.375rem" },
  label: {
    fontSize: "0.8125rem",
    fontWeight: 500,
    color: "var(--on-surface-variant)",
    display: "flex",
    alignItems: "center",
    gap: "0.375rem",
  },
  hint: {
    fontSize: "0.6875rem",
    color: "var(--outline)",
    marginTop: "-0.125rem",
  },
};

// ── Modal styles ───────────────────────────────────────────────────────────────

const styles: Record<string, React.CSSProperties> = {
  overlay: {
    position: "fixed" as const,
    inset: 0,
    background: "rgba(0,0,0,0.65)",
    backdropFilter: "blur(6px)",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    zIndex: 300,
  },
  modal: {
    background: "var(--surface-container-highest)",
    borderRadius: "var(--radius-xl)",
    padding: "1.75rem",
    width: 520,
    maxWidth: "calc(100vw - 3rem)",
    boxShadow: "var(--shadow-float)",
  },
  modalHeader: {
    display: "flex",
    alignItems: "flex-start",
    gap: "0.875rem",
    marginBottom: "1.5rem",
  },
  modalIconWrap: {
    width: 40,
    height: 40,
    background: "rgba(180,197,255,0.1)",
    borderRadius: "var(--radius-lg)",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    flexShrink: 0,
  },
  modalTitle: {
    fontSize: "1rem",
    fontWeight: 600,
    color: "var(--on-surface)",
    marginBottom: "0.1875rem",
  },
  modalSubtitle: {
    fontSize: "0.8125rem",
    color: "var(--on-surface-variant)",
  },
  form: {
    display: "flex",
    flexDirection: "column",
    gap: "1rem",
  },
  row: {
    display: "grid",
    gridTemplateColumns: "1fr 1fr",
    gap: "0.75rem",
  },
  actions: {
    display: "flex",
    justifyContent: "flex-end",
    gap: "0.625rem",
    marginTop: "0.5rem",
  },
  errorBox: {
    display: "flex",
    alignItems: "center",
    gap: "0.5rem",
    background: "rgba(255,107,107,0.1)",
    borderRadius: "var(--radius-md)",
    padding: "0.625rem 0.75rem",
    fontSize: "0.8125rem",
    color: "var(--error)",
  },
};
