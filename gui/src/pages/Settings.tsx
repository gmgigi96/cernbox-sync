import { useState, useEffect } from "react";
import { Save, X, RotateCcw, AlertCircle, CheckCircle2, Timer, Upload, Download, ArrowLeftRight, FolderCog } from "lucide-react";
import { ipc } from "../ipc";

// ── Bandwidth helpers ─────────────────────────────────────────────────────────

const BW_UNITS = [
  { label: "KB/s", multiplier: 1_024 },
  { label: "MB/s", multiplier: 1_024 * 1_024 },
  { label: "GB/s", multiplier: 1_024 * 1_024 * 1_024 },
] as const;

type BwUnit = typeof BW_UNITS[number]["label"];

/** Split bytes/sec into a (value, unit) pair using the largest whole unit. */
function bytesToDisplay(bytes: number): { value: number; unit: BwUnit } {
  if (bytes <= 0) return { value: 0, unit: "MB/s" };
  for (let i = BW_UNITS.length - 1; i >= 0; i--) {
    const { multiplier, label } = BW_UNITS[i];
    if (bytes % multiplier === 0) return { value: bytes / multiplier, unit: label };
  }
  return { value: bytes / BW_UNITS[0].multiplier, unit: BW_UNITS[0].label };
}

function displayToBytes(value: number, unit: BwUnit): number {
  if (value <= 0) return 0;
  return value * (BW_UNITS.find((u) => u.label === unit)!.multiplier);
}

// ── BandwidthInput component ──────────────────────────────────────────────────

function BandwidthInput({
  bytes,
  onChange,
  disabled,
}: {
  bytes: number;
  onChange: (bytes: number) => void;
  disabled?: boolean;
}) {
  const init = bytesToDisplay(bytes);
  const [value, setValue] = useState(init.value);
  const [unit, setUnit] = useState<BwUnit>(init.unit);

  // Sync inward when the parent resets/loads the value.
  useEffect(() => {
    const d = bytesToDisplay(bytes);
    setValue(d.value);
    setUnit(d.unit);
  }, [bytes]);

  function handleValue(raw: string) {
    const n = Math.max(0, parseInt(raw) || 0);
    setValue(n);
    onChange(displayToBytes(n, unit));
  }

  function handleUnit(u: BwUnit) {
    setUnit(u);
    onChange(displayToBytes(value, u));
  }

  return (
    <div style={{
      display: "flex",
      background: "var(--surface-container-lowest)",
      border: "1px solid rgba(68,71,90,0.10)",
      borderRadius: "var(--radius-md)",
      overflow: "hidden",
    }}>
      <input
        style={{
          flex: 1,
          background: "transparent",
          color: "var(--on-surface)",
          border: "none",
          borderRight: "1px solid rgba(68,71,90,0.10)",
          padding: "0.5rem 0.75rem",
          fontSize: "0.875rem",
          outline: "none",
          fontFamily: "var(--font-family)",
          minWidth: 0,
          MozAppearance: "textfield",
        }}
        type="number"
        min={0}
        placeholder="0 — unlimited"
        value={value === 0 ? "" : value}
        onChange={(e) => handleValue(e.target.value)}
        disabled={disabled}
      />
      <select
        style={{
          background: "var(--surface-container-high)",
          color: "var(--on-surface-variant)",
          border: "none",
          borderLeft: "1px solid rgba(68,71,90,0.10)",
          padding: "0 0.5rem",
          fontSize: "0.75rem",
          outline: "none",
          fontFamily: "var(--font-family)",
          cursor: "pointer",
          flexShrink: 0,
          width: "5.5rem",
          appearance: "none",
          WebkitAppearance: "none",
          textAlign: "center",
        }}
        value={unit}
        onChange={(e) => handleUnit(e.target.value as BwUnit)}
        disabled={disabled}
      >
        {BW_UNITS.map((u) => (
          <option key={u.label} value={u.label} style={{ background: "var(--surface-container-high)" }}>{u.label}</option>
        ))}
      </select>
    </div>
  );
}

// Default values committed when Reset is clicked.
const DEFAULTS = {
  logRotateMaxAge: "",
  syncInterval: "5m",
  uploadBandwidth: 0,
  downloadBandwidth: 0,
  transferStreams: 1,
  metadataStreams: 1,
};

