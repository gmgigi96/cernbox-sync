import { LayoutDashboard, FolderSync, Settings, Activity, Plus } from "lucide-react";
import type { NavPage } from "../types";

interface SidebarProps {
  page: NavPage;
  onNavigate: (p: NavPage) => void;
  daemonOnline: boolean;
  onAddFolder: () => void;
}

interface NavItem {
  id: NavPage;
  label: string;
  icon: React.ElementType;
}

const NAV_ITEMS: NavItem[] = [
  { id: "dashboard", label: "Dashboard", icon: LayoutDashboard },
  { id: "folders",   label: "Syncing Folders", icon: FolderSync },
  { id: "settings",  label: "Settings", icon: Settings },
];

export function Sidebar({ page, onNavigate, daemonOnline, onAddFolder }: SidebarProps) {
  return (
    <aside style={styles.sidebar}>
      {/* Brand */}
      <div style={styles.brand}>
        <div style={styles.brandIcon}>
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor"
            strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
            <path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/>
            <polyline points="3.27 6.96 12 12.01 20.73 6.96"/>
            <line x1="12" y1="22.08" x2="12" y2="12"/>
          </svg>
        </div>
        <div>
          <div style={styles.brandName}>CERNBox</div>
          <div style={styles.brandSub}>
            <span style={{ ...styles.statusDot, background: daemonOnline ? "var(--success)" : "var(--error)" }} />
            {daemonOnline ? "Connected" : "Offline"}
          </div>
        </div>
      </div>

      {/* Navigation */}
      <nav style={styles.nav}>
        {NAV_ITEMS.map(({ id, label, icon: Icon }) => {
          const active = page === id;
          return (
            <button
              key={id}
              onClick={() => onNavigate(id)}
              style={{
                ...styles.navItem,
                ...(active ? styles.navItemActive : {}),
              }}
            >
              <Icon size={16} strokeWidth={1.5} />
              <span>{label}</span>
            </button>
          );
        })}
      </nav>

      <div style={styles.spacer} />

      {/* Activity hint */}
      <button style={styles.navItem}>
        <Activity size={16} strokeWidth={1.5} />
        <span>Activity</span>
      </button>

      {/* Add folder CTA */}
      <button className="btn-primary" style={styles.addBtn} onClick={onAddFolder}>
        <Plus size={15} strokeWidth={2} />
        Add Sync Folder
      </button>
    </aside>
  );
}

const styles: Record<string, React.CSSProperties> = {
  sidebar: {
    width: 220,
    minWidth: 220,
    background: "var(--surface-container-low)",
    display: "flex",
    flexDirection: "column",
    padding: "1.25rem 0.75rem",
    gap: "0.25rem",
    overflow: "hidden",
  },
  brand: {
    display: "flex",
    alignItems: "center",
    gap: "0.625rem",
    padding: "0 0.5rem",
    marginBottom: "1.5rem",
  },
  brandIcon: {
    width: 34,
    height: 34,
    background: "var(--gradient-primary)",
    borderRadius: "var(--radius-lg)",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    color: "var(--on-primary)",
    flexShrink: 0,
  },
  brandName: {
    fontSize: "0.875rem",
    fontWeight: 700,
    color: "var(--on-surface)",
    letterSpacing: "-0.01em",
  },
  brandSub: {
    fontSize: "0.6875rem",
    color: "var(--outline)",
    display: "flex",
    alignItems: "center",
    gap: "0.3125rem",
    marginTop: "0.0625rem",
  },
  statusDot: {
    width: 6,
    height: 6,
    borderRadius: "50%",
    display: "inline-block",
  },
  nav: {
    display: "flex",
    flexDirection: "column",
    gap: "0.125rem",
  },
  navItem: {
    display: "flex",
    alignItems: "center",
    gap: "0.625rem",
    padding: "0.5rem 0.625rem",
    borderRadius: "var(--radius-md)",
    background: "transparent",
    color: "var(--on-surface-variant)",
    fontSize: "0.8125rem",
    fontWeight: 400,
    cursor: "pointer",
    border: "none",
    width: "100%",
    transition: "all var(--transition-base)",
    fontFamily: "var(--font-family)",
  },
  navItemActive: {
    background: "var(--surface-container-highest)",
    color: "var(--on-surface)",
    fontWeight: 500,
  },
  spacer: { flex: 1 },
  addBtn: {
    marginTop: "0.75rem",
    width: "100%",
    justifyContent: "center",
    padding: "0.5625rem 1rem",
    fontSize: "0.8125rem",
  },
};
