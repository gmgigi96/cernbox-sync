import { describe, it, expect } from "vitest";
import {
  formatBytes,
  subtreeSize,
  sortChildren,
  collectHrefs,
  nodeSelectionState,
  displaySelectionState,
  collectSelectedUrls,
  collectSelectedNodes,
  expandSelectionExcluding,
  findNode,
  updateInTree,
  pruneEmptyAncestors,
} from "../components/FolderTree";
import type { TreeNode } from "../components/FolderTree";

// ── Helpers ────────────────────────────────────────────────────────────────────

function makeDir(href: string, name: string, children: TreeNode[] | null = null): TreeNode {
  return {
    resource: { href, name, isCollection: true, size: 0, lastModified: "" },
    children,
    loading: false,
    error: null,
  };
}

function makeFile(href: string, name: string, size = 100): TreeNode {
  return {
    resource: { href, name, isCollection: false, size, lastModified: "Mon, 01 Jan 2024 00:00:00 GMT" },
    children: null,
    loading: false,
    error: null,
  };
}

// ── formatBytes ────────────────────────────────────────────────────────────────

describe("formatBytes", () => {
  it("returns — for 0", () => expect(formatBytes(0)).toBe("—"));
  it("formats bytes", () => expect(formatBytes(512)).toBe("512 B"));
  it("formats kilobytes", () => expect(formatBytes(1024)).toBe("1.0 KB"));
  it("formats megabytes", () => expect(formatBytes(1024 * 1024)).toBe("1.0 MB"));
  it("formats gigabytes", () => expect(formatBytes(1024 ** 3)).toBe("1.0 GB"));
  it("uses no decimal for values >= 10 in a unit", () => expect(formatBytes(10 * 1024)).toBe("10 KB"));
});

// ── subtreeSize ────────────────────────────────────────────────────────────────

describe("subtreeSize", () => {
  it("returns size directly for files", () => {
    expect(subtreeSize(makeFile("/f.txt", "f.txt", 250))).toBe(250);
  });

  it("returns resource.size for collections that have a non-zero size (server-reported)", () => {
    const dir: TreeNode = { ...makeDir("/dir/", "dir"), resource: { ...makeDir("/dir/", "dir").resource, size: 999 } };
    expect(subtreeSize(dir)).toBe(999);
  });

  it("sums children sizes when collection.size is 0", () => {
    const root = makeDir("/root/", "root", [
      makeFile("/root/a.txt", "a.txt", 100),
      makeFile("/root/b.txt", "b.txt", 200),
    ]);
    expect(subtreeSize(root)).toBe(300);
  });

  it("recursively sums nested children", () => {
    const root = makeDir("/root/", "root", [
      makeDir("/root/sub/", "sub", [
        makeFile("/root/sub/c.txt", "c.txt", 50),
      ]),
      makeFile("/root/d.txt", "d.txt", 150),
    ]);
    expect(subtreeSize(root)).toBe(200);
  });
});

// ── sortChildren ───────────────────────────────────────────────────────────────

describe("sortChildren", () => {
  it("puts directories before files", () => {
    const nodes = [
      makeFile("/f.txt", "f.txt"),
      makeDir("/d/", "d"),
    ];
    const sorted = sortChildren(nodes);
    expect(sorted[0].resource.isCollection).toBe(true);
    expect(sorted[1].resource.isCollection).toBe(false);
  });

  it("sorts alphabetically within each group", () => {
    const nodes = [
      makeDir("/c/", "Charlie"),
      makeFile("/b.txt", "Bob"),
      makeDir("/a/", "Alice"),
      makeFile("/d.txt", "Dave"),
    ];
    const sorted = sortChildren(nodes);
    expect(sorted[0].resource.name).toBe("Alice");
    expect(sorted[1].resource.name).toBe("Charlie");
    expect(sorted[2].resource.name).toBe("Bob");
    expect(sorted[3].resource.name).toBe("Dave");
  });

  it("does not mutate the original array", () => {
    const nodes = [makeFile("/b.txt", "b.txt"), makeDir("/a/", "a")];
    sortChildren(nodes);
    expect(nodes[0].resource.name).toBe("b.txt");
  });
});

// ── collectHrefs ──────────────────────────────────────────────────────────────

