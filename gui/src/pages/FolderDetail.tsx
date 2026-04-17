import {
  ArrowLeft,
  RefreshCw,
  Trash2,
  FolderOpen,
  CheckCircle2,
  Folder,
  MoreVertical,
  ArrowUpRight,
  Settings,
  AlertCircle,
  Save,
} from "lucide-react";
import { useState, useEffect, useCallback, useRef } from "react";
import type { DaemonState } from "../hooks/useDaemon";
import type { Account, Folder as FolderType } from "../types";
import { listRemoteResources } from "../graph";
import {
  type TreeNode,
  sortChildren,
  collectHrefs,
  collectSelectedUrls,
  nodeSelectionState,
  displaySelectionState,
  pruneEmptyAncestors,
  expandSelectionExcluding,
  findNode,
  updateInTree,
  TreeRow,
} from "../components/FolderTree";

interface FolderDetailProps {
  folder: FolderType;
  daemon: DaemonState;
  account: Account;
  onBack: () => void;
  onRemove: () => void;
  onFolderUpdated: (f: FolderType) => void;
}

function formatRelative(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime();
  const s = Math.floor(diff / 1000);
  if (s < 60) return "just now";
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} mins ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}


export function FolderDetail({ folder, daemon, account, onBack, onRemove, onFolderUpdated }: FolderDetailProps) {
  const { status, daemonOnline, syncFolder } = daemon;
  const [confirmRemove, setConfirmRemove] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);

  const syncing = status?.syncing?.includes(folder.Name) ?? false;
  const lastSyncTs = status?.last_sync?.[folder.Name];
  const counts = status?.counts?.[folder.Name] ?? null;

  function handleSync() {
    setMenuOpen(false);
    syncFolder(folder.Name);
  }

  function handleRemove() {
    setMenuOpen(false);
    setConfirmRemove(true);
  }

  async function handleSaveSelection(remoteUrls: string[]) {
    const base = folder.RemoteBase.endsWith("/") ? folder.RemoteBase : folder.RemoteBase + "/";
    const relativeFolders = remoteUrls
      .map((url) => {
        const u = url.endsWith("/") ? url : url + "/";
        return u.startsWith(base) ? u.slice(base.length).replace(/\/$/, "") : null;
      })
      .filter((p): p is string => p !== null && p !== "");
    const updated: FolderType = { ...folder, Folders: relativeFolders };
    await daemon.updateFolder(updated);
    onFolderUpdated(updated);
  }

  return (
    <div style={s.root}>
      {/* Header */}
      <div style={s.header}>
        <button className="btn-icon" onClick={onBack} style={{ alignSelf: "flex-start", marginTop: "0.2rem" }}>
          <ArrowLeft size={16} strokeWidth={1.5} />
        </button>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={s.titleRow}>
            {/* Composite folder+settings icon */}
            <div style={s.folderIcon}>
              <FolderOpen size={26} strokeWidth={1.5} />
              <div style={s.folderIconBadge}>
                <Settings size={11} strokeWidth={2} />
              </div>
            </div>
            <div style={{ minWidth: 0 }}>
              <h1 style={s.title}>{folder.Name}</h1>
              <p style={s.subtitle}>{folder.LocalRoot}</p>
            </div>
          </div>
        </div>
        {/* Action menu */}
        <div style={{ position: "relative" as const, flexShrink: 0, alignSelf: "flex-start", marginTop: "0.2rem" }}>
          <button className="btn-icon" onClick={() => setMenuOpen(!menuOpen)}>
            <MoreVertical size={15} strokeWidth={1.5} />
          </button>
          {menuOpen && (
            <>
              <div style={s.menuOverlay} onClick={() => setMenuOpen(false)} />
              <div style={s.menu}>
                <button style={s.menuItem} onClick={handleSync} disabled={!daemonOnline}>
                  <RefreshCw size={13} strokeWidth={1.5} /> Sync Now
                </button>
                <div style={s.menuDivider} />
                <button style={{ ...s.menuItem, color: "var(--error)" }} onClick={handleRemove}>
                  <Trash2 size={13} strokeWidth={1.5} /> Remove Folder
                </button>
              </div>
            </>
          )}
        </div>
      </div>

      {/* Body */}
      <div style={s.body}>

        {/* Top: 4 stat cards */}
        <div style={s.statsRow}>
          <div style={s.statCard}>
            <p style={s.statLabel}>LAST SYNCED</p>
            <p style={s.statValue}>{lastSyncTs ? formatRelative(lastSyncTs) : "Never"}</p>
          </div>

          <div style={s.statCard}>
            <p style={s.statLabel}>FILES</p>
            <p style={s.statValue}>{counts ? counts.files.toLocaleString() : "—"}</p>
            <p style={s.statMeta}>
              {counts ? `Across ${counts.dirs} subfolder${counts.dirs !== 1 ? "s" : ""}` : "No data yet"}
            </p>
          </div>

          <div style={s.statCard}>
            <p style={s.statLabel}>SYNCED ITEMS</p>
            <p style={s.statValue}>{counts ? (counts.files + counts.dirs).toLocaleString() : "—"}</p>
            <p style={s.statMeta}>
              {counts
                ? `${counts.files} file${counts.files !== 1 ? "s" : ""} · ${counts.dirs} dir${counts.dirs !== 1 ? "s" : ""}`
                : "No data yet"}
            </p>
          </div>

          <div style={s.statCard}>
            <p style={s.statLabel}>STATUS</p>
            <p style={{ ...s.statValue, fontSize: "1.125rem", color: syncing ? "var(--tertiary)" : "var(--success)" }}>
              {syncing ? "Syncing" : "Up to date"}
            </p>
            <p style={s.statMeta}>
              {syncing
                ? <><RefreshCw size={10} strokeWidth={2} style={{ animation: "spin 1.5s linear infinite", marginRight: 4 }} />In progress</>
                : <><CheckCircle2 size={10} strokeWidth={1.5} style={{ marginRight: 4 }} />All files synced</>
              }
            </p>
          </div>

        </div>

        {/* Bottom: Selective Sync (left) + right column */}
        <div style={s.bottomRow}>

          {/* Selective Sync panel */}
          <div style={s.panel}>
            <div style={s.panelHeader}>
              <div style={s.panelTitleRow}>
                <Folder size={14} strokeWidth={1.5} style={{ color: "var(--primary)" }} />
                <span style={s.panelTitle}>Selective Sync</span>
              </div>
            </div>

            <InlineFolderTree
              folder={folder}
              account={account}
              onSave={handleSaveSelection}
            />
          </div>

          {/* Right column: Recent Activity + Folder Composition */}
          <div style={s.rightCol}>
            <div style={s.activitySection}>
              <div style={s.activityHeader}>
                <div style={s.activityTitleRow}>
                  <RefreshCw size={15} strokeWidth={1.5} style={{ color: "var(--on-surface-variant)" }} />
                  <span style={s.activityTitle}>Recent Activity</span>
                </div>
                <button className="btn-ghost" style={s.viewLogsBtn}>
                  VIEW ALL LOGS <ArrowUpRight size={11} strokeWidth={2} />
                </button>
              </div>
              <div style={s.activityList}>
                <ActivityItem
                  icon={<CheckCircle2 size={13} strokeWidth={1.5} />}
                  iconBg="rgba(107,217,160,0.15)"
                  iconColor="var(--success)"
                  title="Uploaded 12 research papers"
                  description="System successfully pushed new entries to cloud storage at high priority."
                  time="2 minutes ago"
                />
                <ActivityItem
                  icon={<RefreshCw size={13} strokeWidth={1.5} />}
                  iconBg="rgba(255,181,150,0.15)"
                  iconColor="var(--tertiary)"
                  title="Modified 'thesis_outline_v4.docx'"
                  description="Detected local change. Conflict resolution bypassed as cloud was older."
                  time="1 hour ago"
                />
              </div>
            </div>

            {/* Sync Settings */}
            <div style={s.sideCard}>
              <div style={s.settingsHeader}>
                <Settings size={14} strokeWidth={1.5} style={{ color: "var(--on-surface-variant)" }} />
                <span style={s.settingsTitle}>Sync Settings</span>
              </div>

              <div style={s.settingsList}>
                <SettingRow
                  label="Sync hidden files"
                  description="Include dotfiles and system items"
                  enabled={folder.Settings?.sync_hidden_files ?? false}
                  onChange={(val) => {
                    const updated: FolderType = { ...folder, Settings: { ...folder.Settings, sync_hidden_files: val } };
                    daemon.updateFolder(updated).then(() => onFolderUpdated(updated));
                  }}
                />
                <SettingRow
                  label="Auto-sync on change"
                  description="Real-time backup of local edits"
                  enabled={true}
                  todo
                />
                <SyncModeRow />
              </div>


            </div>

          </div>

        </div>
      </div>

      {/* Confirm remove dialog */}
      {confirmRemove && (
        <ConfirmDialog
          name={folder.Name}
          onConfirm={() => { onRemove(); setConfirmRemove(false); }}
          onCancel={() => setConfirmRemove(false)}
        />
      )}

      <style>{`
        @keyframes spin { to { transform: rotate(360deg); } }
        @keyframes pulse { 0%,100% { opacity: 1; } 50% { opacity: 0.4; } }
      `}</style>
    </div>
  );
}

