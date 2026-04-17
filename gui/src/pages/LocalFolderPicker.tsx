import { useState, useEffect, useCallback, useRef } from "react";
import {
  ArrowLeft,
  Folder as FolderIcon,
  FolderPlus,
  ChevronRight,
  HardDrive,
  RefreshCw,
  AlertCircle,
  Check,
  ChevronUp,
  X,
} from "lucide-react";
import { ipc } from "../ipc";
import type { LocalEntry } from "../ipc";
import type { Folder, Space } from "../types";
import type { DaemonState } from "../hooks/useDaemon";

// ── Types ──────────────────────────────────────────────────────────────────────

interface LocalFolderPickerProps {
  space: Space;
  remoteUrls: string[];
  /** When set, we are editing an existing sync pair rather than creating a new one. */
  existingFolder?: Folder;
  daemon: DaemonState;
  onBack: () => void;
  onDone: () => void;
}

// ── Helpers ────────────────────────────────────────────────────────────────────

function parentPath(path: string): string | null {
  const sep = path.includes("/") ? "/" : "\\";
  const parts = path.split(sep).filter(Boolean);
  if (parts.length === 0) return null;
  parts.pop();
  const parent = sep + parts.join(sep);
  return parent === sep ? sep : parent;
}

// ── Component ──────────────────────────────────────────────────────────────────

