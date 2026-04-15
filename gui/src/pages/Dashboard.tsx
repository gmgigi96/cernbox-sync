import { RefreshCw, PauseCircle, FolderOpen, HardDrive, Wifi, WifiOff, Clock, AlertCircle, CheckCircle2, ArrowUpRight } from "lucide-react";
import type { DaemonState } from "../hooks/useDaemon";

interface DashboardProps {
  daemon: DaemonState;
  onNavigate: (p: "folders") => void;
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

function mostRecentSync(lastSync: Record<string, string>): string | null {
  const times = Object.values(lastSync);
  if (!times.length) return null;
  return times.reduce((a, b) => (new Date(a) > new Date(b) ? a : b));
}

export function Dashboard({ daemon, onNavigate }: DashboardProps) {
  const { folders, status, daemonOnline, loading, syncFolder } = daemon;

  const syncingCount = status?.syncing.length ?? 0;
  const totalFolders = folders.length;
  const lastSyncTs = status ? mostRecentSync(status.last_sync) : null;

  const systemReady = daemonOnline && syncingCount === 0;

  if (loading) {
    return (
      <div style={styles.root}>
        <div style={styles.loadingWrap}>
          <RefreshCw size={24} strokeWidth={1.5} style={{ animation: "spin 1s linear infinite", color: "var(--primary)" }} />
          <span style={{ color: "var(--on-surface-variant)", marginTop: "0.75rem" }}>Connecting to daemon…</span>
        </div>
        <style>{`@keyframes spin { to { transform: rotate(360deg); } }`}</style>
      </div>
    );
  }

  return (
    <div style={styles.root}>
      {/* Header strip */}
      <div style={styles.header}>
        <div style={styles.headerLeft}>
          <div style={styles.statusBadge}>
            {daemonOnline ? (
              <><Wifi size={12} strokeWidth={1.5} style={{ color: "var(--success)" }} /> SYSTEM READY</>
            ) : (
              <><WifiOff size={12} strokeWidth={1.5} style={{ color: "var(--error)" }} /> DAEMON OFFLINE</>
            )}
          </div>
          <h1 style={styles.headline}>
            {syncingCount > 0
              ? `Syncing ${syncingCount} folder${syncingCount > 1 ? "s" : ""}…`
              : daemonOnline
              ? "All files are up to date"
              : "Cannot reach daemon"}
          </h1>
          {lastSyncTs && (
            <p style={styles.subtext}>
              <Clock size={12} strokeWidth={1.5} style={{ marginRight: "0.3rem", verticalAlign: "middle" }} />
              Last synced {formatRelative(lastSyncTs)}
            </p>
          )}
          <div style={styles.ctaRow}>
            <button className="btn-secondary" style={styles.ctaBtn} onClick={() => onNavigate("folders")}>
              <FolderOpen size={14} strokeWidth={1.5} />
              Open CERNBox Folder
            </button>
            <button
              className="btn-ghost"
              style={styles.ctaBtn}
              onClick={() => syncFolder()}
              disabled={!daemonOnline}
            >
              {syncingCount > 0 ? (
                <><PauseCircle size={14} strokeWidth={1.5} /> Syncing…</>
              ) : (
                <><RefreshCw size={14} strokeWidth={1.5} /> Sync Now</>
              )}
            </button>
          </div>
        </div>

        {/* Storage efficiency donut */}
        <div style={styles.storageCard}>
          <div style={styles.storageTitle}>STORAGE EFFICIENCY</div>
          <div style={styles.donutWrap}>
            <svg width="110" height="110" viewBox="0 0 110 110">
              <circle cx="55" cy="55" r="46" fill="none" stroke="var(--surface-container-highest)" strokeWidth="10" />
              <circle
                cx="55" cy="55" r="46"
                fill="none"
                stroke="url(#grad)"
                strokeWidth="10"
                strokeDasharray={`${2 * Math.PI * 46 * 0.78} ${2 * Math.PI * 46}`}
                strokeLinecap="round"
                transform="rotate(-90 55 55)"
              />
              <defs>
                <linearGradient id="grad" x1="0%" y1="0%" x2="100%" y2="0%">
                  <stop offset="0%" stopColor="var(--primary)" />
                  <stop offset="100%" stopColor="var(--primary-container)" />
                </linearGradient>
              </defs>
              <text x="55" y="50" textAnchor="middle" fill="var(--on-surface)" fontSize="14" fontWeight="600" fontFamily="Inter">1.4</text>
              <text x="55" y="64" textAnchor="middle" fill="var(--outline)" fontSize="10" fontFamily="Inter">TB used</text>
            </svg>
          </div>
          <div style={styles.storageRows}>
            {[
              { label: "Projects", value: "650 GB", color: "var(--primary)" },
              { label: "Photos",   value: "420 GB", color: "var(--primary-container)" },
              { label: "Documents",value: "330 GB", color: "var(--outline)" },
            ].map(({ label, value, color }) => (
              <div key={label} style={styles.storageRow}>
                <span style={{ ...styles.storageDot, background: color }} />
                <span style={{ color: "var(--on-surface-variant)", fontSize: "0.75rem" }}>{label}</span>
                <span style={{ color: "var(--outline)", fontSize: "0.75rem", marginLeft: "auto" }}>{value}</span>
              </div>
            ))}
          </div>
        </div>
      </div>

      {/* Lower two-column layout */}
      <div style={styles.lower}>
        {/* Activity Feed */}
        <div style={styles.activityCard}>
          <div style={styles.sectionHeader}>
            <span className="title-md">Activity Feed</span>
            <button className="btn-ghost" style={{ fontSize: "0.75rem", padding: "0.25rem 0.5rem" }}>
              Mark all read <ArrowUpRight size={11} strokeWidth={1.5} />
            </button>
          </div>

          <div className="list-group" style={{ marginTop: "1rem" }}>
            {totalFolders === 0 && !loading ? (
              <EmptyActivity />
            ) : (
              <>
                {status?.syncing.map((name) => (
                  <ActivityItem
                    key={name}
                    icon="sync"
                    title={`Syncing "${name}"`}
                    time="now"
                    unread
                  />
                ))}
                {Object.entries(status?.last_sync ?? {}).map(([name, ts]) => (
                  <ActivityItem
                    key={name}
                    icon="check"
                    title={`"${name}" synced successfully`}
                    time={formatRelative(ts)}
                  />
                ))}
                {totalFolders === 0 && (
                  <ActivityItem
                    icon="info"
                    title="No sync folders configured"
                    time=""
                  />
                )}
              </>
            )}
          </div>
        </div>

        {/* Diagnostic Tools */}
        <div style={styles.diagCard}>
          <div style={styles.sectionHeader}>
            <span className="title-md">Diagnostic Tools</span>
          </div>
          <div style={{ display: "flex", flexDirection: "column", gap: "0.625rem", marginTop: "1rem" }}>
            <DiagTool
              icon={<HardDrive size={16} strokeWidth={1.5} />}
              label="View Logs"
              sub="Per-folder sync history"
            />
            <DiagTool
              icon={<RefreshCw size={16} strokeWidth={1.5} />}
              label="Force Resync"
              sub="Rebuild state from scratch"
              onClick={() => syncFolder()}
              disabled={!daemonOnline}
            />
            <DiagTool
              icon={daemonOnline
                ? <CheckCircle2 size={16} strokeWidth={1.5} style={{ color: "var(--success)" }} />
                : <AlertCircle size={16} strokeWidth={1.5} style={{ color: "var(--error)" }} />}
              label={daemonOnline ? "Daemon Active" : "Daemon Offline"}
              sub={daemonOnline ? `${totalFolders} folder${totalFolders !== 1 ? "s" : ""} registered` : "Start cernbox-syncd"}
            />
          </div>
        </div>
      </div>

      {/* Stats bar */}
      <div style={styles.statsBar}>
        <Stat label="Total Folders" value={String(totalFolders)} />
        <Stat label="Currently Syncing" value={String(syncingCount)} highlight={syncingCount > 0} />
        <Stat label="Daemon Status" value={daemonOnline ? "Online" : "Offline"} highlight={!daemonOnline} ok={daemonOnline} />
        <Stat label="System" value={systemReady ? "Ready" : "Busy"} ok={systemReady} />
      </div>
    </div>
  );
}

// ── Sub-components ─────────────────────────────────────────────────────────────

function ActivityItem({ icon, title, time, unread }: { icon: string; title: string; time: string; unread?: boolean }) {
  const iconEl = icon === "sync"
    ? <RefreshCw size={14} strokeWidth={1.5} style={{ color: "var(--primary)", animation: "spin 1.5s linear infinite" }} />
    : icon === "check"
    ? <CheckCircle2 size={14} strokeWidth={1.5} style={{ color: "var(--success)" }} />
    : <AlertCircle size={14} strokeWidth={1.5} style={{ color: "var(--tertiary)" }} />;

  return (
    <div style={styles.activityItem}>
      <div style={styles.activityIconWrap}>{iconEl}</div>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontSize: "0.8125rem", color: unread ? "var(--on-surface)" : "var(--on-surface-variant)", fontWeight: unread ? 500 : 400, whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }}>
          {title}
        </div>
        {time && <div className="label-sm" style={{ marginTop: "0.125rem" }}>{time}</div>}
      </div>
      {unread && <div style={styles.unreadDot} />}
    </div>
  );
}

