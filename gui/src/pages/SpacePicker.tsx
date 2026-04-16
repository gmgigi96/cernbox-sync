import { useState, useEffect } from "react";
import { ArrowLeft, Grid3X3, Search, ChevronLeft, ChevronRight } from "lucide-react";
import { listSpaces } from "../graph";
import type { Space } from "../types";

interface SpacePickerProps {
  serverUrl: string;
  username: string;
  password: string;
  onBack: () => void;
  onSelectSpace: (space: Space) => void;
}

type Filter = "all" | "personal" | "project";

const PAGE_SIZE = 12;

export function SpacePicker({ serverUrl, username, password, onBack, onSelectSpace }: SpacePickerProps) {
  const [spaces, setSpaces] = useState<Space[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState<Filter>("all");
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(1);

  function load() {
    setLoading(true);
    setError(null);
    listSpaces(serverUrl, username, password)
      .then(setSpaces)
      .catch((e) => setError(String(e)))
      .finally(() => setLoading(false));
  }

  useEffect(() => { load(); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const filtered = spaces.filter((s) => {
    if (filter === "personal" && s.drive_type !== "personal") return false;
    if (filter === "project" && s.drive_type !== "project") return false;
    if (search && !s.name.toLowerCase().includes(search.toLowerCase())) return false;
    return true;
  });

  const totalPages = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE));
  const currentPage = Math.min(page, totalPages);
  const pageItems = filtered.slice((currentPage - 1) * PAGE_SIZE, currentPage * PAGE_SIZE);

  function handleFilterChange(f: Filter) {
    setFilter(f);
    setPage(1);
  }

  return (
    <div style={styles.root}>
      {/* Header */}
      <div style={styles.header}>
        <button className="btn-icon" style={styles.backBtn} onClick={onBack}>
          <ArrowLeft size={16} strokeWidth={1.5} />
        </button>
        <div style={styles.headerText}>
          <h1 style={styles.title}>Spaces</h1>
          <p style={styles.subtitle}>Choose a space to synchronize with your local folder.</p>
        </div>
        <div style={styles.headerRight}>
          <div style={styles.searchWrap}>
            <Search size={13} strokeWidth={1.5} style={styles.searchIcon} />
            <input
              style={styles.searchInput}
              placeholder="Search spaces…"
              value={search}
              onChange={(e) => { setSearch(e.target.value); setPage(1); }}
            />
          </div>
          <div style={styles.filterBar}>
            {(["all", "personal", "project"] as Filter[]).map((f) => (
              <button
                key={f}
                style={{ ...styles.filterBtn, ...(filter === f ? styles.filterBtnActive : {}) }}
                onClick={() => handleFilterChange(f)}
              >
                {f.toUpperCase()}
              </button>
            ))}
          </div>
        </div>
      </div>

      {/* Content */}
      <div style={styles.content}>
        {loading && (
          <div style={styles.center}>
            <div style={styles.spinner} />
            <p style={styles.loadingText}>Loading spaces…</p>
          </div>
        )}

        {!loading && error && (
          <div style={styles.center}>
            <p style={{ color: "var(--error)", fontSize: "0.875rem", marginBottom: "0.75rem", textAlign: "center" as const }}>{error}</p>
            <button className="btn-secondary" onClick={load}>Retry</button>
          </div>
        )}

        {!loading && !error && filtered.length === 0 && (
          <div style={styles.center}>
            <Grid3X3 size={36} strokeWidth={1} style={{ color: "var(--outline)", marginBottom: "0.75rem" }} />
            <p style={{ color: "var(--on-surface-variant)", fontSize: "0.875rem" }}>No spaces found.</p>
          </div>
        )}

        {!loading && !error && pageItems.length > 0 && (
          <div style={styles.grid}>
            {pageItems.map((space) => (
              <SpaceCard key={space.id} space={space} onClick={() => onSelectSpace(space)} />
            ))}
          </div>
        )}
      </div>

      {/* Pagination */}
      {!loading && !error && filtered.length > 0 && (
        <div style={styles.pagination}>
          <span style={styles.paginationInfo}>
            DISPLAYING {Math.min((currentPage - 1) * PAGE_SIZE + 1, filtered.length)}–{Math.min(currentPage * PAGE_SIZE, filtered.length)} OF {filtered.length} AVAILABLE SPACES
          </span>
          <div style={styles.paginationControls}>
            <button className="btn-icon" onClick={() => setPage((p) => Math.max(1, p - 1))} disabled={currentPage === 1}>
              <ChevronLeft size={14} strokeWidth={1.5} />
            </button>
            {Array.from({ length: totalPages }, (_, i) => i + 1).map((p) => (
              <button
                key={p}
                style={{ ...styles.pageBtn, ...(p === currentPage ? styles.pageBtnActive : {}) }}
                onClick={() => setPage(p)}
              >
                {p}
              </button>
            ))}
            <button className="btn-icon" onClick={() => setPage((p) => Math.min(totalPages, p + 1))} disabled={currentPage === totalPages}>
              <ChevronRight size={14} strokeWidth={1.5} />
            </button>
          </div>
        </div>
      )}

      <style>{`@keyframes spin { to { transform: rotate(360deg); } }`}</style>
    </div>
  );
}

// ── SpaceCard ──────────────────────────────────────────────────────────────────

function SpaceCard({ space, onClick }: { space: Space; onClick: () => void }) {
  const [hovered, setHovered] = useState(false);
  const isPersonal = space.drive_type === "personal";
  const gradient = isPersonal
    ? "linear-gradient(135deg, #1A4FBF 0%, #4A8FE7 100%)"
    : "linear-gradient(135deg, #2A1F4A 0%, #4A3A6A 100%)";

  return (
    <div
      style={{ ...styles.card, ...(hovered ? styles.cardHovered : {}) }}
      onClick={onClick}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
    >
      <div style={{ ...styles.thumbnail, background: gradient }}>
        <Grid3X3 size={24} strokeWidth={1} style={{ color: "rgba(255,255,255,0.45)" }} />
        {isPersonal && <span style={styles.personalBadge}>PERSONAL</span>}
      </div>
      <div style={styles.cardBody}>
        <div style={styles.cardInfo}>
          <p style={styles.spaceName}>{space.name}</p>
          {space.description && (
            <p style={styles.spaceDesc}>{space.description}</p>
          )}
        </div>
      </div>
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
    overflow: "hidden",
  },
  header: {
    display: "flex",
    alignItems: "flex-start",
    gap: "0.875rem",
    flexWrap: "wrap" as const,
  },
  backBtn: { marginTop: "0.125rem", flexShrink: 0 },
  headerText: { flex: 1, minWidth: 0 },
  title: {
    fontSize: "1.375rem",
    fontWeight: 300,
    letterSpacing: "-0.02em",
    color: "var(--on-surface)",
    marginBottom: "0.25rem",
  },
  subtitle: { fontSize: "0.8125rem", color: "var(--on-surface-variant)" },
  headerRight: {
    display: "flex",
    alignItems: "center",
    gap: "0.625rem",
    flexShrink: 0,
  },
  searchWrap: { position: "relative" as const, display: "flex", alignItems: "center" },
  searchIcon: {
    position: "absolute" as const,
    left: "0.625rem",
    color: "var(--outline)",
    pointerEvents: "none" as const,
  },
  searchInput: {
    width: 180,
    background: "var(--surface-container-high)",
    border: "1px solid rgba(68,71,90,0.15)",
    borderRadius: "var(--radius-md)",
    color: "var(--on-surface)",
    fontSize: "0.8125rem",
    padding: "0.4rem 0.75rem 0.4rem 2rem",
  },
  filterBar: {
    display: "flex",
    gap: "0.25rem",
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-md)",
    padding: "0.25rem",
  },
  filterBtn: {
    fontSize: "0.6875rem",
    fontWeight: 600,
    letterSpacing: "0.05em",
    padding: "0.3125rem 0.625rem",
    borderRadius: "var(--radius-sm)",
    background: "transparent",
    color: "var(--outline)",
    border: "none",
    cursor: "pointer",
    transition: "all var(--transition-fast)",
    fontFamily: "var(--font-family)",
  },
  filterBtnActive: {
    background: "var(--surface-container-highest)",
    color: "var(--on-surface)",
  },
  content: { flex: 1, overflow: "auto" },
  center: {
    height: "100%",
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    justifyContent: "center",
    gap: "0.5rem",
  },
  spinner: {
    width: 28,
    height: 28,
    border: "2px solid var(--surface-container-highest)",
    borderTopColor: "var(--primary)",
    borderRadius: "50%",
    animation: "spin 0.8s linear infinite",
    marginBottom: "0.5rem",
  },
  loadingText: { fontSize: "0.8125rem", color: "var(--on-surface-variant)" },
  grid: {
    display: "grid",
    gridTemplateColumns: "repeat(auto-fill, minmax(160px, 1fr))",
    gap: "0.875rem",
  },
  card: {
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-xl)",
    overflow: "hidden",
    cursor: "pointer",
    transition: "background var(--transition-base), transform var(--transition-base)",
    display: "flex",
    flexDirection: "column",
  },
  cardHovered: {
    background: "var(--surface-container-highest)",
    transform: "translateY(-2px)",
  },
  thumbnail: {
    height: 90,
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    position: "relative" as const,
  },
  personalBadge: {
    position: "absolute" as const,
    bottom: "0.5rem",
    right: "0.5rem",
    fontSize: "0.5625rem",
    fontWeight: 700,
    letterSpacing: "0.06em",
    background: "rgba(180,197,255,0.2)",
    color: "var(--primary)",
    padding: "0.125rem 0.375rem",
    borderRadius: "var(--radius-full)",
  },
  cardBody: { padding: "0.75rem" },
  cardInfo: { minWidth: 0 },
  spaceName: {
    fontSize: "0.8125rem",
    fontWeight: 600,
    color: "var(--on-surface)",
    whiteSpace: "nowrap" as const,
    overflow: "hidden",
    textOverflow: "ellipsis",
    marginBottom: "0.125rem",
  },
  spaceDesc: {
    fontSize: "0.6875rem",
    color: "var(--outline)",
    whiteSpace: "nowrap" as const,
    overflow: "hidden",
    textOverflow: "ellipsis",
  },
  pagination: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    paddingTop: "0.5rem",
    borderTop: "1px solid rgba(68,71,90,0.12)",
  },
  paginationInfo: {
    fontSize: "0.625rem",
    fontWeight: 600,
    letterSpacing: "0.06em",
    color: "var(--outline)",
  },
  paginationControls: { display: "flex", alignItems: "center", gap: "0.25rem" },
  pageBtn: {
    width: 28,
    height: 28,
    borderRadius: "var(--radius-md)",
    background: "transparent",
    color: "var(--on-surface-variant)",
    fontSize: "0.75rem",
    fontWeight: 500,
    border: "none",
    cursor: "pointer",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    transition: "all var(--transition-fast)",
    fontFamily: "var(--font-family)",
  },
  pageBtnActive: {
    background: "var(--gradient-primary)",
    color: "#FFFFFF",
  },
};