// ── SettingRow ─────────────────────────────────────────────────────────────────

function SettingRow({ label, description, enabled, onChange, todo }: { label: string; description: string; enabled: boolean; onChange?: (val: boolean) => void; todo?: boolean }) {
  const [on, setOn] = useState(enabled);
  function toggle() {
    const next = !on;
    setOn(next);
    onChange?.(next);
  }
  return (
    <div style={sr.row}>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ display: "flex", alignItems: "center", gap: "0.375rem", marginBottom: "0.125rem" }}>
          <p style={{ ...sr.label, marginBottom: 0 }}>{label}</p>
          {todo && (
            <span style={{ fontSize: "0.5625rem", fontWeight: 700, letterSpacing: "0.06em", color: "var(--tertiary)", background: "rgba(255,181,150,0.15)", padding: "0.1rem 0.375rem", borderRadius: "var(--radius-full)" }}>
              SOON
            </span>
          )}
        </div>
        <p style={sr.desc}>{description}</p>
      </div>
      <button
        style={{ ...sr.track, background: on ? "var(--primary-container)" : "var(--surface-container-highest)" }}
        onClick={toggle}
        role="switch"
        aria-checked={on}
      >
        <span style={{ ...sr.thumb, transform: on ? "translateX(18px)" : "translateX(2px)" }} />
      </button>
    </div>
  );
}

