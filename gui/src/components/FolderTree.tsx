import { useState } from "react";
import {
  Folder,
  File,
  ChevronRight,
  ChevronDown,
  Check,
  Minus,
} from "lucide-react";
import type { RemoteResource } from "../types";

// ── Types ──────────────────────────────────────────────────────────────────────

export interface TreeNode {
  resource: RemoteResource;
  children: TreeNode[] | null; // null = not yet loaded
  loading: boolean;
  error: string | null;
}

export type SelectionState = "none" | "partial" | "all";

// ── Helpers ────────────────────────────────────────────────────────────────────

export function formatBytes(bytes: number): string {
  if (bytes === 0) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = bytes;
  let u = 0;
  while (v >= 1024 && u < units.length - 1) { v /= 1024; u++; }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[u]}`;
}

export function subtreeSize(node: TreeNode): number {
  if (!node.resource.isCollection) return node.resource.size;
  if (node.resource.size > 0) return node.resource.size;
  return (node.children ?? []).reduce((acc, c) => acc + subtreeSize(c), 0);
}

/** Sort tree nodes so directories appear before files, both groups alphabetically. */
export function sortChildren(nodes: TreeNode[]): TreeNode[] {
  return [...nodes].sort((a, b) => {
    if (a.resource.isCollection !== b.resource.isCollection) return a.resource.isCollection ? -1 : 1;
    return a.resource.name.localeCompare(b.resource.name);
  });
}

/** Collect all collection hrefs in a subtree (including the root node itself). */
export function collectHrefs(node: TreeNode): string[] {
  if (!node.resource.isCollection) return [];
  const hrefs = [node.resource.href];
  if (node.children) for (const child of node.children) hrefs.push(...collectHrefs(child));
  return hrefs;
}

/**
 * Compute selection state based purely on what hrefs are literally in `selected`.
 * Used by collectSelectedUrls / save logic — must reflect the actual stored set.
 */
export function nodeSelectionState(node: TreeNode, selected: Set<string>): SelectionState {
  if (!node.resource.isCollection) return "none";
  if (selected.has(node.resource.href)) return "all";
  const href = node.resource.href;
  for (const s of selected) {
    if (href.startsWith(s) && href !== s) return "all";
  }
  const hrefs = collectHrefs(node);
  const count = hrefs.filter((h) => selected.has(h)).length;
  if (count === 0) return "none";
  if (count === hrefs.length) return "all";
  return "partial";
}

/**
 * Compute selection state for display and toggle logic.
 * Recursively checks children so that a parent whose loaded children are all
 * "all" is itself considered "all", even if its own href is not in `selected`.
 */
export function displaySelectionState(node: TreeNode, selected: Set<string>): SelectionState {
  if (!node.resource.isCollection) return "none";
  if (selected.has(node.resource.href)) return "all";
  const href = node.resource.href;
  for (const s of selected) {
    if (href.startsWith(s) && href !== s) return "all";
  }
  if (!node.children) {
    // Even without loaded children, if a selected href is a descendant show partial.
    for (const s of selected) {
      if (s.startsWith(href) && s !== href) return "partial";
    }
    return "none";
  }
  const collectionChildren = node.children.filter((c) => c.resource.isCollection);
  if (collectionChildren.length === 0) return "none";
  let allAll = true;
  let anySelected = false;
  for (const child of collectionChildren) {
    const cs = displaySelectionState(child, selected);
    if (cs !== "all") allAll = false;
    if (cs !== "none") anySelected = true;
  }
  if (allAll) return "all";
  if (anySelected) return "partial";
  return "none";
}

/**
 * Recursively collect the minimal set of URLs that represents the selection.
 */
export function collectSelectedUrls(node: TreeNode, selected: Set<string>): string[] {
  const state = nodeSelectionState(node, selected);
  if (state === "none") return [];
  if (state === "all") return [node.resource.href];
  const urls: string[] = [];
  for (const child of node.children ?? []) urls.push(...collectSelectedUrls(child, selected));
  return urls;
}

/**
 * Remove from `selected` any folder href whose subtree has become fully
 * deselected. Mutates `selected` in place.
 */
export function pruneEmptyAncestors(node: TreeNode, selected: Set<string>): boolean {
  if (!node.resource.isCollection) return false;
  const anyChild = (node.children ?? []).some((c) => pruneEmptyAncestors(c, selected));
  if (!anyChild && selected.has(node.resource.href)) {
    if (node.children !== null) { selected.delete(node.resource.href); return false; }
  }
  return anyChild || selected.has(node.resource.href);
}

/** Recursively collect the fully-selected nodes (for sidebar display and size). */
export function collectSelectedNodes(node: TreeNode, selected: Set<string>): TreeNode[] {
  const state = nodeSelectionState(node, selected);
  if (state === "none") return [];
  if (state === "all") return [node];
  const nodes: TreeNode[] = [];
  for (const child of node.children ?? []) nodes.push(...collectSelectedNodes(child, selected));
  return nodes;
}

/** Find a node by href anywhere in the tree. */
export function findNode(root: TreeNode, href: string): TreeNode | null {
  if (root.resource.href === href) return root;
  if (!root.children) return null;
  for (const child of root.children) {
    const found = findNode(child, href);
    if (found) return found;
  }
  return null;
}

/**
 * When a node is "all"-selected due to an ancestor covering it, deselecting it
 * requires expanding the ancestor's selection to all siblings, excluding the
 * path that leads to `excludeHref`.
 *
 * Mutates `selected` in place.
 */
export function expandSelectionExcluding(node: TreeNode, excludeHref: string, selected: Set<string>): void {
  selected.delete(node.resource.href);
  if (!node.children) return;
  for (const child of node.children) {
    if (!child.resource.isCollection) continue;
    if (child.resource.href === excludeHref) {
      // This is the node to exclude — skip it entirely.
    } else if (excludeHref.startsWith(child.resource.href)) {
      // This child is an ancestor of excludeHref — recurse into it.
      expandSelectionExcluding(child, excludeHref, selected);
    } else {
      // Unrelated sibling — keep it fully selected.
      selected.add(child.resource.href);
    }
  }
}

/** Immutably update a single node anywhere in the tree rooted at `root`. */
export function updateInTree(root: TreeNode, href: string, fn: (n: TreeNode) => TreeNode): TreeNode {
  if (root.resource.href === href) return fn(root);
  if (root.children) return { ...root, children: root.children.map((n) => updateInTree(n, href, fn)) };
  return root;
}

// ── TreeRow ────────────────────────────────────────────────────────────────────

export interface TreeRowProps {
  node: TreeNode;
  depth: number;
  expanded: Set<string>;
  selected: Set<string>;
  onToggleExpand: (node: TreeNode) => void;
  onToggleSelect: (node: TreeNode) => void;
  /** Show the size column. Default: true. */
  showSize?: boolean;
  /** Show SELECTED/PARTIAL status badges. Default: true. */
  showBadge?: boolean;
}

export function TreeRow({ node, depth, expanded, selected, onToggleExpand, onToggleSelect, showSize = true, showBadge = true }: TreeRowProps) {
  const [hovered, setHovered] = useState(false);
  const { resource } = node;
  const isOpen = expanded.has(resource.href);
  const isFile = !resource.isCollection;
  const selState = displaySelectionState(node, selected);

  return (
    <>
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: "0.4rem",
          height: 36,
          paddingLeft: `${0.75 + depth * 1.25}rem`,
          paddingRight: "0.75rem",
          background: !isFile && hovered ? "var(--surface-container-high)" : "transparent",
          opacity: isFile ? 0.45 : 1,
          cursor: isFile ? "default" : "pointer",
          transition: "background var(--transition-fast)",
        }}
        onMouseEnter={() => { if (!isFile) setHovered(true); }}
        onMouseLeave={() => setHovered(false)}
      >
        {/* Checkbox — folders only */}
        {resource.isCollection ? (
          <button
            style={{ background: "transparent", border: "none", padding: 0, cursor: "pointer", display: "flex", alignItems: "center", flexShrink: 0, borderRadius: 3 }}
            onClick={() => onToggleSelect(node)}
            title={selState === "all" ? "Deselect" : "Select"}
          >
            <CheckboxIcon state={selState} />
          </button>
        ) : (
          <span style={{ width: 15, flexShrink: 0 }} />
        )}

        {/* Expand toggle (collections only) */}
        {resource.isCollection ? (
          <button
            style={{ background: "transparent", border: "none", padding: 0, cursor: "pointer", display: "flex", alignItems: "center", color: "var(--on-surface-variant)", flexShrink: 0, width: 16 }}
            onClick={() => onToggleExpand(node)}
          >
            {node.loading ? (
              <div style={{ width: 12, height: 12, border: "1.5px solid var(--surface-container-highest)", borderTopColor: "var(--primary)", borderRadius: "50%", animation: "spin 0.8s linear infinite" }} />
            ) : isOpen ? (
              <ChevronDown size={13} strokeWidth={1.5} />
            ) : (
              <ChevronRight size={13} strokeWidth={1.5} />
            )}
          </button>
        ) : (
          <span style={{ display: "inline-block", width: 16, flexShrink: 0 }} />
        )}

        {/* Icon */}
        {resource.isCollection ? (
          <Folder size={14} strokeWidth={1.5} style={{ color: "var(--primary)", flexShrink: 0 }} />
        ) : (
          <File size={14} strokeWidth={1.5} style={{ color: "var(--outline)", flexShrink: 0 }} />
        )}

        {/* Name */}
        <span style={{ flex: 1, fontSize: "0.8125rem", color: "var(--on-surface)", whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis", minWidth: 0 }}>
          {resource.name}
        </span>

        {/* Size (optional column) */}
        {showSize && (
          <span style={{ width: 80, fontSize: "0.75rem", color: "var(--outline)", textAlign: "right", flexShrink: 0 }}>
            {formatBytes(subtreeSize(node))}
          </span>
        )}

        {/* Status badge — folders only */}
        {showBadge && (
          <span style={{ width: showSize ? 100 : undefined, display: "flex", justifyContent: "flex-end", flexShrink: 0 }}>
            {resource.isCollection && selState === "all" && <span className="chip chip-success">SELECTED</span>}
            {resource.isCollection && selState === "partial" && <span className="chip chip-warning">PARTIAL</span>}
          </span>
        )}
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
          showSize={showSize}
          showBadge={showBadge}
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

export function CheckboxIcon({ state }: { state: SelectionState }) {
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
