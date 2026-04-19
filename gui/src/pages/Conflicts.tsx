import { useState, useEffect } from "react";
import { ArrowLeft, AlertTriangle, RefreshCw, CheckCircle2 } from "lucide-react";
import { ipc } from "../ipc";
import type { ConflictEntry } from "../types";

interface ConflictsProps {
  /** When set, only show conflicts for this folder. */
  folderName?: string;
  onBack: () => void;
}

function formatRelative(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime();
  const s = Math.floor(diff / 1000);
  if (s < 60) return "just now";
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

export function Conflicts({ folderName, onBack }: ConflictsProps) {
  const [conflicts, setConflicts] = useState<ConflictEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  function load() {
    setLoading(true);
    setError(null);
    ipc.listConflicts(folderName)
      .then((c) => { setConflicts(c ?? []); setLoading(false); })
      .catch((e) => { setError(String(e)); setLoading(false); });
  }

  useEffect(() => { load(); }, [folderName]);

  const title = folderName ? `Conflicts — ${folderName}` : "All Conflicts";

  return (
    <div style={s.root}>
      {/* ── Header ── */}
      <div style={s.header}>
        <button className="btn-icon" onClick={onBack} style={{ alignSelf: "flex-start", marginTop: "0.2rem" }}>
          <ArrowLeft size={16} strokeWidth={1.5} />
        </button>

        <div style={s.titleRow}>
          <div style={s.folderIcon}>
            <AlertTriangle size={22} strokeWidth={1.5} style={{ color: "#fff" }} />
          </div>
          <div>
            <p style={s.title}>{title}</p>
            <p style={s.subtitle}>
              {loading ? "Loading…" : `${conflicts.length} unresolved conflict${conflicts.length !== 1 ? "s" : ""}`}
            </p>
          </div>
        </div>

        <div style={{ marginLeft: "auto" }}>
          <button className="btn-ghost" style={s.refreshBtn} onClick={load} title="Refresh">
            <RefreshCw size={14} strokeWidth={1.5} />
          </button>
        </div>
      </div>

      {/* ── Body ── */}
      <div style={s.body}>
        {loading && (
          <div style={s.centerMsg}>
            <div style={s.spinner} />
          </div>
        )}

        {!loading && error && (
          <div style={s.centerMsg}>
            <AlertTriangle size={18} strokeWidth={1.5} style={{ color: "var(--error)" }} />
            <span style={{ fontSize: "0.8125rem", color: "var(--error)", marginTop: "0.5rem" }}>{error}</span>
          </div>
        )}

        {!loading && !error && conflicts.length === 0 && (
          <div style={s.centerMsg}>
            <CheckCircle2 size={32} strokeWidth={1} style={{ color: "var(--success)", opacity: 0.7 }} />
            <p style={{ fontSize: "0.9375rem", fontWeight: 600, color: "var(--on-surface)", margin: "0.75rem 0 0.25rem" }}>
              No unresolved conflicts
            </p>
            <p style={{ fontSize: "0.8125rem", color: "var(--on-surface-variant)", margin: 0 }}>
              Delete the .conflict-* file once you've reviewed it.
            </p>
          </div>
        )}

        {!loading && !error && conflicts.length > 0 && (
          <div style={s.panel}>
            {/* Panel header */}
            <div style={s.panelHeader}>
              <div style={s.panelTitleRow}>
                <AlertTriangle size={13} strokeWidth={1.5} style={{ color: "var(--tertiary)" }} />
                <span style={s.panelTitle}>Conflict files</span>
                <span style={s.panelBadge}>{conflicts.length}</span>
              </div>
            </div>

            {/* Column header */}
            <div style={s.colHeader}>
              <span style={{ flex: 2 }}>Original file</span>
              {!folderName && <span style={{ flex: 1 }}>Folder</span>}
              <span style={{ flex: 3 }}>Conflict copy</span>
              <span style={{ flex: "0 0 6rem", textAlign: "right" as const }}>Detected</span>
            </div>

            {/* Rows */}
            <div style={s.rowList}>
              {conflicts.map((c, i) => (
                <div key={i} style={{ ...s.row, ...(i % 2 === 1 ? s.rowAlt : {}) }}>
                  <div style={{ flex: 2, minWidth: 0, paddingRight: "0.75rem" }}>
                    <p style={s.fileName} title={c.path}>{c.path}</p>
                  </div>
                  {!folderName && (
                    <div style={{ flex: 1, minWidth: 0, paddingRight: "0.75rem" }}>
                      <p style={s.folderName} title={c.folder}>{c.folder}</p>
                    </div>
                  )}
                  <div style={{ flex: 3, minWidth: 0, paddingRight: "0.75rem" }}>
                    <p style={s.conflictPath} title={c.conflict_path}>{c.conflict_path}</p>
                  </div>
                  <div style={{ flex: "0 0 6rem", textAlign: "right" as const }}>
                    <span style={s.timeChip}>{formatRelative(c.created_at)}</span>
                  </div>
                </div>
              ))}
            </div>

            {/* Footer hint */}
            <div style={s.panelFooter}>
              <p style={s.footerHint}>
                To resolve: delete the conflict copy to keep the server version, or replace the original file with the conflict copy to keep your local changes. The record clears automatically on the next sync.
              </p>
            </div>
          </div>
        )}
      </div>

      <style>{`@keyframes spin { to { transform: rotate(360deg); } }`}</style>
    </div>
  );
}

const s: Record<string, React.CSSProperties> = {
  root: {
    flex: 1,
    display: "flex",
    flexDirection: "column",
    padding: "1.5rem",
    gap: "1.25rem",
    overflow: "hidden auto",
  },

  // Header
  header: {
    display: "flex",
    alignItems: "center",
    gap: "0.875rem",
  },
  titleRow: {
    display: "flex",
    alignItems: "center",
    gap: "1rem",
  },
  folderIcon: {
    width: 56,
    height: 56,
    background: "linear-gradient(135deg, var(--tertiary) 0%, color-mix(in srgb, var(--tertiary) 70%, var(--error)) 100%)",
    borderRadius: "var(--radius-xl)",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    flexShrink: 0,
  },
  title: {
    fontSize: "1.5rem",
    fontWeight: 600,
    letterSpacing: "-0.02em",
    color: "var(--on-surface)",
    margin: 0,
  },
  subtitle: {
    fontSize: "0.75rem",
    color: "var(--outline)",
    marginTop: "0.2rem",
  },
  refreshBtn: {
    display: "flex",
    alignItems: "center",
    padding: "0.375rem",
  },

  // Body
  body: {
    flex: 1,
    display: "flex",
    flexDirection: "column",
    gap: "1rem",
    minHeight: 0,
  },
  centerMsg: {
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    justifyContent: "center",
    paddingTop: "4rem",
    textAlign: "center",
  },
  spinner: {
    width: 20,
    height: 20,
    border: "2px solid var(--outline-variant)",
    borderTopColor: "var(--primary)",
    borderRadius: "50%",
    animation: "spin 0.8s linear infinite",
  },

  // Panel
  panel: {
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-xl)",
    overflow: "hidden",
    display: "flex",
    flexDirection: "column",
  },
  panelHeader: {
    display: "flex",
    alignItems: "center",
    gap: "0.5rem",
    padding: "0.875rem 1rem",
    borderBottom: "1px solid rgba(68,71,90,0.15)",
  },
  panelTitleRow: {
    display: "flex",
    alignItems: "center",
    gap: "0.4rem",
  },
  panelTitle: {
    fontSize: "0.875rem",
    fontWeight: 600,
    color: "var(--on-surface)",
  },
  panelBadge: {
    fontSize: "0.6875rem",
    fontWeight: 500,
    color: "var(--outline)",
    background: "rgba(108,112,134,0.1)",
    padding: "0.125rem 0.5rem",
    borderRadius: "var(--radius-full)",
  },

  // Table
  colHeader: {
    display: "flex",
    alignItems: "center",
    padding: "0.375rem 1rem",
    fontSize: "0.5625rem",
    fontWeight: 700,
    letterSpacing: "0.07em",
    color: "var(--outline)",
    borderBottom: "1px solid rgba(68,71,90,0.1)",
    textTransform: "uppercase" as const,
  },
  rowList: {
    display: "flex",
    flexDirection: "column",
  },
  row: {
    display: "flex",
    alignItems: "center",
    padding: "0.625rem 1rem",
    borderBottom: "1px solid rgba(68,71,90,0.07)",
  },
  rowAlt: {
    background: "rgba(108,112,134,0.035)",
  },
  fileName: {
    fontSize: "0.8125rem",
    fontWeight: 500,
    color: "var(--on-surface)",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap" as const,
    margin: 0,
  },
  folderName: {
    fontSize: "0.8125rem",
    color: "var(--on-surface-variant)",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap" as const,
    margin: 0,
  },
  conflictPath: {
    fontSize: "0.6875rem",
    fontFamily: "monospace",
    color: "var(--tertiary)",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap" as const,
    margin: 0,
  },
  timeChip: {
    fontSize: "0.6875rem",
    color: "var(--outline)",
  },

  // Footer
  panelFooter: {
    padding: "0.75rem 1rem",
    borderTop: "1px solid rgba(68,71,90,0.15)",
  },
  footerHint: {
    fontSize: "0.75rem",
    color: "var(--on-surface-variant)",
    lineHeight: 1.5,
    margin: 0,
  },
};
