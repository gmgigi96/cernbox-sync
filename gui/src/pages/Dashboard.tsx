import { useState, useEffect, useRef } from "react";
import {
  RefreshCw, PauseCircle, Wifi, WifiOff, Clock, AlertCircle, CheckCircle2,
  FolderOpen, ArrowUpDown, ArrowUp, ArrowDown, ChevronDown, ChevronRight,
  Activity, Layers, AlertTriangle, HardDrive,
} from "lucide-react";
import { openPath } from "@tauri-apps/plugin-opener";
import type { DaemonState } from "../hooks/useDaemon";
import type { Folder } from "../types";
import type { SyncProgress } from "../store/syncStore";

interface DashboardProps {
  daemon: DaemonState;
  onNavigate: (p: "folders") => void;
  onOpenFolder: (folder: Folder) => void;
}

// ── Helpers ────────────────────────────────────────────────────────────────────

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

function formatBytes(bytes: number): string {
  if (bytes === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.floor(Math.log(bytes) / Math.log(1024));
  return `${(bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

function formatSpeed(bps: number): string {
  return `${formatBytes(bps)}/s`;
}

function mostRecentSync(lastSync: Record<string, string>): string | null {
  const times = Object.values(lastSync);
  if (!times.length) return null;
  return times.reduce((a, b) => (new Date(a) > new Date(b) ? a : b));
}

// ── Real bandwidth sparkline history ──────────────────────────────────────────

const SPARKLINE_POINTS = 30;

function useBandwidthHistory(uploadBps: number, downloadBps: number) {
  const [history, setHistory] = useState<{ up: number[]; down: number[] }>(() => ({
    up: Array(SPARKLINE_POINTS).fill(0),
    down: Array(SPARKLINE_POINTS).fill(0),
  }));

  // Keep a ref so the stable interval closure always reads the latest values.
  const bpsRef = useRef({ uploadBps, downloadBps });
  bpsRef.current = { uploadBps, downloadBps };

  useEffect(() => {
    const id = setInterval(() => {
      const { uploadBps: up, downloadBps: down } = bpsRef.current;
      setHistory((prev) => ({
        up: [...prev.up.slice(1), up],
        down: [...prev.down.slice(1), down],
      }));
    }, 1000);
    return () => clearInterval(id);
  }, []); // stable — never recreated

  return history;
}

// ── Sparkline SVG ──────────────────────────────────────────────────────────────

function Sparkline({ values, color, height = 32 }: { values: number[]; color: string; height?: number }) {
  const svgRef = useRef<SVGSVGElement>(null);
  const [width, setWidth] = useState(200);

  useEffect(() => {
    const el = svgRef.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(el.getBoundingClientRect().width));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  if (!values.length) return null;
  const max = Math.max(...values, 1);
  const pts = values.map((v, i) => {
    const x = (i / (values.length - 1)) * width;
    const y = height - (v / max) * (height - 2) - 1;
    return `${x},${y}`;
  });
  const pathD = `M${pts.join(" L")}`;
  const areaD = `M${pts[0]} L${pts.join(" L")} L${width},${height} L0,${height} Z`;
  const gradId = `sg-${color.replace(/[^a-z]/gi, "")}`;
  return (
    <svg ref={svgRef} width="100%" height={height} style={{ display: "block" }}>
      <defs>
        <linearGradient id={gradId} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor={color} stopOpacity="0.25" />
          <stop offset="100%" stopColor={color} stopOpacity="0" />
        </linearGradient>
      </defs>
      <path d={areaD} fill={`url(#${gradId})`} />
      <path d={pathD} fill="none" stroke={color} strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

// ── Collapsible section wrapper ────────────────────────────────────────────────

function Section({ title, defaultOpen = true, children, badge }: {
  title: string;
  defaultOpen?: boolean;
  children: React.ReactNode;
  badge?: React.ReactNode;
}) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <div style={S.section}>
      <button style={S.sectionToggle} onClick={() => setOpen((o) => !o)}>
        <span style={S.sectionTitle}>{title}</span>
        {badge && <span style={{ marginLeft: "0.5rem" }}>{badge}</span>}
        <span style={{ marginLeft: "auto", color: "var(--outline)", display: "flex" }}>
          {open ? <ChevronDown size={14} strokeWidth={1.5} /> : <ChevronRight size={14} strokeWidth={1.5} />}
        </span>
      </button>
      {open && <div style={{ marginTop: "0.875rem" }}>{children}</div>}
    </div>
  );
}

// ── Main Dashboard ─────────────────────────────────────────────────────────────

export function Dashboard({ daemon, onNavigate, onOpenFolder }: DashboardProps) {
  const { folders, status, progress, uploadBps, downloadBps, daemonOnline, loading, syncFolder } = daemon;
  const syncing = status?.syncing ?? [];
  const syncingCount = syncing.length;
  const totalFolders = folders.length;
  const lastSyncTs = status ? mostRecentSync(status.last_sync) : null;
  const bw = useBandwidthHistory(uploadBps, downloadBps);

  const totalFiles = Object.values(status?.counts ?? {}).reduce((acc, c) => acc + c.files, 0);
  const totalSize = Object.values(status?.counts ?? {}).reduce((acc, c) => acc + c.size, 0);

  const conflictCount = 0; // placeholder — no conflict API yet

  if (loading) {
    return (
      <div style={S.root}>
        <div style={S.loadingWrap}>
          <RefreshCw size={24} strokeWidth={1.5} style={{ animation: "spin 1s linear infinite", color: "var(--primary)" }} />
          <span style={{ color: "var(--on-surface-variant)", marginTop: "0.75rem" }}>Connecting to daemon…</span>
        </div>
        <style>{spinKeyframe}</style>
      </div>
    );
  }

  return (
    <div style={S.root}>
      <style>{spinKeyframe}</style>

      {/* ── 1. Status hero ──────────────────────────────────────────────────── */}
      <Section title="Sync Status" defaultOpen>
        <div style={S.heroCard}>
          <div style={S.heroLeft}>
            <div style={S.statusBadge}>
              {daemonOnline
                ? <><Wifi size={11} strokeWidth={1.5} style={{ color: "var(--success)" }} /> SYSTEM READY</>
                : <><WifiOff size={11} strokeWidth={1.5} style={{ color: "var(--error)" }} /> DAEMON OFFLINE</>
              }
            </div>
            <h1 style={S.headline}>
              {syncingCount > 0
                ? `Syncing ${syncingCount} folder${syncingCount > 1 ? "s" : ""}…`
                : daemonOnline
                ? "All files are up to date"
                : "Cannot reach daemon"}
            </h1>
            {lastSyncTs && (
              <p style={S.subtext}>
                <Clock size={11} strokeWidth={1.5} />
                Last synced {formatRelative(lastSyncTs)}
              </p>
            )}
            <div style={S.ctaRow}>
              <button className="btn-secondary" style={S.ctaBtn} onClick={() => onNavigate("folders")}>
                <FolderOpen size={13} strokeWidth={1.5} /> Open Folders
              </button>
              <button className="btn-ghost" style={S.ctaBtn} onClick={() => syncFolder()} disabled={!daemonOnline}>
                {syncingCount > 0
                  ? <><PauseCircle size={13} strokeWidth={1.5} /> Syncing…</>
                  : <><RefreshCw size={13} strokeWidth={1.5} /> Sync All</>}
              </button>
            </div>
          </div>

          {/* Active transfers column */}
          <div style={S.transfersCol}>
            <div style={S.colLabel}>ACTIVE TRANSFERS</div>
            {syncingCount === 0 ? (
              <div style={S.noTransfers}>
                <CheckCircle2 size={18} strokeWidth={1} style={{ color: "var(--success)", opacity: 0.6 }} />
                <span style={{ fontSize: "0.75rem", color: "var(--outline)", marginTop: "0.25rem" }}>None</span>
              </div>
            ) : (
              <div style={S.transferList}>
                {syncing.map((name) => {
                  const p = progress[name];
                  const pct = p && p.total > 0 ? Math.round((p.done / p.total) * 100) : 0;
                  return (
                    <TransferRow key={name} name={name} progress={p} pct={pct} />
                  );
                })}
              </div>
            )}
          </div>
        </div>
      </Section>

      {/* ── 2. Stats bar ─────────────────────────────────────────────────────── */}
      <Section title="At-a-Glance Stats" defaultOpen>
        <div style={S.statsGrid}>
          <StatCard label="Folders" value={String(totalFolders)} icon={<Layers size={16} strokeWidth={1.5} />} />
          <StatCard label="Files Synced" value={totalFiles > 0 ? totalFiles.toLocaleString() : "—"} icon={<CheckCircle2 size={16} strokeWidth={1.5} />} ok />
          <StatCard label="Total Size" value={totalSize > 0 ? formatBytes(totalSize) : "—"} icon={<HardDrive size={16} strokeWidth={1.5} />} />
          <StatCard label="Currently Syncing" value={String(syncingCount)} icon={<RefreshCw size={16} strokeWidth={1.5} />} highlight={syncingCount > 0} />
          <StatCard label="Daemon" value={daemonOnline ? "Online" : "Offline"} icon={<Activity size={16} strokeWidth={1.5} />} ok={daemonOnline} err={!daemonOnline} />
          <StatCard label="Conflicts" value={conflictCount > 0 ? String(conflictCount) : "None"} icon={<AlertTriangle size={16} strokeWidth={1.5} />} err={conflictCount > 0} />
        </div>
      </Section>

      {/* ── 3. Bandwidth sparklines ───────────────────────────────────────────── */}
      <Section title="Bandwidth" defaultOpen>
        <div style={S.bwRow}>
          <BandwidthCard
            label="Upload"
            icon={<ArrowUp size={13} strokeWidth={1.5} style={{ color: "var(--primary)" }} />}
            current={uploadBps}
            values={bw.up}
            color="var(--primary)"
          />
          <BandwidthCard
            label="Download"
            icon={<ArrowDown size={13} strokeWidth={1.5} style={{ color: "var(--success)" }} />}
            current={downloadBps}
            values={bw.down}
            color="var(--success)"
          />
          <BandwidthCard
            label="Combined"
            icon={<ArrowUpDown size={13} strokeWidth={1.5} style={{ color: "var(--tertiary)" }} />}
            current={uploadBps + downloadBps}
            values={bw.up.map((u: number, i: number) => u + (bw.down[i] ?? 0))}
            color="var(--tertiary)"
          />
        </div>
      </Section>

      {/* ── 4. Folder overview ───────────────────────────────────────────────── */}
      <Section title="Folder Overview" defaultOpen>
        {totalFolders === 0 ? (
          <EmptyFolders onAdd={() => onNavigate("folders")} />
        ) : (
          <div style={S.folderGrid}>
            {folders.map((f) => (
              <FolderOverviewCard
                key={f.Name}
                folder={f}
                syncing={syncing.includes(f.Name)}
                progress={progress[f.Name]}
                lastSyncTs={status?.last_sync[f.Name]}
                counts={status?.counts[f.Name]}
                daemonOnline={daemonOnline}
                onSync={() => syncFolder(f.Name)}
                onOpen={() => onOpenFolder(f)}
              />
            ))}
          </div>
        )}
      </Section>

      {/* ── 5. Activity feed ──────────────────────────────────────────────────── */}
      <Section title="Recent Activity" defaultOpen>
        <div style={{ display: "flex", flexDirection: "column", gap: "0.375rem" }}>
          {syncing.length === 0 && Object.keys(status?.last_sync ?? {}).length === 0 ? (
            <EmptyActivity />
          ) : (
            <>
              {syncing.map((name) => (
                <ActivityRow key={`sync-${name}`} icon="sync" title={`Syncing "${name}"`} time="now" unread />
              ))}
              {Object.entries(status?.last_sync ?? {})
                .sort(([, a], [, b]) => new Date(b).getTime() - new Date(a).getTime())
                .map(([name, ts]) => (
                  <ActivityRow key={name} icon="check" title={`"${name}" synced successfully`} time={formatRelative(ts)} />
                ))}
            </>
          )}
        </div>
      </Section>

      {/* ── 6. Nice-to-have: error log & pending changes ──────────────────────── */}
      <Section title="Error Log" defaultOpen={false}>
        <div style={S.errorLogEmpty}>
          <CheckCircle2 size={20} strokeWidth={1} style={{ color: "var(--success)", opacity: 0.5 }} />
          <span style={{ fontSize: "0.8125rem", color: "var(--outline)", marginTop: "0.375rem" }}>No errors recorded</span>
        </div>
      </Section>
    </div>
  );
}

// ── Sub-components ─────────────────────────────────────────────────────────────

function TransferRow({ name, progress: p, pct }: { name: string; progress?: SyncProgress; pct: number }) {
  return (
    <div style={S.transferRow}>
      <div style={S.transferName}>{name}</div>
      {p?.current && (
        <div style={S.transferFile}>{p.current.split("/").pop()}</div>
      )}
      <div style={{ display: "flex", alignItems: "center", gap: "0.5rem" }}>
        <div className="progress-track" style={{ flex: 1, height: 3 }}>
          <div className="progress-fill" style={{ width: `${pct}%` }} />
        </div>
        <span style={{ fontSize: "0.6875rem", color: "var(--outline)", width: 28, textAlign: "right" }}>{pct}%</span>
      </div>
    </div>
  );
}

function StatCard({ label, value, icon, highlight, ok, err }: {
  label: string; value: string; icon: React.ReactNode;
  highlight?: boolean; ok?: boolean; err?: boolean;
}) {
  const color = err ? "var(--error)" : ok ? "var(--success)" : highlight ? "var(--tertiary)" : "var(--on-surface)";
  return (
    <div style={S.statCard}>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: "0.5rem" }}>
        <span style={{ color: "var(--outline)" }}>{icon}</span>
        <span className="label-sm">{label}</span>
      </div>
      <div style={{ fontSize: "1.375rem", fontWeight: 300, letterSpacing: "-0.02em", color }}>{value}</div>
    </div>
  );
}