export function LocalFolderPicker({ space, remoteUrls, existingFolder, daemon, onBack, onDone }: LocalFolderPickerProps) {
  const [currentPath, setCurrentPath] = useState<string>("");
  const [entries, setEntries] = useState<LocalEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(existingFolder?.LocalRoot ?? null);
  const [syncName, setSyncName] = useState(existingFolder?.Name ?? space.name);
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const [newFolderActive, setNewFolderActive] = useState(false);
  const [newFolderName, setNewFolderName] = useState("");
  const [newFolderError, setNewFolderError] = useState<string | null>(null);
  const newFolderInputRef = useRef<HTMLInputElement>(null);

  const remoteBase = space.webdav_url;

  const loadDir = useCallback((path?: string) => {
    setLoading(true);
    setError(null);
    ipc.listLocalDir(path)
      .then((result) => {
        setEntries(result);
        // Resolve the actual path from the first non-".." entry, or keep the requested path
        if (path) setCurrentPath(path);
        else {
          // Home dir — derive from entries
          const first = result.find((e) => e.name !== "..");
          if (first) {
            const p = first.path;
            const sep = p.includes("/") ? "/" : "\\";
            const parts = p.split(sep).filter(Boolean);
            parts.pop();
            setCurrentPath(sep + parts.join(sep));
          }
        }
      })
      .catch((e) => setError(String(e)))
      .finally(() => setLoading(false));
  }, []);

  // When editing, open the directory that contains the existing local root
  useEffect(() => {
    if (existingFolder?.LocalRoot) {
      loadDir(parentPath(existingFolder.LocalRoot) ?? undefined);
    } else {
      loadDir();
    }
  }, [loadDir]); // eslint-disable-line react-hooks/exhaustive-deps

  function navigate(entry: LocalEntry) {
    if (!entry.is_dir) return;
    if (entry.name === "..") {
      const parent = parentPath(currentPath);
      loadDir(parent ?? undefined);
    } else {
      loadDir(entry.path);
    }
    setSelected(null);
  }

  async function handleConfirm() {
    if (!selected || !syncName.trim()) return;
    setSubmitError(null);
    setSubmitting(true);
    try {
      // Convert absolute WebDAV URLs to paths relative to the space root.
      // The daemon expects e.g. "documents" not "http://host/.../spaces/abc/documents/".
      const base = remoteBase.endsWith("/") ? remoteBase : remoteBase + "/";
      const relativeFolders = remoteUrls
        .map((url) => {
          const u = url.endsWith("/") ? url : url + "/";
          return u.startsWith(base) ? u.slice(base.length).replace(/\/$/, "") : null;
        })
        .filter((p): p is string => p !== null && p !== "");
      if (existingFolder) {
        await daemon.updateFolder({
          ...existingFolder,
          LocalRoot: selected,
          Folders: relativeFolders,
        });
      } else {
        await daemon.addFolder({
          Name: syncName.trim(),
          LocalRoot: selected,
          RemoteBase: remoteBase,
          Folders: relativeFolders,
        });
      }
      onDone();
    } catch (err) {
      setSubmitError(String(err));
    } finally {
      setSubmitting(false);
    }
  }

  function openNewFolder() {
    setNewFolderName("");
    setNewFolderError(null);
    setNewFolderActive(true);
    setTimeout(() => newFolderInputRef.current?.focus(), 50);
  }

  function cancelNewFolder() {
    setNewFolderActive(false);
    setNewFolderName("");
    setNewFolderError(null);
  }

  async function commitNewFolder() {
    const name = newFolderName.trim();
    if (!name) { setNewFolderError("Name cannot be empty"); return; }
    setNewFolderError(null);
    try {
      const newPath = await ipc.createLocalDir(currentPath, name);
      setNewFolderActive(false);
      setNewFolderName("");
      // Reload and select the newly created folder
      await new Promise<void>((resolve) => {
        setLoading(true);
        setError(null);
        ipc.listLocalDir(currentPath)
          .then((result) => { setEntries(result); setSelected(newPath); })
          .catch((e) => setError(String(e)))
          .finally(() => { setLoading(false); resolve(); });
      });
    } catch (err) {
      setNewFolderError(String(err));
    }
  }

  // ── Render ───────────────────────────────────────────────────────────────────

  return (
    <div style={s.root}>
      {/* Header */}
      <div style={s.header}>
        <button className="btn-icon" style={{ flexShrink: 0 }} onClick={onBack}>
          <ArrowLeft size={16} strokeWidth={1.5} />
        </button>
        <div style={{ flex: 1, minWidth: 0 }}>
          <h1 style={s.title}>Local Folder</h1>
          <p style={s.subtitle}>
            {existingFolder
              ? <>Update the local folder synchronized with <strong>{space.name}</strong>.</>
              : <>Choose the local folder to synchronize with <strong>{space.name}</strong>.</>}
          </p>
        </div>
        <button className="btn-icon" onClick={() => loadDir(currentPath || undefined)} title="Reload">
          <RefreshCw size={15} strokeWidth={1.5} />
        </button>
      </div>

      {/* Body: file tree + sidebar */}
      <div style={s.body}>
        {/* ── File tree ── */}
        <div style={s.treePanel}>
          {/* Current path bar */}
          <div style={s.pathBar}>
            <HardDrive size={12} strokeWidth={1.5} style={{ color: "var(--outline)", flexShrink: 0 }} />
            <span style={s.pathText}>{currentPath || "~"}</span>
            {currentPath && parentPath(currentPath) && (
              <button
                className="btn-icon"
                style={{ flexShrink: 0 }}
                onClick={() => { const p = parentPath(currentPath); loadDir(p ?? undefined); setSelected(null); }}
                title="Go up"
              >
                <ChevronUp size={13} strokeWidth={1.5} />
              </button>
            )}
            <button
              className="btn-icon"
              style={{ flexShrink: 0 }}
              onClick={openNewFolder}
              title="New folder"
              disabled={!currentPath}
            >
              <FolderPlus size={14} strokeWidth={1.5} />
            </button>
          </div>

          {/* Column headers */}
          <div style={s.colHeader}>
            <span style={{ flex: 1 }}>NAME</span>
          </div>

          <div style={s.treeScroll}>
            {loading && (
              <div style={s.center}>
                <div style={s.spinner} />
                <p style={s.loadingText}>Loading…</p>
              </div>
            )}
            {!loading && error && (
              <div style={s.center}>
                <AlertCircle size={20} strokeWidth={1.5} style={{ color: "var(--error)", marginBottom: "0.5rem" }} />
                <p style={{ color: "var(--error)", fontSize: "0.8125rem", marginBottom: "0.75rem" }}>{error}</p>
                <button className="btn-secondary" onClick={() => loadDir(currentPath || undefined)}>Retry</button>
              </div>
            )}
            {!loading && !error && entries.map((entry) => (
              <EntryRow
                key={entry.path}
                entry={entry}
                selected={selected === entry.path}
                onSelect={() => { if (entry.is_dir && entry.name !== "..") setSelected(entry.path); }}
                onNavigate={() => navigate(entry)}
              />
            ))}
            {/* Inline new-folder row */}
            {newFolderActive && (
              <div style={s.newFolderRow}>
                <FolderIcon size={14} strokeWidth={1.5} style={{ color: "var(--primary)", flexShrink: 0 }} />
                <input
                  ref={newFolderInputRef}
                  style={s.newFolderInput}
                  value={newFolderName}
                  onChange={(e) => { setNewFolderName(e.target.value); setNewFolderError(null); }}
                  placeholder="New folder name"
                  onKeyDown={(e) => {
                    if (e.key === "Enter") commitNewFolder();
                    if (e.key === "Escape") cancelNewFolder();
                  }}
                />
                <button className="btn-icon" onClick={commitNewFolder} title="Create">
                  <Check size={13} strokeWidth={2} style={{ color: "var(--primary)" }} />
                </button>
                <button className="btn-icon" onClick={cancelNewFolder} title="Cancel">
                  <X size={13} strokeWidth={1.5} />
                </button>
              </div>
            )}
            {newFolderError && (
              <div style={{ padding: "0.25rem 0.75rem", fontSize: "0.75rem", color: "var(--error)" }}>
                {newFolderError}
              </div>
            )}
          </div>
        </div>

        {/* ── Sidebar ── */}
        <div style={s.sidebar}>
          {/* Selected folder info */}
          <div style={s.sideCard}>
            <p style={s.sideCardLabel}>SELECTED FOLDER</p>
            {selected ? (
              <div style={s.summaryRow}>
                <FolderIcon size={14} strokeWidth={1.5} style={{ color: "var(--primary)", flexShrink: 0 }} />
                <p style={{ fontSize: "0.8125rem", color: "var(--on-surface)", wordBreak: "break-all" as const }}>
                  {selected}
                </p>
              </div>
            ) : (
              <p style={{ fontSize: "0.8125rem", color: "var(--outline)", fontStyle: "italic" }}>
                None selected
              </p>
            )}
          </div>

          {/* Space info */}
          <div style={s.sideCard}>
            <p style={s.sideCardLabel}>SYNCING WITH</p>
            <p style={{ fontSize: "0.8125rem", color: "var(--on-surface)", fontWeight: 500, marginBottom: "0.25rem" }}>
              {space.name}
            </p>
            <div style={{ marginTop: "0.625rem" }}>
              <span style={s.driveTypeBadge}>{space.drive_type.toUpperCase()}</span>
            </div>
          </div>

          {/* Sync name */}
          <div style={s.sideCard}>
            <p style={s.sideCardLabel}>SYNC NAME</p>
            <input
              value={syncName}
              onChange={(e) => setSyncName(e.target.value)}
              placeholder="e.g. My Project"
              style={s.nameInput}
            />
            <p style={{ fontSize: "0.6875rem", color: "var(--outline)", marginTop: "0.375rem" }}>
              Short identifier for this sync pair
            </p>
          </div>

          {submitError && (
            <div style={s.errorBox}>
              <AlertCircle size={13} strokeWidth={1.5} style={{ color: "var(--error)", flexShrink: 0 }} />
              <span style={{ fontSize: "0.75rem" }}>{submitError}</span>
            </div>
          )}

          {/* Actions */}
          <div style={s.actions}>
            <button
              className="btn-primary"
              onClick={handleConfirm}
              disabled={!selected || !syncName.trim() || submitting || !daemon.daemonOnline}
              title={
                !daemon.daemonOnline ? "Daemon is offline" :
                !selected ? "Select a folder first" :
                !syncName.trim() ? "Enter a sync name" : undefined
              }
            >
              <Check size={14} strokeWidth={2} />
              {submitting ? (existingFolder ? "Updating…" : "Adding…") : (existingFolder ? "Update Sync Folder" : "Add Sync Folder")}
            </button>
          </div>
        </div>
      </div>

      <style>{`@keyframes spin { to { transform: rotate(360deg); } }`}</style>
    </div>
  );
}