export function Settings() {
  // committed = what the daemon currently has (loaded on mount, updated on save/reset)
  const [committed, setCommitted] = useState(DEFAULTS);
  const [logRotateMaxAge, setLogRotateMaxAge] = useState("");
  const [syncInterval, setSyncInterval] = useState("");
  const [uploadBandwidth, setUploadBandwidth] = useState(0);
  const [downloadBandwidth, setDownloadBandwidth] = useState(0);
  const [transferStreams, setTransferStreams] = useState(1);
  const [metadataStreams, setMetadataStreams] = useState(1);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    ipc.getSettings()
      .then((v) => {
        const lrma = v.logRotateMaxAge ?? "";
        const si = v.syncInterval ?? "5m";
        const ubw = v.uploadBandwidth ?? 0;
        const dbw = v.downloadBandwidth ?? 0;
        const ts = v.transferStreams > 0 ? v.transferStreams : 1;
        const ms = v.metadataStreams > 0 ? v.metadataStreams : 1;
        setCommitted({ logRotateMaxAge: lrma, syncInterval: si, uploadBandwidth: ubw, downloadBandwidth: dbw, transferStreams: ts, metadataStreams: ms });
        setLogRotateMaxAge(lrma);
        setSyncInterval(si);
        setUploadBandwidth(ubw);
        setDownloadBandwidth(dbw);
        setTransferStreams(ts);
        setMetadataStreams(ms);
      })
      .catch(() => {})
      .finally(() => setLoading(false));
  }, []);

  const dirty =
    logRotateMaxAge !== committed.logRotateMaxAge ||
    syncInterval !== committed.syncInterval ||
    uploadBandwidth !== committed.uploadBandwidth ||
    downloadBandwidth !== committed.downloadBandwidth ||
    transferStreams !== committed.transferStreams ||
    metadataStreams !== committed.metadataStreams;

  function cancel() {
    setLogRotateMaxAge(committed.logRotateMaxAge);
    setSyncInterval(committed.syncInterval);
    setUploadBandwidth(committed.uploadBandwidth);
    setDownloadBandwidth(committed.downloadBandwidth);
    setTransferStreams(committed.transferStreams);
    setMetadataStreams(committed.metadataStreams);
    setError(null);
  }

  async function reset() {
    setError(null);
    try {
      await ipc.setSettings(
        DEFAULTS.logRotateMaxAge || null,
        DEFAULTS.syncInterval || null,
        DEFAULTS.uploadBandwidth,
        DEFAULTS.downloadBandwidth,
        DEFAULTS.transferStreams,
        DEFAULTS.metadataStreams,
      );
      setCommitted(DEFAULTS);
      setLogRotateMaxAge(DEFAULTS.logRotateMaxAge);
      setSyncInterval(DEFAULTS.syncInterval);
      setUploadBandwidth(DEFAULTS.uploadBandwidth);
      setDownloadBandwidth(DEFAULTS.downloadBandwidth);
      setTransferStreams(DEFAULTS.transferStreams);
      setMetadataStreams(DEFAULTS.metadataStreams);
      setSaved(true);
      setTimeout(() => setSaved(false), 2500);
    } catch (e) {
      setError(String(e));
    }
  }

  async function save() {
    setError(null);
    try {
      await ipc.setSettings(logRotateMaxAge.trim() || null, syncInterval.trim() || null, uploadBandwidth, downloadBandwidth, transferStreams, metadataStreams);
      const next = { logRotateMaxAge: logRotateMaxAge.trim(), syncInterval: syncInterval.trim(), uploadBandwidth, downloadBandwidth, transferStreams, metadataStreams };
      setCommitted(next);
      setSaved(true);
      setTimeout(() => setSaved(false), 2500);
    } catch (e) {
      setError(String(e));
    }
  }

  return (
    <div style={styles.root}>
      {/* Page header row */}
      <div style={styles.pageHeaderRow}>
        <div>
          <h1 style={styles.title}>Settings</h1>
          <p style={styles.subtitle}>Configure daemon behaviour</p>
        </div>
        <div style={styles.headerActions}>
          <button
            className="btn-ghost"
            style={{ gap: "0.375rem", display: "flex", alignItems: "center" }}
            onClick={reset}
            disabled={loading}
            title="Reset all settings to defaults and save"
          >
            <RotateCcw size={13} strokeWidth={1.5} /> Reset to defaults
          </button>
          {dirty && (
            <>
              <button
                className="btn-ghost"
                style={{ gap: "0.375rem", display: "flex", alignItems: "center" }}
                onClick={cancel}
              >
                <X size={13} strokeWidth={1.5} /> Cancel
              </button>
              <button className="btn-primary" onClick={save} disabled={loading}>
                <Save size={13} strokeWidth={1.5} /> Save
              </button>
            </>
          )}
        </div>
      </div>

      {error && (
        <div style={styles.errorBox}>
          <AlertCircle size={14} strokeWidth={1.5} />
          {error}
        </div>
      )}

      {saved && (
        <div style={styles.successBox}>
          <CheckCircle2 size={14} strokeWidth={1.5} />
          Settings saved.
        </div>
      )}

      <div style={styles.grid}>
        <div style={styles.section}>
          <div style={styles.sectionHeader}>
            <Timer size={15} strokeWidth={1.5} style={{ color: "var(--primary)" }} />
            <span style={styles.sectionTitle}>Sync Interval</span>
          </div>
          <div style={styles.field}>
            <p style={styles.hint}>
              How often to automatically sync all folders (e.g.{" "}
              <code style={styles.code}>5m</code>, <code style={styles.code}>1h</code>, <code style={styles.code}>30m</code>). Default is <code style={styles.code}>5m</code>.
            </p>
            <input
              style={styles.input}
              placeholder="e.g. 5m"
              value={syncInterval}
              onChange={(e) => setSyncInterval(e.target.value)}
              disabled={loading}
            />
          </div>
        </div>

        <div style={styles.section}>
          <div style={styles.sectionHeader}>
            <RotateCcw size={15} strokeWidth={1.5} style={{ color: "var(--primary)" }} />
            <span style={styles.sectionTitle}>Log Rotation Time</span>
          </div>
          <div style={styles.field}>
            <p style={styles.hint}>
              Duration string for how long per-folder log entries are kept (e.g.{" "}
              <code style={styles.code}>168h</code>, <code style={styles.code}>720h</code>). Leave blank to disable rotation.
            </p>
            <input
              style={styles.input}
              placeholder="e.g. 720h"
              value={logRotateMaxAge}
              onChange={(e) => setLogRotateMaxAge(e.target.value)}
              disabled={loading}
            />
          </div>
        </div>

        <div style={styles.section}>
          <div style={styles.sectionHeader}>
            <Upload size={15} strokeWidth={1.5} style={{ color: "var(--primary)" }} />
            <span style={styles.sectionTitle}>Upload Bandwidth</span>
          </div>
          <div style={styles.field}>
            <p style={styles.hint}>
              Maximum upload speed shared across all syncs. Leave at <code style={styles.code}>0</code> for unlimited.
            </p>
            <BandwidthInput bytes={uploadBandwidth} onChange={setUploadBandwidth} disabled={loading} />
          </div>
        </div>

        <div style={styles.section}>
          <div style={styles.sectionHeader}>
            <Download size={15} strokeWidth={1.5} style={{ color: "var(--primary)" }} />
            <span style={styles.sectionTitle}>Download Bandwidth</span>
          </div>
          <div style={styles.field}>
            <p style={styles.hint}>
              Maximum download speed shared across all syncs. Leave at <code style={styles.code}>0</code> for unlimited.
            </p>
            <BandwidthInput bytes={downloadBandwidth} onChange={setDownloadBandwidth} disabled={loading} />
          </div>
        </div>

        <div style={styles.section}>
          <div style={styles.sectionHeader}>
            <ArrowLeftRight size={15} strokeWidth={1.5} style={{ color: "var(--primary)" }} />
            <span style={styles.sectionTitle}>Transfer Streams</span>
          </div>
          <div style={styles.field}>
            <p style={styles.hint}>
              Number of concurrent upload/download connections per sync cycle.{" "}
              <code style={styles.code}>1</code> means sequential (default).
              Increase to speed up syncs with many small files.
            </p>
            <input
              style={styles.input}
              type="number"
              min={1}
              max={32}
              placeholder="1"
              value={transferStreams}
              onChange={(e) => setTransferStreams(Math.max(1, parseInt(e.target.value) || 1))}
              disabled={loading}
            />
          </div>
        </div>

        <div style={styles.section}>
          <div style={styles.sectionHeader}>
            <FolderCog size={15} strokeWidth={1.5} style={{ color: "var(--primary)" }} />
            <span style={styles.sectionTitle}>Metadata Streams</span>
          </div>
          <div style={styles.field}>
            <p style={styles.hint}>
              Number of concurrent metadata operations (directory creates and
              deletes) per depth tier per sync cycle.{" "}
              <code style={styles.code}>1</code> means sequential (default).
            </p>
            <input
              style={styles.input}
              type="number"
              min={1}
              max={32}
              placeholder="1"
              value={metadataStreams}
              onChange={(e) => setMetadataStreams(Math.max(1, parseInt(e.target.value) || 1))}
              disabled={loading}
            />
          </div>
        </div>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    flex: 1,
    display: "flex",
    flexDirection: "column",
    padding: "1.5rem",
    gap: "1.25rem",
    overflow: "hidden auto",
  },
  pageHeaderRow: {
    display: "flex",
    alignItems: "flex-start",
    justifyContent: "space-between",
    gap: "1rem",
    marginBottom: "0.25rem",
  },
  title: {
    fontSize: "1.375rem",
    fontWeight: 300,
    letterSpacing: "-0.02em",
    color: "var(--on-surface)",
    marginBottom: "0.25rem",
  },
  subtitle: { fontSize: "0.8125rem", color: "var(--on-surface-variant)" },
  headerActions: {
    display: "flex",
    alignItems: "center",
    gap: "0.5rem",
    flexShrink: 0,
    paddingTop: "0.25rem",
  },
  grid: {
    display: "grid",
    gridTemplateColumns: "repeat(auto-fill, minmax(280px, 1fr))",
    gap: "1.25rem",
    alignItems: "stretch",
  },
  section: {
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-xl)",
    padding: "1.25rem",
    display: "flex",
    flexDirection: "column",
    gap: "1rem",
  },
  sectionHeader: {
    display: "flex",
    alignItems: "center",
    gap: "0.5rem",
    marginBottom: "0.25rem",
  },
  sectionTitle: {
    fontSize: "0.875rem",
    fontWeight: 600,
    color: "var(--on-surface)",
  },
  field: { display: "flex", flexDirection: "column", gap: "0.375rem", flex: 1 },
  label: { fontSize: "0.8125rem", fontWeight: 500, color: "var(--on-surface-variant)" },
  hint: { fontSize: "0.75rem", color: "var(--outline)", lineHeight: 1.5, flex: 1 },
  input: {
    background: "var(--surface-container-lowest)",
    color: "var(--on-surface)",
    border: "1px solid rgba(68,71,90,0.10)",
    borderRadius: "var(--radius-md)",
    padding: "0.5rem 0.75rem",
    fontSize: "0.875rem",
    outline: "none",
    fontFamily: "var(--font-family)",
    width: "100%",
  },
  code: {
    background: "var(--surface-container-highest)",
    color: "var(--primary)",
    padding: "0.0625rem 0.3125rem",
    borderRadius: "var(--radius-sm)",
    fontSize: "0.75rem",
    fontFamily: "monospace",
  },
  errorBox: {
    display: "flex",
    alignItems: "center",
    gap: "0.5rem",
    background: "rgba(255,107,107,0.1)",
    borderRadius: "var(--radius-md)",
    padding: "0.5rem 0.75rem",
    fontSize: "0.8125rem",
    color: "var(--error)",
  },
  successBox: {
    display: "flex",
    alignItems: "center",
    gap: "0.5rem",
    background: "rgba(107,217,160,0.1)",
    borderRadius: "var(--radius-md)",
    padding: "0.5rem 0.75rem",
    fontSize: "0.8125rem",
    color: "var(--success)",
  },
};