function DiagTool({ icon, label, sub, onClick, disabled }: { icon: React.ReactNode; label: string; sub: string; onClick?: () => void; disabled?: boolean }) {
  return (
    <button
      style={{ ...styles.diagTool, ...(disabled ? { opacity: 0.4, cursor: "not-allowed" } : {}) }}
      onClick={disabled ? undefined : onClick}
    >
      <div style={styles.diagIcon}>{icon}</div>
      <div style={{ flex: 1 }}>
        <div style={{ fontSize: "0.8125rem", color: "var(--on-surface)", fontWeight: 500 }}>{label}</div>
        <div className="label-sm">{sub}</div>
      </div>
      <ArrowUpRight size={13} strokeWidth={1.5} style={{ color: "var(--outline)", flexShrink: 0 }} />
    </button>
  );
}

function Stat({ label, value, highlight, ok }: { label: string; value: string; highlight?: boolean; ok?: boolean }) {
  const color = highlight
    ? "var(--tertiary)"
    : ok === false
    ? "var(--error)"
    : ok === true
    ? "var(--success)"
    : "var(--on-surface)";
  return (
    <div style={styles.stat}>
      <div style={{ ...styles.statValue, color }}>{value}</div>
      <div className="label-sm">{label}</div>
    </div>
  );
}

