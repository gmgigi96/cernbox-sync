import { useState } from "react";
import { X, FolderOpen, Plus, AlertCircle } from "lucide-react";
import type { DaemonState } from "../hooks/useDaemon";
import type { Space } from "../types";

interface LocalPathModalProps {
  space: Space;
  /** Selected remote URLs from the FolderPicker. Falls back to the space root. */
  remoteUrls: string[];
  daemon: DaemonState;
  onClose: () => void;
}

export function LocalPathModal({ space, remoteUrls, daemon, onClose }: LocalPathModalProps) {
  const [name, setName] = useState(space.name);
  const [localRoot, setLocalRoot] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  // Use the first selected URL as the remote base for now.
  // Once the daemon supports multiple remotes per folder this can be expanded.
  const remoteBase = remoteUrls[0] ?? space.webdav_url;

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim() || !localRoot.trim()) {
      setError("Folder name and local path are required.");
      return;
    }
    setError(null);
    setSubmitting(true);
    try {
      await daemon.addFolder({
        Name: name.trim(),
        LocalRoot: localRoot.trim(),
        RemoteBase: remoteBase,
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
            <h2 style={styles.modalTitle}>Sync "{space.name}"</h2>
            <p style={styles.modalSubtitle}>Choose a local folder to synchronize with this space</p>
          </div>
          <button className="btn-icon" style={{ marginLeft: "auto" }} onClick={onClose}>
            <X size={16} strokeWidth={1.5} />
          </button>
        </div>

        <form onSubmit={handleSubmit} style={styles.form}>
          {/* Folder name */}
          <div style={fieldStyles.wrap}>
            <label style={fieldStyles.label}>
              <FolderOpen size={14} strokeWidth={1.5} style={{ color: "var(--outline)" }} />
              Folder Name
            </label>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="My Project"
              autoFocus
            />
            <p style={fieldStyles.hint}>A short identifier for this sync pair</p>
          </div>

          {/* Local path */}
          <div style={fieldStyles.wrap}>
            <label style={fieldStyles.label}>
              <FolderOpen size={14} strokeWidth={1.5} style={{ color: "var(--outline)" }} />
              Local Path
            </label>
            <input
              value={localRoot}
              onChange={(e) => setLocalRoot(e.target.value)}
              placeholder="/home/user/Documents/MyProject"
            />
            <p style={fieldStyles.hint}>Absolute path to the local directory</p>
          </div>

          {/* Remote URL (read-only) */}
          <div style={fieldStyles.wrap}>
            <label style={fieldStyles.label}>Remote URL</label>
            <input value={remoteBase} readOnly style={{ color: "var(--outline)", cursor: "default" }} />
            {remoteUrls.length > 1 && (
              <p style={fieldStyles.hint}>+{remoteUrls.length - 1} more selected folder{remoteUrls.length > 2 ? "s" : ""} (will be supported in a future update)</p>
            )}
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