// ── EntryRow ───────────────────────────────────────────────────────────────────

interface EntryRowProps {
  entry: LocalEntry;
  selected: boolean;
  onSelect: () => void;
  onNavigate: () => void;
}

function EntryRow({ entry, selected, onSelect, onNavigate }: EntryRowProps) {
  const [hovered, setHovered] = useState(false);
  const isParent = entry.name === "..";

  return (
    <div
      style={{
        ...s.treeRow,
        background: selected
          ? "rgba(180,197,255,0.1)"
          : hovered
          ? "var(--surface-container-high)"
          : "transparent",
        borderLeft: selected ? "2px solid var(--primary)" : "2px solid transparent",
      }}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      onClick={isParent ? onNavigate : onSelect}
      onDoubleClick={onNavigate}
    >
      {/* Icon */}
      {isParent ? (
        <ChevronUp size={14} strokeWidth={1.5} style={{ color: "var(--outline)", flexShrink: 0 }} />
      ) : (
        <FolderIcon size={14} strokeWidth={1.5} style={{ color: selected ? "var(--primary)" : "var(--on-surface-variant)", flexShrink: 0 }} />
      )}

      {/* Name */}
      <span style={{ ...s.rowName, color: isParent ? "var(--outline)" : "var(--on-surface)", fontStyle: isParent ? "italic" : "normal" }}>
        {isParent ? "Parent folder" : entry.name}
      </span>

      {/* Navigate arrow */}
      {!isParent && (
        <button
          style={s.navigateBtn}
          onClick={(e) => { e.stopPropagation(); onNavigate(); }}
          title="Open folder"
        >
          <ChevronRight size={13} strokeWidth={1.5} />
        </button>
      )}
    </div>
  );
}