function EmptyActivity() {
  return (
    <div style={{ textAlign: "center", padding: "2rem 0", color: "var(--outline)" }}>
      <CheckCircle2 size={28} strokeWidth={1} style={{ marginBottom: "0.5rem", opacity: 0.4 }} />
      <div style={{ fontSize: "0.8125rem" }}>No recent activity</div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    flex: 1,
    display: "flex",
    flexDirection: "column",
    gap: "1rem",
    padding: "1.5rem",
    overflow: "hidden auto",
  },
  loadingWrap: {
    flex: 1,
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    justifyContent: "center",
    gap: "0.5rem",
  },
  header: {
    display: "flex",
    gap: "1.25rem",
    alignItems: "flex-start",
  },
  headerLeft: {
    flex: 1,
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-xl)",
    padding: "1.5rem",
    display: "flex",
    flexDirection: "column",
    gap: "0.625rem",
  },
  statusBadge: {
    display: "inline-flex",
    alignItems: "center",
    gap: "0.375rem",
    fontSize: "0.6875rem",
    fontWeight: 600,
    letterSpacing: "0.07em",
    color: "var(--outline)",
    textTransform: "uppercase" as const,
    marginBottom: "0.25rem",
  },
  headline: {
    fontSize: "1.625rem",
    fontWeight: 300,
    letterSpacing: "-0.02em",
    color: "var(--on-surface)",
    lineHeight: 1.2,
  },
  subtext: {
    fontSize: "0.8125rem",
    color: "var(--outline)",
    display: "flex",
    alignItems: "center",
  },
  ctaRow: {
    display: "flex",
    gap: "0.625rem",
    marginTop: "0.5rem",
  },
  ctaBtn: {
    fontSize: "0.8125rem",
    display: "flex",
    alignItems: "center",
    gap: "0.375rem",
  },
  storageCard: {
    width: 220,
    minWidth: 220,
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-xl)",
    padding: "1.25rem",
    display: "flex",
    flexDirection: "column",
  },
  storageTitle: {
    fontSize: "0.6875rem",
    fontWeight: 600,
    letterSpacing: "0.07em",
    color: "var(--outline)",
    textTransform: "uppercase" as const,
    marginBottom: "0.75rem",
  },
  donutWrap: {
    display: "flex",
    justifyContent: "center",
    marginBottom: "0.75rem",
  },
  storageRows: {
    display: "flex",
    flexDirection: "column",
    gap: "0.375rem",
  },
  storageRow: {
    display: "flex",
    alignItems: "center",
    gap: "0.5rem",
  },
  storageDot: {
    width: 8,
    height: 8,
    borderRadius: "50%",
    flexShrink: 0,
  },
  lower: {
    display: "flex",
    gap: "1rem",
    flex: 1,
    minHeight: 0,
  },
  activityCard: {
    flex: 1,
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-xl)",
    padding: "1.25rem",
    overflow: "hidden auto",
  },
  diagCard: {
    width: 260,
    minWidth: 260,
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-xl)",
    padding: "1.25rem",
  },
  sectionHeader: {
    display: "flex",
    justifyContent: "space-between",
    alignItems: "center",
  },
  activityItem: {
    display: "flex",
    alignItems: "center",
    gap: "0.75rem",
    padding: "0.5rem 0.625rem",
    borderRadius: "var(--radius-md)",
    background: "transparent",
    transition: "background var(--transition-base)",
    cursor: "default",
  },
  activityIconWrap: {
    width: 28,
    height: 28,
    background: "var(--surface-container-highest)",
    borderRadius: "var(--radius-md)",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    flexShrink: 0,
  },
  unreadDot: {
    width: 6,
    height: 6,
    borderRadius: "50%",
    background: "var(--primary)",
    flexShrink: 0,
  },
  diagTool: {
    display: "flex",
    alignItems: "center",
    gap: "0.75rem",
    background: "var(--surface-container-highest)",
    borderRadius: "var(--radius-lg)",
    padding: "0.75rem",
    width: "100%",
    border: "none",
    cursor: "pointer",
    transition: "all var(--transition-base)",
    fontFamily: "var(--font-family)",
    textAlign: "left" as const,
  },
  diagIcon: {
    width: 32,
    height: 32,
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-md)",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    color: "var(--on-surface-variant)",
    flexShrink: 0,
  },
  statsBar: {
    display: "flex",
    gap: "1rem",
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-xl)",
    padding: "1rem 1.5rem",
  },
  stat: {
    flex: 1,
    display: "flex",
    flexDirection: "column",
    gap: "0.1875rem",
  },
  statValue: {
    fontSize: "1.375rem",
    fontWeight: 300,
    letterSpacing: "-0.02em",
  },
};