describe("collectHrefs", () => {
  it("returns empty array for files", () => {
    expect(collectHrefs(makeFile("/f.txt", "f.txt"))).toEqual([]);
  });

  it("returns own href for a dir with no children", () => {
    expect(collectHrefs(makeDir("/d/", "d", []))).toEqual(["/d/"]);
  });

  it("recursively collects collection hrefs", () => {
    const tree = makeDir("/root/", "root", [
      makeDir("/root/a/", "a", [makeDir("/root/a/b/", "b", [])]),
      makeFile("/root/f.txt", "f.txt"),
    ]);
    const hrefs = collectHrefs(tree);
    expect(hrefs).toContain("/root/");
    expect(hrefs).toContain("/root/a/");
    expect(hrefs).toContain("/root/a/b/");
    expect(hrefs).not.toContain("/root/f.txt");
  });
});

// ── nodeSelectionState ────────────────────────────────────────────────────────

describe("nodeSelectionState", () => {
  const root = makeDir("/root/", "root", [
    makeDir("/root/a/", "a", []),
    makeDir("/root/b/", "b", []),
  ]);

  it("returns none for files", () => {
    expect(nodeSelectionState(makeFile("/f.txt", "f.txt"), new Set())).toBe("none");
  });

  it("returns all when own href is selected", () => {
    expect(nodeSelectionState(root, new Set(["/root/"]))).toBe("all");
  });

  it("returns none when nothing selected", () => {
    expect(nodeSelectionState(root, new Set())).toBe("none");
  });

  it("returns partial when some children selected", () => {
    expect(nodeSelectionState(root, new Set(["/root/a/"]))).toBe("partial");
  });

  it("returns partial when children are selected but root href is not", () => {
    // nodeSelectionState counts literally-selected hrefs vs collectHrefs(root).
    // collectHrefs(root) = ["/root/"] only (children have no children so they
    // don't appear under root's own href list). Selecting child hrefs is still
    // "partial" — to get "all" you select the root href itself.
    expect(nodeSelectionState(root, new Set(["/root/a/", "/root/b/"]))).toBe("partial");
  });

  it("returns all when root href is directly selected", () => {
    expect(nodeSelectionState(root, new Set(["/root/"]))).toBe("all");
  });
});

// ── displaySelectionState ─────────────────────────────────────────────────────

describe("displaySelectionState", () => {
  const root = makeDir("/root/", "root", [
    makeDir("/root/a/", "a", [makeDir("/root/a/c/", "c", [])]),
    makeDir("/root/b/", "b", []),
  ]);

  it("returns none when nothing selected", () => {
    expect(displaySelectionState(root, new Set())).toBe("none");
  });

  it("returns all when own href is selected", () => {
    expect(displaySelectionState(root, new Set(["/root/"]))).toBe("all");
  });

  it("returns partial when only one branch selected", () => {
    expect(displaySelectionState(root, new Set(["/root/a/"]))).toBe("partial");
  });

  it("returns all when all collection children are all-selected", () => {
    expect(displaySelectionState(root, new Set(["/root/a/", "/root/b/"]))).toBe("all");
  });

  it("returns none for a file node", () => {
    expect(displaySelectionState(makeFile("/f.txt", "f.txt"), new Set(["/f.txt"]))).toBe("none");
  });
});

// ── collectSelectedUrls ───────────────────────────────────────────────────────

describe("collectSelectedUrls", () => {
  const root = makeDir("/root/", "root", [
    makeDir("/root/a/", "a", [makeDir("/root/a/c/", "c", [])]),
    makeDir("/root/b/", "b", []),
  ]);

  it("returns empty when nothing selected", () => {
    expect(collectSelectedUrls(root, new Set())).toEqual([]);
  });

  it("returns root href when root is all-selected", () => {
    expect(collectSelectedUrls(root, new Set(["/root/"]))).toEqual(["/root/"]);
  });

  it("returns individual hrefs for partial selection", () => {
    const urls = collectSelectedUrls(root, new Set(["/root/a/"]));
    expect(urls).toEqual(["/root/a/"]);
  });

  it("returns the deepest selected nodes when partial at multiple levels", () => {
    const urls = collectSelectedUrls(root, new Set(["/root/a/c/"]));
    expect(urls).toEqual(["/root/a/c/"]);
  });
});

// ── collectSelectedNodes ──────────────────────────────────────────────────────