// ── Styles ─────────────────────────────────────────────────────────────────────

const s: Record<string, React.CSSProperties> = {
  root: {
    flex: 1,
    display: "flex",
    flexDirection: "column",
    padding: "1.5rem",
    gap: "1rem",
    overflow: "hidden",
  },
  header: {
    display: "flex",
    alignItems: "flex-start",
    gap: "0.875rem",
  },
  title: {
    fontSize: "1.375rem",
    fontWeight: 300,
    letterSpacing: "-0.02em",
    color: "var(--on-surface)",
    marginBottom: "0.2rem",
  },
  subtitle: {
    fontSize: "0.8125rem",
    color: "var(--on-surface-variant)",
  },
  body: {
    flex: 1,
    display: "flex",
    gap: "1rem",
    overflow: "hidden",
  },

  // ── Tree panel ──
  treePanel: {
    flex: 1,
    display: "flex",
    flexDirection: "column",
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-xl)",
    overflow: "hidden",
  },
  pathBar: {
    display: "flex",
    alignItems: "center",
    gap: "0.5rem",
    padding: "0.5rem 0.75rem",
    background: "var(--surface-container-highest)",
    borderBottom: "1px solid rgba(68,71,90,0.15)",
  },
  pathText: {
    flex: 1,
    fontSize: "0.75rem",
    color: "var(--on-surface-variant)",
    whiteSpace: "nowrap" as const,
    overflow: "hidden",
    textOverflow: "ellipsis",
    direction: "rtl" as const,
    textAlign: "left" as const,
  },
  colHeader: {
    display: "flex",
    alignItems: "center",
    padding: "0.5rem 0.75rem",
    fontSize: "0.625rem",
    fontWeight: 700,
    letterSpacing: "0.07em",
    color: "var(--outline)",
    borderBottom: "1px solid rgba(68,71,90,0.15)",
    gap: "0.5rem",
  },
  treeScroll: {
    flex: 1,
    overflowY: "auto" as const,
  },
  treeRow: {
    display: "flex",
    alignItems: "center",
    gap: "0.5rem",
    height: 36,
    paddingLeft: "0.75rem",
    paddingRight: "0.5rem",
    cursor: "pointer",
    transition: "background var(--transition-fast)",
  },
  rowName: {
    flex: 1,
    fontSize: "0.8125rem",
    whiteSpace: "nowrap" as const,
    overflow: "hidden",
    textOverflow: "ellipsis",
    minWidth: 0,
  },
  navigateBtn: {
    background: "transparent",
    border: "none",
    padding: "0.25rem",
    cursor: "pointer",
    display: "flex",
    alignItems: "center",
    color: "var(--outline)",
    borderRadius: "var(--radius-sm)",
    flexShrink: 0,
  },
  center: {
    height: "100%",
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    justifyContent: "center",
    padding: "2rem",
  },
  spinner: {
    width: 24,
    height: 24,
    border: "2px solid var(--surface-container-highest)",
    borderTopColor: "var(--primary)",
    borderRadius: "50%",
    animation: "spin 0.8s linear infinite",
    marginBottom: "0.5rem",
  },
  loadingText: {
    fontSize: "0.8125rem",
    color: "var(--on-surface-variant)",
  },

  // ── Sidebar ──
  sidebar: {
    width: 220,
    display: "flex",
    flexDirection: "column",
    gap: "0.75rem",
    flexShrink: 0,
  },
  sideCard: {
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-xl)",
    padding: "1rem",
  },
  sideCardLabel: {
    fontSize: "0.5625rem",
    fontWeight: 700,
    letterSpacing: "0.08em",
    color: "var(--outline)",
    marginBottom: "0.75rem",
  },
  summaryRow: {
    display: "flex",
    alignItems: "flex-start",
    gap: "0.5rem",
  },
  driveTypeBadge: {
    display: "inline-flex",
    fontSize: "0.5625rem",
    fontWeight: 700,
    letterSpacing: "0.06em",
    background: "rgba(180,197,255,0.12)",
    color: "var(--primary)",
    padding: "0.125rem 0.5rem",
    borderRadius: "var(--radius-full)",
  },
  nameInput: {
    width: "100%",
    boxSizing: "border-box" as const,
    background: "var(--surface-container-highest)",
    border: "1px solid rgba(68,71,90,0.2)",
    borderRadius: "var(--radius-md)",
    color: "var(--on-surface)",
    fontSize: "0.8125rem",
    padding: "0.4rem 0.625rem",
  },
  errorBox: {
    display: "flex",
    alignItems: "flex-start",
    gap: "0.5rem",
    background: "rgba(255,107,107,0.1)",
    borderRadius: "var(--radius-md)",
    padding: "0.625rem 0.75rem",
    color: "var(--error)",
  },
  actions: {
    display: "flex",
    flexDirection: "column",
    gap: "0.5rem",
    marginTop: "auto",
  },
  newFolderRow: {
    display: "flex",
    alignItems: "center",
    gap: "0.5rem",
    height: 36,
    paddingLeft: "0.75rem",
    paddingRight: "0.5rem",
    background: "rgba(180,197,255,0.06)",
    borderLeft: "2px solid var(--primary)",
  },
  newFolderInput: {
    flex: 1,
    background: "transparent",
    border: "none",
    borderBottom: "1px solid var(--primary)",
    borderRadius: 0,
    color: "var(--on-surface)",
    fontSize: "0.8125rem",
    padding: "0.125rem 0",
    outline: "none",
    minWidth: 0,
  },
};
