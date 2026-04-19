import { LayoutDashboard, FolderSync, Settings, Plus } from "lucide-react";
import type { NavPage } from "../types";

interface SidebarProps {
  page: NavPage;
  onNavigate: (p: NavPage) => void;
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

export function Sidebar({ page, onNavigate, onAddFolder }: SidebarProps) {
  return (
    <aside style={styles.sidebar}>
      {/* Brand */}
      <div style={styles.brand}>
        <img src="/cernbox-lrg.svg" alt="CERNBox" style={styles.brandLogo} />
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

      {/* Add sync folder */}
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
    flexDirection: "column",
    alignItems: "flex-start",
    gap: "0.375rem",
    marginBottom: "1.5rem",
  },
  brandLogo: {
    width: "65%",
    marginLeft: "0.25rem",
    height: "auto",
  },
  brandSub: {
    fontSize: "0.6875rem",
    color: "var(--outline)",
    display: "flex",
    alignItems: "center",
    gap: "0.3125rem",
    padding: "0 0.625rem",
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