describe("collectSelectedNodes", () => {
  const root = makeDir("/root/", "root", [
    makeDir("/root/a/", "a", []),
    makeDir("/root/b/", "b", []),
  ]);

  it("returns root node when root is all-selected", () => {
    const nodes = collectSelectedNodes(root, new Set(["/root/"]));
    expect(nodes).toHaveLength(1);
    expect(nodes[0].resource.href).toBe("/root/");
  });

  it("returns child nodes for partial selection", () => {
    const nodes = collectSelectedNodes(root, new Set(["/root/a/"]));
    expect(nodes).toHaveLength(1);
    expect(nodes[0].resource.href).toBe("/root/a/");
  });
});

// ── expandSelectionExcluding ─────────────────────────────────────────────────

describe("expandSelectionExcluding", () => {
  it("removes root and selects all siblings except the excluded node", () => {
    const root = makeDir("/root/", "root", [
      makeDir("/root/a/", "a", []),
      makeDir("/root/b/", "b", []),
      makeDir("/root/c/", "c", []),
    ]);
    const selected = new Set(["/root/"]);
    expandSelectionExcluding(root, "/root/b/", selected);
    expect(selected.has("/root/")).toBe(false);
    expect(selected.has("/root/a/")).toBe(true);
    expect(selected.has("/root/b/")).toBe(false);
    expect(selected.has("/root/c/")).toBe(true);
  });

  it("recurses into ancestor of excluded node", () => {
    const root = makeDir("/root/", "root", [
      makeDir("/root/a/", "a", [
        makeDir("/root/a/x/", "x", []),
        makeDir("/root/a/y/", "y", []),
      ]),
      makeDir("/root/b/", "b", []),
    ]);
    const selected = new Set(["/root/"]);
    expandSelectionExcluding(root, "/root/a/x/", selected);
    expect(selected.has("/root/")).toBe(false);
    expect(selected.has("/root/b/")).toBe(true);
    expect(selected.has("/root/a/")).toBe(false);
    expect(selected.has("/root/a/y/")).toBe(true);
    expect(selected.has("/root/a/x/")).toBe(false);
  });
});

// ── findNode ──────────────────────────────────────────────────────────────────

describe("findNode", () => {
  const tree = makeDir("/root/", "root", [
    makeDir("/root/a/", "a", [makeFile("/root/a/f.txt", "f.txt")]),
  ]);

  it("finds the root itself", () => {
    expect(findNode(tree, "/root/")?.resource.href).toBe("/root/");
  });

  it("finds a deep node", () => {
    expect(findNode(tree, "/root/a/f.txt")?.resource.name).toBe("f.txt");
  });

  it("returns null for unknown href", () => {
    expect(findNode(tree, "/nope/")).toBeNull();
  });
});

// ── updateInTree ──────────────────────────────────────────────────────────────

describe("updateInTree", () => {
  const tree = makeDir("/root/", "root", [
    makeDir("/root/a/", "a", []),
  ]);

  it("applies fn to the matching node", () => {
    const updated = updateInTree(tree, "/root/a/", (n) => ({ ...n, loading: true }));
    expect(findNode(updated, "/root/a/")?.loading).toBe(true);
  });

  it("returns a structurally equal node when href not found", () => {
    // updateInTree maps over children even when not matched, producing a new object.
    const updated = updateInTree(tree, "/nope/", (n) => ({ ...n, loading: true }));
    expect(updated).toStrictEqual(tree);
  });
});

// ── pruneEmptyAncestors ───────────────────────────────────────────────────────

describe("pruneEmptyAncestors", () => {
  it("removes a dir href from selected when its children are all deselected", () => {
    const tree = makeDir("/root/", "root", [
      makeDir("/root/a/", "a", []),
    ]);
    const selected = new Set(["/root/a/"]);
    selected.delete("/root/a/");
    pruneEmptyAncestors(tree, selected);
    expect(selected.has("/root/a/")).toBe(false);
  });

  it("keeps a dir href when its children list is null (not yet loaded)", () => {
    // pruneEmptyAncestors only deletes a dir's href when children !== null and
    // no descendant is selected. A node with children === null is treated as
    // opaque and its href is preserved.
    const tree = makeDir("/root/", "root", [
      makeDir("/root/a/", "a", null),
      makeDir("/root/b/", "b", null),
    ]);
    const selected = new Set(["/root/a/", "/root/b/"]);
    pruneEmptyAncestors(tree, selected);
    expect(selected.has("/root/a/")).toBe(true);
    expect(selected.has("/root/b/")).toBe(true);
  });
});
