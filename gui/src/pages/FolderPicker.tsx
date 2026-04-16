import { useState, useEffect, useCallback, useRef } from "react";
import {
  ArrowLeft,
  Folder,
  File,
  ChevronRight,
  ChevronDown,
  HardDrive,
  RefreshCw,
  AlertCircle,
  Check,
  Minus,
  ArrowRight,
} from "lucide-react";
import { listRemoteResources } from "../graph";
import type { Space, RemoteResource } from "../types";

// ── Types ──────────────────────────────────────────────────────────────────────

interface TreeNode {
  resource: RemoteResource;
  children: TreeNode[] | null; // null = not yet loaded
  loading: boolean;
  error: string | null;
}

type SelectionState = "none" | "partial" | "all";

interface FolderPickerProps {
  space: Space;
  username: string;
  password: string;
  onBack: () => void;
  /** Called when the user confirms; receives the list of selected resource URLs */
  onConfirm: (selectedUrls: string[]) => void;
}

// ── Helpers ────────────────────────────────────────────────────────────────────

function formatBytes(bytes: number): string {
  if (bytes === 0) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = bytes;
  let u = 0;
  while (v >= 1024 && u < units.length - 1) { v /= 1024; u++; }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[u]}`;
}

/** Sort tree nodes so directories appear before files, both groups alphabetically. */
function sortChildren(nodes: TreeNode[]): TreeNode[] {
  return [...nodes].sort((a, b) => {
    if (a.resource.isCollection !== b.resource.isCollection) {
      return a.resource.isCollection ? -1 : 1;
    }
    return a.resource.name.localeCompare(b.resource.name);
  });
}

/** Collect all collection hrefs in a subtree (including the root node itself). Files are excluded since sync operates at folder granularity. */
function collectHrefs(node: TreeNode): string[] {
  if (!node.resource.isCollection) return [];
  const hrefs = [node.resource.href];
  if (node.children) {
    for (const child of node.children) hrefs.push(...collectHrefs(child));
  }
  return hrefs;
}

/** Return the size of a node's subtree.
 *  - Files: use resource.size directly.
 *  - Collections with a server-provided oc:size (resource.size > 0): use it directly.
 *  - Collections without a server size (synthetic root): sum loaded children. */
function subtreeSize(node: TreeNode): number {
  if (!node.resource.isCollection) return node.resource.size;
  if (node.resource.size > 0) return node.resource.size;
  return (node.children ?? []).reduce((acc, c) => acc + subtreeSize(c), 0);
}

/**
 * Recursively collect the minimal set of URLs that represents the selection.
 * - Fully selected node: emit its own href (no need to recurse into children).
 * - Partially selected node: recurse into children to find the selected subtrees.
 * - Unselected node: emit nothing.
 */
function collectSelectedUrls(node: TreeNode, selected: Set<string>): string[] {
  const state = nodeSelectionState(node, selected);
  if (state === "none") return [];
  if (state === "all") return [node.resource.href];
  // partial — recurse into children
  const urls: string[] = [];
  for (const child of node.children ?? []) {
    urls.push(...collectSelectedUrls(child, selected));
  }
  return urls;
}

/**
 * Remove from `selected` any folder href whose subtree has become fully
 * deselected. Mutates `selected` in place.
 * Returns true if this node or any of its descendants is still selected.
 */
function pruneEmptyAncestors(node: TreeNode, selected: Set<string>): boolean {
  if (!node.resource.isCollection) return false;
  const anyChildSelected = (node.children ?? []).some((c) => pruneEmptyAncestors(c, selected));
  if (!anyChildSelected && selected.has(node.resource.href)) {
    // No descendants are selected — only keep this href if its children haven't
    // been loaded yet (null means unloaded; we treat it as a valid leaf selection).
    if (node.children !== null) {
      selected.delete(node.resource.href);
      return false;
    }
  }
  return anyChildSelected || selected.has(node.resource.href);
}

/** Recursively collect the fully-selected nodes (for sidebar display and size). */
function collectSelectedNodes(node: TreeNode, selected: Set<string>): TreeNode[] {
  const state = nodeSelectionState(node, selected);
  if (state === "none") return [];
  if (state === "all") return [node];
  // partial — recurse into children
  const nodes: TreeNode[] = [];
  for (const child of node.children ?? []) {
    nodes.push(...collectSelectedNodes(child, selected));
  }
  return nodes;
}

/** Compute selection state for a node given the selected set. */
function nodeSelectionState(node: TreeNode, selected: Set<string>): SelectionState {
  const hrefs = collectHrefs(node);
  const selectedCount = hrefs.filter((h) => selected.has(h)).length;
  if (selectedCount === 0) return "none";
  if (selectedCount === hrefs.length) return "all";
  return "partial";
}

// ── Component ──────────────────────────────────────────────────────────────────

// Build the synthetic root node representing the space itself.
// Its children are loaded lazily (null = not yet loaded).
function makeSpaceRootNode(space: Space): TreeNode {
  return {
    resource: {
      href: space.webdav_url.endsWith("/") ? space.webdav_url : space.webdav_url + "/",
      name: space.name,
      isCollection: true,
      size: 0,
      lastModified: "",
    },
    children: null,
    loading: false,
    error: null,
  };
}

export function FolderPicker({ space, username, password, onBack, onConfirm }: FolderPickerProps) {
  const [rootNode, setRootNode] = useState<TreeNode>(() => makeSpaceRootNode(space));
  const rootNodeRef = useRef(rootNode);
  rootNodeRef.current = rootNode;
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [expanded, setExpanded] = useState<Set<string>>(new Set());

  // ── Load root ────────────────────────────────────────────────────────────────

  const loadRoot = useCallback(() => {
    setLoading(true);
    setError(null);
    const spaceRoot = makeSpaceRootNode(space);
    listRemoteResources(space.webdav_url, username, password)
      .then((resources) => {
        setRootNode({
          ...spaceRoot,
          children: sortChildren(resources.map((r) => ({
            resource: r,
            children: r.isCollection ? null : [],
            loading: false,
            error: null,
          }))),
        });
        // Auto-expand the space root so children are visible immediately
        setExpanded((prev) => new Set(prev).add(spaceRoot.resource.href));
      })
      .catch((e) => setError(String(e)))
      .finally(() => setLoading(false));
  }, [space.webdav_url, username, password]);

  useEffect(() => { loadRoot(); }, [loadRoot]);

  // ── Expand / load children ───────────────────────────────────────────────────

  function toggleExpand(node: TreeNode) {
    const href = node.resource.href;
    const isOpen = expanded.has(href);

    if (isOpen) {
      setExpanded((prev) => { const s = new Set(prev); s.delete(href); return s; });
      return;
    }

    setExpanded((prev) => new Set(prev).add(href));

    if (node.children !== null) return; // already loaded

    // Mark as loading
    updateNode(node, (n) => ({ ...n, loading: true, error: null }));

    listRemoteResources(node.resource.href, username, password)
      .then((resources) => {
        updateNode(node, (n) => ({
          ...n,
          loading: false,
          children: sortChildren(resources.map((r) => ({
            resource: r,
            children: r.isCollection ? null : [],
            loading: false,
            error: null,
          }))),
        }));
      })
      .catch((e) => {
        updateNode(node, (n) => ({ ...n, loading: false, error: String(e) }));
      });
  }

  /** Immutably update a node anywhere in the tree by href. */
  function updateNode(target: TreeNode, fn: (n: TreeNode) => TreeNode) {
    setRootNode((prev) => updateInTree(prev, target.resource.href, fn));
  }

  // ── Selection logic ──────────────────────────────────────────────────────────

  function toggleSelect(node: TreeNode) {
    const hrefs = collectHrefs(node);
    const state = nodeSelectionState(node, selected);
    setSelected((prev) => {
      const next = new Set(prev);
      if (state === "all") {
        hrefs.forEach((h) => next.delete(h));
      } else {
        hrefs.forEach((h) => next.add(h));
      }
      // Clean up ancestor hrefs that no longer have any selected descendants
      pruneEmptyAncestors(rootNodeRef.current, next);
      return next;
    });
  }

  // ── Stats ────────────────────────────────────────────────────────────────────

  const selectedTopLevel = collectSelectedNodes(rootNode, selected);
  const totalSelectedSize = selectedTopLevel.reduce((acc, n) => acc + subtreeSize(n), 0);

  // ── Confirm ──────────────────────────────────────────────────────────────────

  function handleConfirm() {
    if (nodeSelectionState(rootNode, selected) === "all") {
      onConfirm([]);
      return;
    }
    onConfirm(collectSelectedUrls(rootNode, selected));
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
          <h1 style={s.title}>{space.name}</h1>
          <p style={s.subtitle}>Select the folders you want to synchronize.</p>
        </div>
        <button className="btn-icon" onClick={loadRoot} title="Reload">
          <RefreshCw size={15} strokeWidth={1.5} />
        </button>
      </div>

      {/* Body: file tree + sidebar */}
      <div style={s.body}>
        {/* ── File tree ── */}
        <div style={s.treePanel}>
          {/* Column headers */}
          <div style={s.colHeader}>
            <span style={{ flex: 1 }}>NAME</span>
            <span style={{ width: 80, textAlign: "right" as const }}>SIZE</span>
            <span style={{ width: 100, textAlign: "right" as const }}>STATUS</span>
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
                <button className="btn-secondary" onClick={loadRoot}>Retry</button>
              </div>
            )}
            {!loading && !error && (
              <TreeRow
                key={rootNode.resource.href}
                node={rootNode}
                depth={0}
                expanded={expanded}
                selected={selected}
                onToggleExpand={toggleExpand}
                onToggleSelect={toggleSelect}
              />
            )}
          </div>
        </div>

        {/* ── Sidebar ── */}
        <div style={s.sidebar}>
          {/* Sync summary */}
          <div style={s.sideCard}>
            <p style={s.sideCardLabel}>SYNC SUMMARY</p>

            <div style={s.summaryRow}>
              <HardDrive size={14} strokeWidth={1.5} style={{ color: "var(--outline)", flexShrink: 0 }} />
              <div>
                <p style={s.summaryValue}>
                  {selected.size > 0 ? formatBytes(totalSelectedSize) : "—"}
                </p>
                <p style={s.summaryMeta}>
                  Total Selected
                  {selectedTopLevel.length > 0 && ` · ${selectedTopLevel.length} folder${selectedTopLevel.length > 1 ? "s" : ""}`}
                </p>
              </div>
            </div>

            {selectedTopLevel.length > 0 && (
              <div style={s.selectedList}>
                {selectedTopLevel.slice(0, 6).map((n) => (
                  <div key={n.resource.href} style={s.selectedItem}>
                    <Folder size={11} strokeWidth={1.5} style={{ color: "var(--primary)", flexShrink: 0 }} />
                    <span style={s.selectedItemName}>{n.resource.name}</span>
                  </div>
                ))}
                {selectedTopLevel.length > 6 && (
                  <p style={{ fontSize: "0.6875rem", color: "var(--outline)", marginTop: "0.25rem" }}>
                    +{selectedTopLevel.length - 6} more
                  </p>
                )}
              </div>
            )}
          </div>

          {/* Space info */}
          <div style={s.sideCard}>
            <p style={s.sideCardLabel}>SPACE</p>
            <p style={{ fontSize: "0.8125rem", color: "var(--on-surface)", fontWeight: 500, marginBottom: "0.25rem" }}>
              {space.name}
            </p>
            {space.description && (
              <p style={{ fontSize: "0.6875rem", color: "var(--outline)" }}>{space.description}</p>
            )}
            <div style={{ marginTop: "0.625rem" }}>
              <span style={s.driveTypeBadge}>{space.drive_type.toUpperCase()}</span>
            </div>
          </div>

          {/* Actions */}
          <div style={s.actions}>
            <button
              className="btn-secondary"
              onClick={handleConfirm}
              disabled={selected.size === 0}
              title={selected.size === 0 ? "Select at least one folder" : undefined}
              style={{ display: "flex", alignItems: "center", justifyContent: "center", gap: "0.375rem" }}
            >
              Continue
              <ArrowRight size={14} strokeWidth={1.5} />
            </button>
          </div>
        </div>
      </div>

      <style>{`@keyframes spin { to { transform: rotate(360deg); } }`}</style>
    </div>
  );
}

// ── TreeRow ────────────────────────────────────────────────────────────────────

interface TreeRowProps {
  node: TreeNode;
  depth: number;
  expanded: Set<string>;
  selected: Set<string>;
  onToggleExpand: (node: TreeNode) => void;
  onToggleSelect: (node: TreeNode) => void;
}

function TreeRow({ node, depth, expanded, selected, onToggleExpand, onToggleSelect }: TreeRowProps) {
  const [hovered, setHovered] = useState(false);
  const { resource } = node;
  const isOpen = expanded.has(resource.href);
  const isFile = !resource.isCollection;
  const selState = nodeSelectionState(node, selected);

  return (
    <>
      <div
        style={{
          ...s.treeRow,
          paddingLeft: `${0.75 + depth * 1.25}rem`,
          background: !isFile && hovered ? "var(--surface-container-high)" : "transparent",
          opacity: isFile ? 0.45 : 1,
          cursor: isFile ? "default" : "pointer",
        }}
        onMouseEnter={() => { if (!isFile) setHovered(true); }}
        onMouseLeave={() => setHovered(false)}
      >
        {/* Checkbox — folders only */}
        {resource.isCollection ? (
          <button
            style={s.checkbox}
            onClick={() => onToggleSelect(node)}
            title={selState === "all" ? "Deselect" : "Select"}
          >
            <CheckboxIcon state={selState} />
          </button>
        ) : (
          <span style={{ ...s.checkbox, width: 15, flexShrink: 0 }} />
        )}

        {/* Expand toggle (collections only) */}
        {resource.isCollection ? (
          <button
            style={s.expandBtn}
            onClick={() => onToggleExpand(node)}
          >
            {node.loading ? (
              <div style={{ ...s.spinner, width: 12, height: 12, borderWidth: 1.5, margin: 0 }} />
            ) : isOpen ? (
              <ChevronDown size={13} strokeWidth={1.5} />
            ) : (
              <ChevronRight size={13} strokeWidth={1.5} />
            )}
          </button>
        ) : (
          <span style={s.expandPlaceholder} />
        )}

        {/* Icon */}
        {resource.isCollection ? (
          <Folder size={14} strokeWidth={1.5} style={{ color: "var(--primary)", flexShrink: 0 }} />
        ) : (
          <File size={14} strokeWidth={1.5} style={{ color: "var(--outline)", flexShrink: 0 }} />
        )}

        {/* Name */}
        <span style={s.rowName}>{resource.name}</span>

        {/* Size */}
        <span style={s.rowSize}>
          {formatBytes(subtreeSize(node))}
        </span>

        {/* Status badge — folders only */}
        <span style={s.rowStatus}>
          {resource.isCollection && selState === "all" && <span className="chip chip-success">SELECTED</span>}
          {resource.isCollection && selState === "partial" && <span className="chip chip-warning">PARTIAL</span>}
        </span>
      </div>

      {/* Children */}
      {isOpen && node.children && node.children.map((child) => (
        <TreeRow
          key={child.resource.href}
          node={child}
          depth={depth + 1}
          expanded={expanded}
          selected={selected}
          onToggleExpand={onToggleExpand}
          onToggleSelect={onToggleSelect}
        />
      ))}

      {isOpen && node.error && (
        <div style={{ paddingLeft: `${1.5 + (depth + 1) * 1.25}rem`, padding: "0.375rem 0.75rem", color: "var(--error)", fontSize: "0.75rem" }}>
          {node.error}
        </div>
      )}
    </>
  );
}

// ── CheckboxIcon ───────────────────────────────────────────────────────────────

function CheckboxIcon({ state }: { state: SelectionState }) {
  const base: React.CSSProperties = {
    width: 15,
    height: 15,
    borderRadius: 3,
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    flexShrink: 0,
    transition: "all var(--transition-fast)",
  };

  if (state === "all") {
    return (
      <span style={{ ...base, background: "var(--primary-container)", border: "none" }}>
        <Check size={10} strokeWidth={3} style={{ color: "#fff" }} />
      </span>
    );
  }
  if (state === "partial") {
    return (
      <span style={{ ...base, background: "rgba(180,197,255,0.15)", border: "1.5px solid var(--primary)" }}>
        <Minus size={10} strokeWidth={3} style={{ color: "var(--primary)" }} />
      </span>
    );
  }
  return (
    <span style={{ ...base, background: "transparent", border: "1.5px solid var(--outline-variant)" }} />
  );
}

// ── Tree update helper ─────────────────────────────────────────────────────────

/** Immutably update a single node anywhere in the tree rooted at `root`. */
function updateInTree(root: TreeNode, href: string, fn: (n: TreeNode) => TreeNode): TreeNode {
  if (root.resource.href === href) return fn(root);
  if (root.children) {
    return {
      ...root,
      children: root.children.map((n) => updateInTree(n, href, fn)),
    };
  }
  return root;
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
    gap: "0.4rem",
    height: 36,
    paddingRight: "0.75rem",
    cursor: "default",
    transition: "background var(--transition-fast)",
  },
  checkbox: {
    background: "transparent",
    border: "none",
    padding: 0,
    cursor: "pointer",
    display: "flex",
    alignItems: "center",
    flexShrink: 0,
    borderRadius: 3,
  },
  expandBtn: {
    background: "transparent",
    border: "none",
    padding: 0,
    cursor: "pointer",
    display: "flex",
    alignItems: "center",
    color: "var(--on-surface-variant)",
    flexShrink: 0,
    width: 16,
  },
  expandPlaceholder: {
    display: "inline-block",
    width: 16,
    flexShrink: 0,
  },
  rowName: {
    flex: 1,
    fontSize: "0.8125rem",
    color: "var(--on-surface)",
    whiteSpace: "nowrap" as const,
    overflow: "hidden",
    textOverflow: "ellipsis",
    minWidth: 0,
  },
  rowSize: {
    width: 80,
    fontSize: "0.75rem",
    color: "var(--outline)",
    textAlign: "right" as const,
    flexShrink: 0,
  },
  rowStatus: {
    width: 100,
    display: "flex",
    justifyContent: "flex-end",
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
    gap: "0.625rem",
    marginBottom: "0.75rem",
  },
  summaryValue: {
    fontSize: "1.25rem",
    fontWeight: 300,
    color: "var(--on-surface)",
    letterSpacing: "-0.02em",
    lineHeight: 1.2,
  },
  summaryMeta: {
    fontSize: "0.6875rem",
    color: "var(--outline)",
    marginTop: "0.125rem",
  },
  selectedList: {
    display: "flex",
    flexDirection: "column",
    gap: "0.3rem",
    borderTop: "1px solid rgba(68,71,90,0.15)",
    paddingTop: "0.625rem",
  },
  selectedItem: {
    display: "flex",
    alignItems: "center",
    gap: "0.375rem",
    overflow: "hidden",
  },
  selectedItemName: {
    fontSize: "0.6875rem",
    color: "var(--on-surface-variant)",
    whiteSpace: "nowrap" as const,
    overflow: "hidden",
    textOverflow: "ellipsis",
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
  actions: {
    display: "flex",
    flexDirection: "column",
    gap: "0.5rem",
    marginTop: "auto",
  },
};