const sr: Record<string, React.CSSProperties> = {
  row: {
    display: "flex",
    alignItems: "center",
    gap: "1rem",
    padding: "0.625rem 0",
  },
  label: {
    fontSize: "0.875rem",
    fontWeight: 500,
    color: "var(--on-surface)",
    marginBottom: "0.125rem",
  },
  desc: {
    fontSize: "0.75rem",
    color: "var(--on-surface-variant)",
  },
  track: {
    width: 38,
    height: 22,
    borderRadius: "var(--radius-full)",
    border: "none",
    padding: 0,
    cursor: "pointer",
    flexShrink: 0,
    position: "relative" as const,
    transition: "background var(--transition-base)",
  },
  thumb: {
    position: "absolute" as const,
    top: 3,
    width: 16,
    height: 16,
    borderRadius: "50%",
    background: "#fff",
    transition: "transform var(--transition-base)",
    boxShadow: "0 1px 3px rgba(0,0,0,0.3)",
  },
};

// ── SyncModeRow ────────────────────────────────────────────────────────────────

function SyncModeRow() {
  return (
    <div style={sr.row}>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ display: "flex", alignItems: "center", gap: "0.375rem", marginBottom: "0.125rem" }}>
          <p style={{ ...sr.label, marginBottom: 0 }}>Sync mode</p>
          <span style={{ fontSize: "0.5625rem", fontWeight: 700, letterSpacing: "0.06em", color: "var(--tertiary)", background: "rgba(255,181,150,0.15)", padding: "0.1rem 0.375rem", borderRadius: "var(--radius-full)" }}>
            SOON
          </span>
        </div>
        <p style={sr.desc}>Choose how files are kept locally</p>
      </div>
      <div style={smr.segmented}>
        <div style={{ ...smr.option, ...smr.optionActive }}>Mirror</div>
        <div style={{ ...smr.option, ...smr.optionDisabled }}>On-demand</div>
      </div>
    </div>
  );
}