function BandwidthCard({ label, icon, current, values, color }: {
  label: string; icon: React.ReactNode; current: number; values: number[]; color: string;
}) {
  return (
    <div style={S.bwCard}>
      <div style={{ display: "flex", alignItems: "center", gap: "0.4rem", marginBottom: "0.5rem" }}>
        {icon}
        <span className="label-sm">{label}</span>
      </div>
      <div style={{ fontSize: "1rem", fontWeight: 500, color, marginBottom: "0.5rem" }}>
        {formatSpeed(current)}
      </div>
      <div style={{ margin: "0 -0.875rem" }}>
        <Sparkline values={values} color={color} height={36} />
      </div>
    </div>
  );
}

function FolderOverviewCard({ folder, syncing, progress: p, lastSyncTs, counts, daemonOnline, onSync, onOpen }: {
  folder: Folder;
  syncing: boolean;
  progress?: SyncProgress;
  lastSyncTs?: string;
  counts?: { files: number; dirs: number; size: number };
  daemonOnline: boolean;
  onSync: () => void;
  onOpen: () => void;
}) {
  const pct = syncing && p && p.total > 0 ? Math.round((p.done / p.total) * 100) : syncing ? 0 : 100;

  return (
    <div style={S.folderCard} onClick={onOpen}>
      <div style={{ display: "flex", alignItems: "center", gap: "0.75rem", marginBottom: "0.75rem" }}>
        <div style={S.folderIconWrap}>
          <FolderOpen size={15} strokeWidth={1.5} />
        </div>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={S.folderCardName}>{folder.Name}</div>
          <div
            className="label-sm"
            style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", cursor: "pointer" }}
            onClick={(e) => { e.stopPropagation(); openPath(folder.LocalRoot); }}
            title={folder.LocalRoot}
          >
            {folder.LocalRoot}
          </div>
        </div>
        {syncing
          ? <span className="chip chip-syncing"><RefreshCw size={8} strokeWidth={2} style={{ animation: "spin 1.5s linear infinite" }} />Syncing</span>
          : <span className="chip chip-success"><CheckCircle2 size={8} strokeWidth={2} />Up to date</span>
        }
      </div>

      {syncing && (
        <div style={{ marginBottom: "0.75rem" }}>
          <div className="progress-track">
            <div className="progress-fill" style={{ width: `${pct}%` }} />
          </div>
          {p?.current && (
            <div className="label-sm" style={{ marginTop: "0.25rem", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
              {p.current.split("/").pop()}
            </div>
          )}
        </div>
      )}

      <div style={S.folderCardFooter}>
        <div style={{ display: "flex", gap: "0.75rem" }}>
          {counts && (
            <>
              <FooterStat value={counts.files.toLocaleString()} label="files" />
              <FooterStat value={formatBytes(counts.size)} label="size" />
            </>
          )}
        </div>
        <div style={{ display: "flex", alignItems: "center", gap: "0.375rem" }}>
          {lastSyncTs
            ? <><Clock size={10} strokeWidth={1.5} style={{ color: "var(--outline)" }} /><span style={{ fontSize: "0.6875rem", color: "var(--outline)" }}>{formatRelative(lastSyncTs)}</span></>
            : <><AlertCircle size={10} strokeWidth={1.5} style={{ color: "var(--tertiary)" }} /><span style={{ fontSize: "0.6875rem", color: "var(--tertiary)" }}>Never</span></>
          }
          <button
            className="btn-ghost"
            style={{ fontSize: "0.6875rem", padding: "0.2rem 0.4rem", marginLeft: "0.25rem" }}
            onClick={(e) => { e.stopPropagation(); onSync(); }}
            disabled={!daemonOnline}
          >
            <RefreshCw size={10} strokeWidth={1.5} />
          </button>
        </div>
      </div>
    </div>
  );
}

function FooterStat({ value, label }: { value: string; label: string }) {
  return (
    <span style={{ display: "flex", alignItems: "baseline", gap: "0.2rem" }}>
      <span style={{ fontSize: "0.75rem", fontWeight: 500, color: "var(--on-surface-variant)" }}>{value}</span>
      <span className="label-sm">{label}</span>
    </span>
  );
}

function ActivityRow({ icon, title, time, unread }: { icon: string; title: string; time: string; unread?: boolean }) {
  const iconEl = icon === "sync"
    ? <RefreshCw size={13} strokeWidth={1.5} style={{ color: "var(--primary)", animation: "spin 1.5s linear infinite" }} />
    : icon === "check"
    ? <CheckCircle2 size={13} strokeWidth={1.5} style={{ color: "var(--success)" }} />
    : <AlertCircle size={13} strokeWidth={1.5} style={{ color: "var(--tertiary)" }} />;

  return (
    <div style={{ ...S.activityRow, background: unread ? "var(--surface-container-highest)" : "transparent" }}>
      <div style={S.activityIcon}>{iconEl}</div>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontSize: "0.8125rem", color: unread ? "var(--on-surface)" : "var(--on-surface-variant)", fontWeight: unread ? 500 : 400, whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }}>
          {title}
        </div>
        {time && <div className="label-sm" style={{ marginTop: "0.1rem" }}>{time}</div>}
      </div>
      {unread && <div style={S.unreadDot} />}
    </div>
  );
}

