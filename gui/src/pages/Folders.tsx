import { RefreshCw, Trash2, MoreVertical, FolderOpen, CheckCircle2, AlertCircle, Clock, Filter, SlidersHorizontal } from "lucide-react";
import { useState } from "react";
import type { DaemonState } from "../hooks/useDaemon";
import type { Folder } from "../types";

interface FoldersProps {
  daemon: DaemonState;
  onAddFolder: () => void;
  onOpenFolder: (folder: Folder) => void;
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

function truncatePath(p: string, max = 30): string {
  if (p.length <= max) return p;
  return "…" + p.slice(-(max - 1));
}

export function Folders({ daemon, onAddFolder, onOpenFolder }: FoldersProps) {
  const { folders, status, daemonOnline, syncFolder, removeFolder } = daemon;
  const [menuOpen, setMenuOpen] = useState<string | null>(null);
  const [confirmRemove, setConfirmRemove] = useState<string | null>(null);

  const syncingSet = new Set(status?.syncing ?? []);

  return (
    <div style={styles.root}>
      {/* Page header */}
      <div style={styles.pageHeader}>
        <div>
          <h1 style={styles.title}>Syncing Folders</h1>
          <p style={styles.subtitle}>
            Managing {folders.length} active{" "}
            {folders.length === 1 ? "directory" : "directories"}.{" "}
            {syncingSet.size > 0 && `${syncingSet.size} syncing now.`}
          </p>
        </div>
        <div style={styles.headerActions}>
          <button className="btn-ghost" style={styles.actionBtn}>
            <Filter size={14} strokeWidth={1.5} /> Filter
          </button>
          <button className="btn-ghost" style={styles.actionBtn}>
            <SlidersHorizontal size={14} strokeWidth={1.5} /> Sync Logs
          </button>
        </div>
      </div>

      {/* Folder grid */}
      {folders.length === 0 ? (
        <EmptyFolders onAdd={onAddFolder} />
      ) : (
        <div style={styles.grid}>
          {folders.map((folder) => {
            const syncing = syncingSet.has(folder.Name);
            const lastSyncTs = status?.last_sync[folder.Name];
            return (
              <FolderCard
                key={folder.Name}
                folder={folder}
                syncing={syncing}
                lastSyncTs={lastSyncTs}
                daemonOnline={daemonOnline}
                menuOpen={menuOpen === folder.Name}
                onMenuToggle={() => setMenuOpen(menuOpen === folder.Name ? null : folder.Name)}
                onMenuClose={() => setMenuOpen(null)}
                onSync={() => { setMenuOpen(null); syncFolder(folder.Name); }}
                onRemove={() => { setMenuOpen(null); setConfirmRemove(folder.Name); }}
                onViewDetails={() => onOpenFolder(folder)}
              />
            );
          })}
        </div>
      )}

      {/* Stats footer */}
      {folders.length > 0 && (
        <div style={styles.statsBar}>
          <span style={{ color: "var(--on-surface-variant)", fontSize: "0.8125rem" }}>
            <strong style={{ color: "var(--on-surface)" }}>{folders.length}</strong> total folders
          </span>
          <span style={{ color: "var(--outline)", fontSize: "0.8125rem" }}>•</span>
          <span style={{ color: "var(--on-surface-variant)", fontSize: "0.8125rem" }}>
            Status:{" "}
            <strong style={{ color: daemonOnline ? "var(--success)" : "var(--error)" }}>
              {daemonOnline ? "Optimal" : "Offline"}
            </strong>
          </span>
        </div>
      )}

      {/* Confirm remove dialog */}
      {confirmRemove && (
        <ConfirmDialog
          name={confirmRemove}
          onConfirm={() => { removeFolder(confirmRemove); setConfirmRemove(null); }}
          onCancel={() => setConfirmRemove(null)}
        />
      )}
    </div>
  );
}

// ── FolderCard ─────────────────────────────────────────────────────────────────

interface FolderCardProps {
  folder: Folder;
  syncing: boolean;
  lastSyncTs?: string;
  daemonOnline: boolean;
  menuOpen: boolean;
  onMenuToggle: () => void;
  onMenuClose: () => void;
  onSync: () => void;
  onRemove: () => void;
  onViewDetails: () => void;
}

function FolderCard({
  folder,
  syncing,
  lastSyncTs,
  daemonOnline,
  menuOpen,
  onMenuToggle,
  onMenuClose,
  onSync,
  onRemove,
  onViewDetails,
}: FolderCardProps) {
  const statusChip = syncing
    ? <span className="chip chip-syncing"><RefreshCw size={9} strokeWidth={2} style={{ animation: "spin 1.5s linear infinite" }} />Syncing</span>
    : <span className="chip chip-success"><CheckCircle2 size={9} strokeWidth={2} />Up to date</span>;

  // fake progress percentage for visual interest (100% when done, partial when syncing)
  const progress = syncing ? 45 : 100;

  return (
    <div
      style={{ ...styles.card, cursor: "pointer" }}
      onClick={menuOpen ? onMenuClose : onViewDetails}
    >
      {/* Card header */}
      <div style={styles.cardHeader}>
        <div style={styles.folderIcon}>
          <FolderOpen size={16} strokeWidth={1.5} />
        </div>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={styles.folderName}>{folder.Name}</div>
          <div style={styles.folderMeta}>
            <span className="label-sm">{truncatePath(folder.LocalRoot)}</span>
            <span style={{ color: "var(--outline-variant)", margin: "0 0.25rem" }}>→</span>
            <span className="label-sm">{truncatePath(folder.RemoteBase)}</span>
          </div>
        </div>
        <div style={{ position: "relative" as const }}>
          <button className="btn-icon" onClick={(e) => { e.stopPropagation(); onMenuToggle(); }}>
            <MoreVertical size={15} strokeWidth={1.5} />
          </button>
          {menuOpen && (
            <ContextMenu onSync={onSync} onRemove={onRemove} onClose={onMenuClose} disabled={!daemonOnline} />
          )}
        </div>
      </div>

      {/* Progress bar */}
      <div style={{ marginTop: "1rem", marginBottom: "0.5rem" }}>
        <div style={styles.progressMeta}>
          {statusChip}
          <span className="label-sm" style={{ marginLeft: "auto" }}>
            {syncing ? `${progress}%` : "100%"}
          </span>
        </div>
        <div className="progress-track" style={{ marginTop: "0.5rem" }}>
          <div
            className="progress-fill"
            style={{ width: `${progress}%`, ...(syncing ? { background: "var(--gradient-primary)" } : {}) }}
          />
        </div>
      </div>

      {/* Footer */}
      <div style={styles.cardFooter}>
        <div style={styles.lastSync}>
          {lastSyncTs ? (
            <><Clock size={11} strokeWidth={1.5} style={{ color: "var(--outline)" }} /> Last synced {formatRelative(lastSyncTs)}</>
          ) : (
            <><AlertCircle size={11} strokeWidth={1.5} style={{ color: "var(--tertiary)" }} /> Never synced</>
          )}
        </div>
        <button
          className="btn-ghost"
          style={{ fontSize: "0.75rem", padding: "0.25rem 0.5rem", gap: "0.25rem" }}
          onClick={(e) => { e.stopPropagation(); onViewDetails(); }}
        >
          View Details
        </button>
      </div>

      <style>{`@keyframes spin { to { transform: rotate(360deg); } }`}</style>
    </div>
  );
}

// ── Context menu ───────────────────────────────────────────────────────────────

function ContextMenu({ onSync, onRemove, onClose, disabled }: { onSync: () => void; onRemove: () => void; onClose: () => void; disabled: boolean }) {
  return (
    <>
      <div style={styles.menuOverlay} onClick={onClose} />
      <div style={styles.menu}>
        <button
          style={styles.menuItem}
          onClick={onSync}
          disabled={disabled}
        >
          <RefreshCw size={13} strokeWidth={1.5} /> Sync Now
        </button>
        <div style={styles.menuDivider} />
        <button style={{ ...styles.menuItem, color: "var(--error)" }} onClick={onRemove}>
          <Trash2 size={13} strokeWidth={1.5} /> Remove Folder
        </button>
      </div>
    </>
  );
}

// ── Confirm dialog ─────────────────────────────────────────────────────────────

function ConfirmDialog({ name, onConfirm, onCancel }: { name: string; onConfirm: () => void; onCancel: () => void }) {
  return (
    <div style={styles.dialogOverlay}>
      <div style={styles.dialog}>
        <div style={styles.dialogIcon}>
          <Trash2 size={20} strokeWidth={1.5} style={{ color: "var(--error)" }} />
        </div>
        <h3 style={{ fontSize: "1rem", fontWeight: 600, color: "var(--on-surface)", marginBottom: "0.375rem" }}>
          Remove "{name}"?
        </h3>
        <p style={{ fontSize: "0.8125rem", color: "var(--on-surface-variant)", lineHeight: 1.5, marginBottom: "1.25rem" }}>
          The folder will be unregistered from the daemon. Local and remote files are not deleted.
        </p>
        <div style={{ display: "flex", gap: "0.625rem", justifyContent: "flex-end" }}>
          <button className="btn-secondary" style={{ fontSize: "0.8125rem" }} onClick={onCancel}>Cancel</button>
          <button
            style={{ background: "var(--error)", color: "#fff", border: "none", borderRadius: "var(--radius-md)", padding: "0.5rem 1rem", fontSize: "0.8125rem", fontWeight: 600, cursor: "pointer", fontFamily: "var(--font-family)" }}
            onClick={onConfirm}
          >
            Remove
          </button>
        </div>
      </div>
    </div>
  );
}

// ── Empty state ────────────────────────────────────────────────────────────────

function EmptyFolders({ onAdd }: { onAdd: () => void }) {
  return (
    <div style={styles.emptyWrap}>
      <FolderOpen size={40} strokeWidth={1} style={{ color: "var(--outline)", marginBottom: "0.75rem" }} />
      <div style={{ fontSize: "1rem", fontWeight: 500, color: "var(--on-surface)", marginBottom: "0.375rem" }}>
        No sync folders yet
      </div>
      <div style={{ fontSize: "0.8125rem", color: "var(--on-surface-variant)", marginBottom: "1.25rem" }}>
        Add a folder to start syncing with CERNBox.
      </div>
      <button className="btn-primary" onClick={onAdd}>Add Sync Folder</button>
    </div>
  );
}

// ── Styles ─────────────────────────────────────────────────────────────────────

const styles: Record<string, React.CSSProperties> = {
  root: {
    flex: 1,
    display: "flex",
    flexDirection: "column",
    padding: "1.5rem",
    gap: "1.25rem",
    overflow: "hidden auto",
  },
  pageHeader: {
    display: "flex",
    justifyContent: "space-between",
    alignItems: "flex-end",
  },
  title: {
    fontSize: "1.375rem",
    fontWeight: 300,
    letterSpacing: "-0.02em",
    color: "var(--on-surface)",
    marginBottom: "0.25rem",
  },
  subtitle: {
    fontSize: "0.8125rem",
    color: "var(--on-surface-variant)",
  },
  headerActions: {
    display: "flex",
    gap: "0.5rem",
  },
  actionBtn: {
    fontSize: "0.8125rem",
    display: "flex",
    alignItems: "center",
    gap: "0.375rem",
  },
  grid: {
    display: "grid",
    gridTemplateColumns: "repeat(auto-fill, minmax(310px, 1fr))",
    gap: "1rem",
  },
  card: {
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-xl)",
    padding: "1.25rem",
    display: "flex",
    flexDirection: "column",
    transition: "background var(--transition-base)",
    cursor: "default",
  },
  cardHeader: {
    display: "flex",
    alignItems: "flex-start",
    gap: "0.75rem",
  },
  folderIcon: {
    width: 36,
    height: 36,
    background: "var(--gradient-primary)",
    borderRadius: "var(--radius-lg)",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    color: "#FFFFFF",
    flexShrink: 0,
  },
  folderName: {
    fontSize: "0.9375rem",
    fontWeight: 600,
    color: "var(--on-surface)",
    marginBottom: "0.1875rem",
    whiteSpace: "nowrap" as const,
    overflow: "hidden",
    textOverflow: "ellipsis",
  },
  folderMeta: {
    display: "flex",
    alignItems: "center",
    gap: "0",
    overflow: "hidden",
  },
  progressMeta: {
    display: "flex",
    alignItems: "center",
  },
  cardFooter: {
    display: "flex",
    justifyContent: "space-between",
    alignItems: "center",
    marginTop: "0.75rem",
  },
  lastSync: {
    display: "flex",
    alignItems: "center",
    gap: "0.3rem",
    fontSize: "0.75rem",
    color: "var(--outline)",
  },
  statsBar: {
    display: "flex",
    gap: "0.75rem",
    alignItems: "center",
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-xl)",
    padding: "0.875rem 1.25rem",
    marginTop: "auto",
  },
  menuOverlay: {
    position: "fixed" as const,
    inset: 0,
    zIndex: 90,
  },
  menu: {
    position: "absolute" as const,
    right: 0,
    top: "calc(100% + 4px)",
    background: "var(--surface-container-highest)",
    borderRadius: "var(--radius-lg)",
    padding: "0.375rem",
    boxShadow: "var(--shadow-float)",
    zIndex: 100,
    minWidth: 160,
  },
  menuItem: {
    display: "flex",
    alignItems: "center",
    gap: "0.5rem",
    padding: "0.5rem 0.625rem",
    borderRadius: "var(--radius-md)",
    background: "transparent",
    color: "var(--on-surface-variant)",
    fontSize: "0.8125rem",
    fontWeight: 400,
    cursor: "pointer",
    border: "none",
    width: "100%",
    fontFamily: "var(--font-family)",
    transition: "all var(--transition-fast)",
  },
  menuDivider: {
    height: 1,
    background: "rgba(68,71,90,0.15)",
    margin: "0.25rem 0",
  },
  dialogOverlay: {
    position: "fixed" as const,
    inset: 0,
    background: "rgba(0,0,0,0.6)",
    backdropFilter: "blur(4px)",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    zIndex: 200,
  },
  dialog: {
    background: "var(--surface-container-highest)",
    borderRadius: "var(--radius-xl)",
    padding: "1.75rem",
    width: 380,
    boxShadow: "var(--shadow-float)",
  },
  dialogIcon: {
    width: 44,
    height: 44,
    background: "rgba(255,107,107,0.1)",
    borderRadius: "var(--radius-lg)",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    marginBottom: "1rem",
  },
  emptyWrap: {
    flex: 1,
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    justifyContent: "center",
    padding: "3rem",
    textAlign: "center" as const,
  },
};