const smr: Record<string, React.CSSProperties> = {
  segmented: {
    display: "flex",
    background: "var(--surface-container-highest)",
    borderRadius: "var(--radius-full)",
    padding: "0.1875rem",
    gap: "0.125rem",
    flexShrink: 0,
  },
  option: {
    fontSize: "0.6875rem",
    fontWeight: 500,
    color: "var(--on-surface-variant)",
    padding: "0.25rem 0.625rem",
    borderRadius: "var(--radius-full)",
    cursor: "not-allowed",
    whiteSpace: "nowrap" as const,
  },
  optionActive: {
    background: "var(--primary-container)",
    color: "var(--on-surface)",
    fontWeight: 600,
  },
  optionDisabled: {
    opacity: 0.45,
  },
};

// ── ActivityItem ───────────────────────────────────────────────────────────────

interface ActivityItemProps {
  icon: React.ReactNode;
  iconBg: string;
  iconColor: string;
  title: string;
  description: string;
  time: string;
}

function ActivityItem({ icon, iconBg, iconColor, title, description, time }: ActivityItemProps) {
  return (
    <div style={s.activityItem}>
      <div style={{ ...s.activityIconWrap, background: iconBg, color: iconColor }}>
        {icon}
      </div>
      <div style={{ minWidth: 0 }}>
        <p style={s.activityItemTitle}>{title}</p>
        <p style={s.activityDesc}>{description}</p>
        <p style={s.activityTime}>{time}</p>
      </div>
    </div>
  );
}

// ── InlineFolderTree ───────────────────────────────────────────────────────────

interface InlineFolderTreeProps {
  folder: FolderType;
  account: Account;
  onSave: (remoteUrls: string[]) => Promise<void>;
}