function EmptyActivity() {
  return (
    <div style={{ textAlign: "center", padding: "1.5rem 0", color: "var(--outline)" }}>
      <CheckCircle2 size={26} strokeWidth={1} style={{ opacity: 0.35, marginBottom: "0.4rem" }} />
      <div style={{ fontSize: "0.8125rem" }}>No recent activity</div>
    </div>
  );
}

function EmptyFolders({ onAdd }: { onAdd: () => void }) {
  return (
    <div style={{ textAlign: "center", padding: "1.5rem", color: "var(--outline)" }}>
      <FolderOpen size={30} strokeWidth={1} style={{ opacity: 0.35, marginBottom: "0.5rem" }} />
      <div style={{ fontSize: "0.8125rem", marginBottom: "0.75rem" }}>No folders configured yet</div>
      <button className="btn-primary" style={{ fontSize: "0.8125rem" }} onClick={onAdd}>Add Folder</button>
    </div>
  );
}

// ── Styles ─────────────────────────────────────────────────────────────────────

const spinKeyframe = `@keyframes spin { to { transform: rotate(360deg); } }`;

const S: Record<string, React.CSSProperties> = {
  root: {
    flex: 1,
    display: "flex",
    flexDirection: "column",
    gap: "0.75rem",
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

  // Section
  section: {
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-xl)",
    padding: "1rem 1.25rem",
  },
  sectionToggle: {
    display: "flex",
    alignItems: "center",
    width: "100%",
    background: "transparent",
    border: "none",
    padding: 0,
    cursor: "pointer",
    gap: "0",
  },
  sectionTitle: {
    fontSize: "0.8125rem",
    fontWeight: 600,
    color: "var(--on-surface)",
    letterSpacing: "0.01em",
  },

  // Hero
  heroCard: {
    display: "flex",
    gap: "1.25rem",
    alignItems: "stretch",
  },
  heroLeft: {
    flex: 1,
    display: "flex",
    flexDirection: "column",
    gap: "0.5rem",
  },
  statusBadge: {
    display: "inline-flex",
    alignItems: "center",
    gap: "0.375rem",
    fontSize: "0.625rem",
    fontWeight: 600,
    letterSpacing: "0.07em",
    color: "var(--outline)",
    textTransform: "uppercase" as const,
  },
  headline: {
    fontSize: "1.5rem",
    fontWeight: 300,
    letterSpacing: "-0.02em",
    color: "var(--on-surface)",
    lineHeight: 1.2,
  },
  subtext: {
    fontSize: "0.75rem",
    color: "var(--outline)",
    display: "flex",
    alignItems: "center",
    gap: "0.3rem",
  },
  ctaRow: {
    display: "flex",
    gap: "0.5rem",
    marginTop: "0.25rem",
  },
  ctaBtn: {
    fontSize: "0.8125rem",
    display: "flex",
    alignItems: "center",
    gap: "0.375rem",
  },

  // Transfers
  transfersCol: {
    width: 240,
    minWidth: 240,
    background: "var(--surface-container-highest)",
    borderRadius: "var(--radius-lg)",
    padding: "0.875rem",
    display: "flex",
    flexDirection: "column",
    gap: "0.5rem",
  },
  colLabel: {
    fontSize: "0.625rem",
    fontWeight: 600,
    letterSpacing: "0.07em",
    color: "var(--outline)",
    textTransform: "uppercase" as const,
    marginBottom: "0.25rem",
  },
  noTransfers: {
    flex: 1,
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    justifyContent: "center",
    padding: "0.75rem 0",
  },
  transferList: {
    display: "flex",
    flexDirection: "column",
    gap: "0.625rem",
  },
  transferRow: {
    display: "flex",
    flexDirection: "column",
    gap: "0.2rem",
  },
  transferName: {
    fontSize: "0.75rem",
    fontWeight: 500,
    color: "var(--on-surface)",
    whiteSpace: "nowrap" as const,
    overflow: "hidden",
    textOverflow: "ellipsis",
  },
  transferFile: {
    fontSize: "0.6875rem",
    color: "var(--outline)",
    whiteSpace: "nowrap" as const,
    overflow: "hidden",
    textOverflow: "ellipsis",
  },

  // Stats grid
  statsGrid: {
    display: "grid",
    gridTemplateColumns: "repeat(auto-fill, minmax(140px, 1fr))",
    gap: "0.75rem",
  },
  statCard: {
    background: "var(--surface-container-highest)",
    borderRadius: "var(--radius-lg)",
    padding: "0.875rem",
  },

  // Bandwidth
  bwRow: {
    display: "grid",
    gridTemplateColumns: "repeat(3, 1fr)",
    gap: "0.75rem",
  },
  bwCard: {
    background: "var(--surface-container-highest)",
    borderRadius: "var(--radius-lg)",
    padding: "0.875rem",
    paddingBottom: "0.875rem",
    overflow: "hidden",
  },

  // Folder overview grid
  folderGrid: {
    display: "grid",
    gridTemplateColumns: "repeat(auto-fill, minmax(280px, 1fr))",
    gap: "0.75rem",
  },
  folderCard: {
    background: "var(--surface-container-highest)",
    borderRadius: "var(--radius-lg)",
    padding: "0.875rem",
    cursor: "pointer",
    transition: "background var(--transition-base)",
  },
  folderIconWrap: {
    width: 32,
    height: 32,
    background: "var(--gradient-primary)",
    borderRadius: "var(--radius-md)",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    color: "#fff",
    flexShrink: 0,
  },
  folderCardName: {
    fontSize: "0.875rem",
    fontWeight: 600,
    color: "var(--on-surface)",
    whiteSpace: "nowrap" as const,
    overflow: "hidden",
    textOverflow: "ellipsis",
    marginBottom: "0.1rem",
  },
  folderCardFooter: {
    display: "flex",
    justifyContent: "space-between",
    alignItems: "center",
  },

  // Activity
  activityRow: {
    display: "flex",
    alignItems: "center",
    gap: "0.625rem",
    padding: "0.5rem 0.625rem",
    borderRadius: "var(--radius-md)",
    transition: "background var(--transition-base)",
  },
  activityIcon: {
    width: 26,
    height: 26,
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

  // Error log / pending
  errorLogEmpty: {
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    justifyContent: "center",
    padding: "1.25rem 0",
    gap: "0.125rem",
  },
};