function InlineFolderTree({ folder, account, onSave }: InlineFolderTreeProps) {
  const remoteBase = folder.RemoteBase.endsWith("/") ? folder.RemoteBase : folder.RemoteBase + "/";

  const makeRoot = useCallback((): TreeNode => ({
    resource: { href: remoteBase, name: folder.Name, isCollection: true, size: 0, lastModified: "" },
    children: null,
    loading: false,
    error: null,
  }), [remoteBase, folder.Name]);

  const [rootNode, setRootNode] = useState<TreeNode>(makeRoot);
  const rootNodeRef = useRef(rootNode);
  rootNodeRef.current = rootNode;
  const [treeLoading, setTreeLoading] = useState(true);
  const [treeError, setTreeError] = useState<string | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [savedSelected, setSavedSelected] = useState<Set<string>>(new Set());
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [saving, setSaving] = useState(false);

  const isDirty = selected.size !== savedSelected.size || [...selected].some((h) => !savedSelected.has(h));

  const loadRoot = useCallback(() => {
    setTreeLoading(true);
    setTreeError(null);
    const root = makeRoot();

    async function buildInitialTree() {
      const resources = await listRemoteResources(remoteBase, account.username, account.password);
      let currentRoot: TreeNode = {
        ...root,
        children: sortChildren(resources.map((r) => ({
          resource: r,
          children: r.isCollection ? null : [],
          loading: false,
          error: null,
        }))),
      };

      const expandSet = new Set([remoteBase]);

      // Pre-load ancestors of saved selected folders so they appear expanded.
      if (folder.Folders.length > 0) {
        const ancestorRels = new Set<string>();
        for (const rel of folder.Folders) {
          const parts = rel.split("/").filter(Boolean);
          for (let i = 1; i < parts.length; i++) {
            ancestorRels.add(parts.slice(0, i).join("/"));
          }
        }
        // Load shallowest first so updateInTree can find each node.
        const sortedAncestors = [...ancestorRels].sort(
          (a, b) => a.split("/").length - b.split("/").length
        );
        for (const rel of sortedAncestors) {
          const href = remoteBase + rel + "/";
          expandSet.add(href);
          try {
            const childResources = await listRemoteResources(href, account.username, account.password);
            currentRoot = updateInTree(currentRoot, href, (n) => ({
              ...n,
              children: sortChildren(childResources.map((r) => ({
                resource: r,
                children: r.isCollection ? null : [],
                loading: false,
                error: null,
              }))),
            }));
          } catch {
            // Ignore — the node stays collapsed; user can expand manually.
          }
        }
      }

      return { currentRoot, expandSet };
    }

    buildInitialTree()
      .then(({ currentRoot, expandSet }) => {
        setRootNode(currentRoot);
        setExpanded(expandSet);
        const initial = folder.Folders.length === 0
          ? new Set([remoteBase])
          : new Set(folder.Folders.map((rel) => remoteBase + rel + "/"));
        setSelected(initial);
        setSavedSelected(new Set(initial));
      })
      .catch((e) => setTreeError(String(e)))
      .finally(() => setTreeLoading(false));
  }, [remoteBase, account.username, account.password, folder.Folders, makeRoot]);

  useEffect(() => { loadRoot(); }, [loadRoot]);

  function updateNode(target: TreeNode, fn: (n: TreeNode) => TreeNode) {
    setRootNode((prev) => updateInTree(prev, target.resource.href, fn));
  }

  function toggleExpand(node: TreeNode) {
    const href = node.resource.href;
    if (expanded.has(href)) {
      setExpanded((prev) => { const s = new Set(prev); s.delete(href); return s; });
      return;
    }
    setExpanded((prev) => new Set(prev).add(href));
    if (node.children !== null) return;
    updateNode(node, (n) => ({ ...n, loading: true, error: null }));
    listRemoteResources(href, account.username, account.password)
      .then((resources) => {
        updateNode(node, (n) => ({
          ...n, loading: false,
          children: sortChildren(resources.map((r) => ({
            resource: r,
            children: r.isCollection ? null : [],
            loading: false,
            error: null,
          }))),
        }));
      })
      .catch((e) => updateNode(node, (n) => ({ ...n, loading: false, error: String(e) })));
  }

  function toggleSelect(node: TreeNode) {
    setSelected((prev) => {
      const state = displaySelectionState(node, prev);
      const next = new Set(prev);
      if (state === "all") {
        if (next.has(node.resource.href)) {
          // Directly selected — remove the whole subtree.
          collectHrefs(node).forEach((h) => next.delete(h));
        } else {
          // Covered by an ancestor — expand that ancestor's selection
          // Check if covered by an ancestor.
          let coveredByAncestor = false;
          for (const href of next) {
            if (node.resource.href.startsWith(href) && node.resource.href !== href) {
              const ancestor = findNode(rootNodeRef.current, href);
              if (ancestor) expandSelectionExcluding(ancestor, node.resource.href, next);
              coveredByAncestor = true;
              break;
            }
          }
          if (!coveredByAncestor) {
            // All descendants are individually selected — remove them all.
            collectHrefs(node).forEach((h) => next.delete(h));
          }
        }
      } else {
        collectHrefs(node).forEach((h) => next.add(h));
      }
      pruneEmptyAncestors(rootNodeRef.current, next);
      return next;
    });
  }

  async function handleSave() {
    setSaving(true);
    try {
      const urls = nodeSelectionState(rootNode, selected) === "all"
        ? []
        : collectSelectedUrls(rootNode, selected);
      await onSave(urls);
      setSavedSelected(new Set(selected));
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <div style={{ flex: 1, overflowY: "auto" as const, minHeight: 0 }}>
        {treeLoading && (
          <div style={{ display: "flex", alignItems: "center", justifyContent: "center", padding: "2rem", gap: "0.5rem" }}>
            <div style={s.spinner} />
            <span style={{ fontSize: "0.8125rem", color: "var(--on-surface-variant)" }}>Loading…</span>
          </div>
        )}
        {!treeLoading && treeError && (
          <div style={{ display: "flex", flexDirection: "column" as const, alignItems: "center", padding: "1.5rem", gap: "0.5rem" }}>
            <AlertCircle size={18} strokeWidth={1.5} style={{ color: "var(--error)" }} />
            <p style={{ fontSize: "0.8125rem", color: "var(--error)" }}>{treeError}</p>
            <button className="btn-secondary" style={{ fontSize: "0.8125rem" }} onClick={loadRoot}>Retry</button>
          </div>
        )}
        {!treeLoading && !treeError && (
          <TreeRow
            node={rootNode}
            depth={0}
            expanded={expanded}
            selected={selected}
            onToggleExpand={toggleExpand}
            onToggleSelect={toggleSelect}
            showSize={false}
            showBadge={false}
          />
        )}
      </div>
      {isDirty && (
        <div style={s.panelActions}>
          <div style={{ marginLeft: "auto", display: "flex", gap: "0.5rem" }}>
            <button className="btn-secondary" style={{ fontSize: "0.8125rem" }} onClick={() => setSelected(new Set(savedSelected))} disabled={saving}>
              Cancel
            </button>
            <button className="btn-primary" style={{ fontSize: "0.8125rem" }} onClick={handleSave} disabled={saving}>
              {saving
                ? <><RefreshCw size={13} strokeWidth={1.5} style={{ animation: "spin 1s linear infinite" }} /> Saving…</>
                : <><Save size={13} strokeWidth={1.5} /> Save</>}
            </button>
          </div>
        </div>
      )}
    </>
  );
}

// ── ConfirmDialog ──────────────────────────────────────────────────────────────

function ConfirmDialog({ name, onConfirm, onCancel }: { name: string; onConfirm: () => void; onCancel: () => void }) {
  return (
    <div style={s.dialogOverlay}>
      <div style={s.dialog}>
        <div style={s.dialogIcon}>
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

// ── Styles ─────────────────────────────────────────────────────────────────────

const s: Record<string, React.CSSProperties> = {
  root: {
    flex: 1,
    display: "flex",
    flexDirection: "column",
    padding: "1.5rem",
    gap: "1.25rem",
    overflow: "hidden auto",
  },
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
    background: "var(--gradient-primary)",
    borderRadius: "var(--radius-xl)",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    color: "#FFFFFF",
    flexShrink: 0,
    position: "relative" as const,
  },
  folderIconBadge: {
    position: "absolute" as const,
    bottom: 4,
    right: 4,
    background: "rgba(255,255,255,0.2)",
    borderRadius: "var(--radius-sm)",
    width: 18,
    height: 18,
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
  },
  title: {
    fontSize: "1.5rem",
    fontWeight: 600,
    letterSpacing: "-0.02em",
    color: "var(--on-surface)",
    whiteSpace: "nowrap" as const,
    overflow: "hidden",
    textOverflow: "ellipsis",
  },
  subtitle: {
    fontSize: "0.75rem",
    color: "var(--outline)",
    marginTop: "0.2rem",
    whiteSpace: "nowrap" as const,
    overflow: "hidden",
    textOverflow: "ellipsis",
  },

  // Body layout
  body: {
    flex: 1,
    display: "flex",
    flexDirection: "column" as const,
    gap: "1rem",
    overflow: "hidden auto",
    minHeight: 0,
  },
  statsRow: {
    display: "grid",
    gridTemplateColumns: "repeat(4, 1fr)",
    gap: "0.75rem",
    flexShrink: 0,
  },
  bottomRow: {
    flex: 1,
    display: "flex",
    gap: "1rem",
    minHeight: 0,
  },
  rightCol: {
    flex: 1,
    display: "flex",
    flexDirection: "column" as const,
    gap: "0.75rem",
    overflow: "hidden auto",
  },
  statCard: {
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-xl)",
    padding: "1.125rem 1.25rem",
    display: "flex",
    flexDirection: "column" as const,
    justifyContent: "space-between",
    minHeight: 100,
  },
  statCardAccent: {
    background: "var(--surface-container-highest)",
    position: "relative" as const,
    overflow: "hidden",
  },
  statBgIcon: {
    position: "absolute" as const,
    right: "-0.5rem",
    bottom: "-0.75rem",
    color: "rgba(180,197,255,0.06)",
    pointerEvents: "none" as const,
  },
  statusDot: {
    display: "inline-block",
    width: 6,
    height: 6,
    borderRadius: "50%",
    background: "var(--success)",
    marginRight: "0.35rem",
    verticalAlign: "middle",
  },
  statLabel: {
    fontSize: "0.5625rem",
    fontWeight: 700,
    letterSpacing: "0.08em",
    color: "var(--outline)",
    marginBottom: "0.5rem",
  },
  statValue: {
    fontSize: "1.625rem",
    fontWeight: 300,
    color: "var(--on-surface)",
    letterSpacing: "-0.02em",
    lineHeight: 1.1,
    marginBottom: "0.25rem",
  },
  statMeta: {
    fontSize: "0.6875rem",
    color: "var(--outline)",
    display: "flex",
    alignItems: "center",
  },

  // Selective sync panel
  panel: {
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-xl)",
    overflow: "hidden",
    flex: 1.5,
    display: "flex",
    flexDirection: "column" as const,
    minWidth: 0,
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
  panelActions: {
    display: "flex",
    alignItems: "center",
    padding: "0.75rem 1rem",
    borderTop: "1px solid rgba(68,71,90,0.15)",
    flexShrink: 0,
  },

  // Tree
  treeColHeader: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    padding: "0.375rem 1rem",
    fontSize: "0.5625rem",
    fontWeight: 700,
    letterSpacing: "0.07em",
    color: "var(--outline)",
    borderBottom: "1px solid rgba(68,71,90,0.1)",
  },
  treeList: {
    padding: "0.25rem 0",
  },
  treeRow: {
    display: "flex",
    alignItems: "center",
    gap: "0.4rem",
    height: 36,
    paddingRight: "1rem",
    cursor: "default",
  },
  treeRowName: {
    flex: 1,
    fontSize: "0.8125rem",
    color: "var(--on-surface)",
    whiteSpace: "nowrap" as const,
    overflow: "hidden",
    textOverflow: "ellipsis",
    minWidth: 0,
  },

  // Sidebar
  sideCard: {
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-xl)",
    padding: "1rem",
    flexShrink: 0,
  },
  settingsHeader: {
    display: "flex",
    alignItems: "center",
    gap: "0.375rem",
    marginBottom: "0.25rem",
  },
  settingsTitle: {
    fontSize: "1rem",
    fontWeight: 600,
    color: "var(--on-surface)",
  },
  settingsList: {
    display: "flex",
    flexDirection: "column" as const,
    margin: "0.25rem 0",
  },
  sideCardLabel: {
    fontSize: "0.5625rem",
    fontWeight: 700,
    letterSpacing: "0.08em",
    color: "var(--outline)",
    marginBottom: "0.75rem",
  },

  // Paths
  pathRow: {
    display: "flex",
    alignItems: "flex-start",
    gap: "0.625rem",
    marginBottom: "0.625rem",
  },
  pathIconWrap: {
    width: 28,
    height: 28,
    borderRadius: "var(--radius-md)",
    background: "rgba(180,197,255,0.08)",
    color: "var(--primary)",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    flexShrink: 0,
  },
  pathRowLabel: {
    fontSize: "0.625rem",
    fontWeight: 600,
    color: "var(--outline)",
    letterSpacing: "0.04em",
    marginBottom: "0.1rem",
  },
  pathValue: {
    fontSize: "0.75rem",
    color: "var(--on-surface-variant)",
    wordBreak: "break-all" as const,
    lineHeight: 1.4,
  },
  pathDivider: {
    height: 1,
    background: "rgba(68,71,90,0.15)",
    margin: "0.375rem 0 0.625rem",
  },

  // Activity
  activitySection: {
    flex: 1,
    display: "flex",
    flexDirection: "column" as const,
    gap: "1rem",
    overflow: "hidden auto",
    minHeight: 0,
  },
  activityHeader: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
  },
  activityTitleRow: {
    display: "flex",
    alignItems: "center",
    gap: "0.5rem",
  },
  activityTitle: {
    fontSize: "1rem",
    fontWeight: 600,
    color: "var(--on-surface)",
  },
  viewLogsBtn: {
    fontSize: "0.6875rem",
    fontWeight: 600,
    letterSpacing: "0.04em",
    color: "var(--primary)",
    padding: "0.125rem 0.25rem",
    gap: "0.25rem",
  },
  activityList: {
    display: "flex",
    flexDirection: "column" as const,
    gap: "1.25rem",
  },
  activityItem: {
    display: "flex",
    alignItems: "flex-start",
    gap: "0.75rem",
  },
  activityIconWrap: {
    width: 30,
    height: 30,
    borderRadius: "var(--radius-md)",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    flexShrink: 0,
    marginTop: "0.05rem",
  },
  activityItemTitle: {
    fontSize: "0.8125rem",
    fontWeight: 600,
    color: "var(--on-surface)",
    marginBottom: "0.2rem",
    lineHeight: 1.3,
  },
  activityDesc: {
    fontSize: "0.75rem",
    color: "var(--on-surface-variant)",
    lineHeight: 1.4,
    marginBottom: "0.25rem",
  },
  activityTime: {
    fontSize: "0.625rem",
    color: "var(--outline)",
  },
  activityEmpty: {
    display: "flex",
    flexDirection: "column" as const,
    alignItems: "center",
    padding: "0.75rem 0",
  },

  // Context menu
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

  // Dialog
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
  spinner: {
    width: 20,
    height: 20,
    border: "2px solid var(--surface-container-highest)",
    borderTopColor: "var(--primary)",
    borderRadius: "50%",
    animation: "spin 0.8s linear infinite",
    flexShrink: 0,
  },
};
