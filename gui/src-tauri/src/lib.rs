use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::io::{BufRead, BufReader, Write};
use std::sync::Mutex;
use tauri::{AppHandle, Emitter};
use tauri_plugin_opener::OpenerExt;
use tauri_plugin_shell::ShellExt;

// ── IPC types (must mirror the Go structs) ────────────────────────────────────

#[derive(Debug, Serialize, Deserialize, Clone, Default)]
pub struct FolderSettings {
    #[serde(default)]
    pub sync_hidden_files: bool,
    #[serde(default)]
    pub auto_sync_on_change: bool,
}

#[derive(Debug, Serialize, Deserialize, Clone)]
pub struct Folder {
    #[serde(rename = "Name")]
    pub name: String,
    #[serde(rename = "LocalRoot")]
    pub local_root: String,
    #[serde(rename = "RemoteBase")]
    pub remote_base: String,
    /// Sub-folder names relative to RemoteBase to synchronize.
    /// An empty list means "sync the entire space".
    #[serde(rename = "Folders", default)]
    pub folders: Vec<String>,
    /// Per-folder sync settings.
    #[serde(rename = "Settings", default)]
    pub settings: FolderSettings,
}

#[derive(Debug, Serialize, Deserialize)]
struct SettingsPayload {
    #[serde(skip_serializing_if = "Option::is_none")]
    log_rotate_max_age: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    sync_interval: Option<String>,
    #[serde(default)]
    upload_bandwidth: i64,
    #[serde(default)]
    download_bandwidth: i64,
}

#[derive(Debug, Serialize, Deserialize)]
struct AccountPayload {
    username: String,
    password: String,
}

#[derive(Debug, Serialize, Deserialize)]
struct IpcRequest {
    cmd: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    folder: Option<Folder>,
    #[serde(skip_serializing_if = "Option::is_none")]
    name: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    settings: Option<SettingsPayload>,
    #[serde(skip_serializing_if = "Option::is_none")]
    account: Option<AccountPayload>,
}

#[derive(Debug, Serialize, Deserialize)]
struct IpcResponse {
    ok: bool,
    #[serde(default)]
    error: String,
    #[serde(default)]
    folders: Vec<Folder>,
    #[serde(default)]
    status: Option<StatusPayload>,
    #[serde(default)]
    settings: Option<SettingsPayload>,
    #[serde(default)]
    account: Option<AccountPayload>,
}

#[derive(Debug, Serialize, Deserialize, Clone)]
struct FileCounts {
    files: i64,
    dirs: i64,
    #[serde(default)]
    size: i64,
}

#[derive(Debug, Serialize, Deserialize, Clone)]
pub struct StatusPayload {
    syncing: Vec<String>,
    last_sync: HashMap<String, String>,
    #[serde(default)]
    counts: HashMap<String, FileCounts>,
}

// ── Tauri-facing status type ──────────────────────────────────────────────────

#[derive(Debug, Serialize, Deserialize)]
pub struct SyncStatus {
    pub syncing: Vec<String>,
    pub last_sync: HashMap<String, String>,
    pub counts: HashMap<String, FileCounts>,
}

// ── Daemon sidecar state (Windows only) ───────────────────────────────────────

#[cfg(windows)]
pub struct DaemonState {
    pub child: Mutex<Option<tauri::async_runtime::JoinHandle<()>>>,
}

// ── Socket path (mirrors Go SocketPath()) ─────────────────────────────────────

fn socket_path() -> String {
    if let Ok(xdg) = std::env::var("XDG_RUNTIME_DIR") {
        return format!("{}/cernbox-sync.sock", xdg);
    }
    let cache = dirs_next::cache_dir()
        .unwrap_or_else(|| std::path::PathBuf::from("/tmp"));
    format!("{}/cernbox-sync/sync.sock", cache.display())
}

// ── Low-level send/receive ─────────────────────────────────────────────────

fn ipc_send(req: &IpcRequest) -> Result<IpcResponse, String> {
    let path = socket_path();

    #[cfg(unix)]
    let stream = {
        use std::os::unix::net::UnixStream;
        UnixStream::connect(&path)
            .map_err(|e| format!("Cannot connect to daemon at {path}: {e}"))?
    };

    #[cfg(windows)]
    let stream = {
        use std::os::windows::net::UnixStream;
        UnixStream::connect(&path)
            .map_err(|e| format!("Cannot connect to daemon at {path}: {e}"))?
    };

    let mut writer = &stream;
    let payload = serde_json::to_string(req).map_err(|e| e.to_string())?;
    writer
        .write_all(format!("{}\n", payload).as_bytes())
        .map_err(|e| format!("Send error: {e}"))?;

    let mut reader = BufReader::new(&stream);
    let mut line = String::new();
    reader
        .read_line(&mut line)
        .map_err(|e| format!("Receive error: {e}"))?;

    let resp: IpcResponse = serde_json::from_str(&line).map_err(|e| e.to_string())?;
    if !resp.ok {
        return Err(resp.error);
    }
    Ok(resp)
}

// ── Daemon lifecycle (Windows only) ───────────────────────────────────────────

#[cfg(windows)]
fn start_daemon(app: &AppHandle) -> Result<(), String> {
    let sidecar_command = app
        .shell()
        .sidecar("cernbox-syncd")
        .map_err(|e| format!("Failed to prepare sidecar: {e}"))?;

    let (_rx, child) = sidecar_command
        .spawn()
        .map_err(|e| format!("Failed to spawn daemon: {e}"))?;

    let handle = tauri::async_runtime::spawn(async move {
        // Keep the child handle alive for the lifetime of the app.
        // Dropping it would kill the process on some platforms.
        let _ = child;
        std::future::pending::<()>().await;
    });

    app.state::<DaemonState>().child.lock().unwrap().replace(handle);
    Ok(())
}

// ── Local filesystem types ────────────────────────────────────────────────────

#[derive(Debug, Serialize, Deserialize, Clone)]
pub struct LocalEntry {
    pub name: String,
    pub path: String,
    pub is_dir: bool,
}

// ── Tauri commands ─────────────────────────────────────────────────────────────

#[tauri::command]
fn ipc_list() -> Result<Vec<Folder>, String> {
    let req = IpcRequest {
        cmd: "list".into(),
        folder: None,
        name: None,
        settings: None,
        account: None,
    };
    let resp = ipc_send(&req)?;
    Ok(resp.folders)
}

#[tauri::command]
fn ipc_add(name: String, local_root: String, remote_base: String, folders: Option<Vec<String>>, sync_hidden_files: Option<bool>, auto_sync_on_change: Option<bool>) -> Result<(), String> {
    let req = IpcRequest {
        cmd: "add".into(),
        folder: Some(Folder {
            name,
            local_root,
            remote_base,
            folders: folders.unwrap_or_default(),
            settings: FolderSettings {
                sync_hidden_files: sync_hidden_files.unwrap_or(false),
                auto_sync_on_change: auto_sync_on_change.unwrap_or(false),
            },
        }),
        name: None,
        settings: None,
        account: None,
    };
    ipc_send(&req)?;
    Ok(())
}

#[tauri::command]
fn ipc_get_account() -> Result<Option<AccountPayload>, String> {
    let req = IpcRequest {
        cmd: "get-account".into(),
        folder: None,
        name: None,
        settings: None,
        account: None,
    };
    let resp = ipc_send(&req)?;
    Ok(resp.account)
}

#[tauri::command]
fn ipc_set_account(username: String, password: String) -> Result<(), String> {
    let req = IpcRequest {
        cmd: "set-account".into(),
        folder: None,
        name: None,
        settings: None,
        account: Some(AccountPayload { username, password }),
    };
    ipc_send(&req)?;
    Ok(())
}

#[tauri::command]
fn ipc_remove(name: String) -> Result<(), String> {
    let req = IpcRequest {
        cmd: "remove".into(),
        folder: None,
        name: Some(name),
        settings: None,
        account: None,
    };
    ipc_send(&req)?;
    Ok(())
}

#[tauri::command]
fn ipc_update(name: String, local_root: String, remote_base: String, folders: Option<Vec<String>>, sync_hidden_files: Option<bool>, auto_sync_on_change: Option<bool>) -> Result<(), String> {
    let req = IpcRequest {
        cmd: "update".into(),
        folder: Some(Folder {
            name,
            local_root,
            remote_base,
            folders: folders.unwrap_or_default(),
            settings: FolderSettings {
                sync_hidden_files: sync_hidden_files.unwrap_or(false),
                auto_sync_on_change: auto_sync_on_change.unwrap_or(false),
            },
        }),
        name: None,
        settings: None,
        account: None,
    };
    ipc_send(&req)?;
    Ok(())
}

#[tauri::command]
fn ipc_sync(name: Option<String>) -> Result<(), String> {
    let req = IpcRequest {
        cmd: "sync".into(),
        folder: None,
        name,
        settings: None,
        account: None,
    };
    ipc_send(&req)?;
    Ok(())
}

#[tauri::command]
fn ipc_status() -> Result<SyncStatus, String> {
    let req = IpcRequest {
        cmd: "status".into(),
        folder: None,
        name: None,
        settings: None,
        account: None,
    };
    let resp = ipc_send(&req)?;
    let s = resp.status.ok_or("No status in response")?;
    Ok(SyncStatus {
        syncing: s.syncing,
        last_sync: s.last_sync,
        counts: s.counts,
    })
}

#[tauri::command]
fn ipc_stop() -> Result<(), String> {
    let req = IpcRequest {
        cmd: "stop".into(),
        folder: None,
        name: None,
        settings: None,
        account: None,
    };
    ipc_send(&req)?;
    Ok(())
}

#[derive(Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SettingsResult {
    pub log_rotate_max_age: Option<String>,
    pub sync_interval: Option<String>,
    pub upload_bandwidth: i64,
    pub download_bandwidth: i64,
}

#[tauri::command]
fn ipc_get_settings() -> Result<SettingsResult, String> {
    let req = IpcRequest {
        cmd: "get-settings".into(),
        folder: None,
        name: None,
        settings: None,
        account: None,
    };
    let resp = ipc_send(&req)?;
    let s = resp.settings.unwrap_or(SettingsPayload { log_rotate_max_age: None, sync_interval: None, upload_bandwidth: 0, download_bandwidth: 0 });
    Ok(SettingsResult {
        log_rotate_max_age: s.log_rotate_max_age,
        sync_interval: s.sync_interval,
        upload_bandwidth: s.upload_bandwidth,
        download_bandwidth: s.download_bandwidth,
    })
}

#[tauri::command]
fn list_local_dir(path: Option<String>) -> Result<Vec<LocalEntry>, String> {
    use std::path::Path;

    let dir_path = match path {
        Some(ref p) if !p.is_empty() => std::path::PathBuf::from(p),
        _ => dirs_next::home_dir().unwrap_or_else(|| std::path::PathBuf::from("/")),
    };

    let read = std::fs::read_dir(&dir_path)
        .map_err(|e| format!("Cannot read directory: {e}"))?;

    let mut entries: Vec<LocalEntry> = read
        .filter_map(|entry| entry.ok())
        .filter_map(|entry| {
            let name = entry.file_name().to_string_lossy().to_string();
            // Skip hidden entries (starting with .)
            if name.starts_with('.') {
                return None;
            }
            let is_dir = entry.file_type().map(|t| t.is_dir()).unwrap_or(false);
            // Only include directories
            if !is_dir {
                return None;
            }
            let path = entry.path().to_string_lossy().to_string();
            Some(LocalEntry { name, path, is_dir: true })
        })
        .collect();

    // Sort alphabetically
    entries.sort_by(|a, b| a.name.to_lowercase().cmp(&b.name.to_lowercase()));

    // Prepend a ".." entry if we're not at the root
    let parent = Path::new(&dir_path).parent();
    if let Some(parent_path) = parent {
        let parent_str = parent_path.to_string_lossy().to_string();
        entries.insert(0, LocalEntry {
            name: "..".to_string(),
            path: parent_str,
            is_dir: true,
        });
    }

    Ok(entries)
}

#[tauri::command]
fn create_local_dir(parent: String, name: String) -> Result<String, String> {
    let name = name.trim().to_string();
    if name.is_empty() {
        return Err("Folder name cannot be empty".into());
    }
    if name.contains('/') || name.contains('\\') || name.contains('\0') {
        return Err("Folder name contains invalid characters".into());
    }
    let new_path = std::path::Path::new(&parent).join(&name);
    std::fs::create_dir(&new_path)
        .map_err(|e| format!("Could not create folder: {e}"))?;
    Ok(new_path.to_string_lossy().to_string())
}

#[tauri::command]
fn read_text_file(path: String) -> Result<Option<String>, String> {
    match std::fs::read_to_string(&path) {
        Ok(content) => Ok(Some(content)),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(None),
        Err(e) => Err(e.to_string()),
    }
}

#[tauri::command]
fn open_log_file(app: AppHandle, path: String) -> Result<(), String> {
    app.opener()
        .open_path(path, None::<&str>)
        .map_err(|e: tauri_plugin_opener::Error| e.to_string())
}

#[tauri::command]
fn ipc_set_settings(log_rotate_max_age: Option<String>, sync_interval: Option<String>, upload_bandwidth: i64, download_bandwidth: i64) -> Result<(), String> {
    let req = IpcRequest {
        cmd: "set-settings".into(),
        folder: None,
        name: None,
        settings: Some(SettingsPayload { log_rotate_max_age, sync_interval, upload_bandwidth, download_bandwidth }),
        account: None,
    };
    ipc_send(&req)?;
    Ok(())
}

// ── Push event types (mirror Go ipc.Event / ipc.SubscribeResponse) ────────────

#[derive(Debug, Serialize, Deserialize, Clone)]
pub struct SyncProgressPayload {
    pub done: i32,
    pub total: i32,
    #[serde(default)]
    pub current: String,
}

/// A single push event emitted by the daemon over the subscribe connection.
#[derive(Debug, Serialize, Deserialize, Clone)]
pub struct DaemonEvent {
    #[serde(rename = "type")]
    pub type_: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub folder: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub folder_data: Option<Folder>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub progress: Option<SyncProgressPayload>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub last_sync: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub counts: Option<FileCounts>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

/// Full state snapshot sent as the first response to a subscribe command.
#[derive(Debug, Serialize, Deserialize, Clone)]
pub struct DaemonSnapshot {
    pub ok: bool,
    #[serde(default)]
    pub folders: Vec<Folder>,
    #[serde(default)]
    pub status: Option<StatusPayload>,
}

// ── Background event subscriber ────────────────────────────────────────────────

/// Starts a background thread that maintains a long-lived subscribe connection
/// to the daemon and forwards each received event as a Tauri event.
///
/// - `"daemon-snapshot"` — fired once on (re)connect with the full state
/// - `"daemon-event"`    — fired for every incremental update
/// - `"daemon-offline"`  — fired when the connection is lost (retrying)
fn start_event_subscriber(app: AppHandle) {
    std::thread::spawn(move || loop {
        match run_subscriber(&app) {
            Ok(()) => {}
            Err(e) => {
                let _ = app.emit(
                    "daemon-offline",
                    DaemonEvent {
                        type_: "daemon_offline".into(),
                        folder: None,
                        folder_data: None,
                        progress: None,
                        last_sync: None,
                        counts: None,
                        error: Some(e),
                    },
                );
            }
        }
        std::thread::sleep(std::time::Duration::from_secs(2));
    });
}

fn run_subscriber(app: &AppHandle) -> Result<(), String> {
    let path = socket_path();

    #[cfg(unix)]
    let stream = {
        use std::os::unix::net::UnixStream;
        UnixStream::connect(&path)
            .map_err(|e| format!("Cannot connect to daemon at {path}: {e}"))?
    };

    #[cfg(windows)]
    let stream = {
        use std::os::windows::net::UnixStream;
        UnixStream::connect(&path)
            .map_err(|e| format!("Cannot connect to daemon at {path}: {e}"))?
    };

    // Send the subscribe command.
    {
        let mut writer = &stream;
        writer
            .write_all(b"{\"cmd\":\"subscribe\"}\n")
            .map_err(|e| format!("Send subscribe: {e}"))?;
    }

    let mut reader = BufReader::new(&stream);
    let mut line = String::new();

    // Read the initial snapshot.
    reader
        .read_line(&mut line)
        .map_err(|e| format!("Read snapshot: {e}"))?;
    if line.is_empty() {
        return Err("Connection closed before snapshot".into());
    }
    let snapshot: DaemonSnapshot =
        serde_json::from_str(&line).map_err(|e| format!("Parse snapshot: {e}"))?;
    if !snapshot.ok {
        return Err("Daemon rejected subscribe".into());
    }
    app.emit("daemon-snapshot", &snapshot)
        .map_err(|e| format!("Emit snapshot: {e}"))?;

    // Stream incremental events until EOF or error.
    loop {
        line.clear();
        let n = reader
            .read_line(&mut line)
            .map_err(|e| format!("Read event: {e}"))?;
        if n == 0 {
            return Err("Connection closed by daemon".into());
        }
        let event: DaemonEvent =
            serde_json::from_str(&line).map_err(|e| format!("Parse event: {e}"))?;
        app.emit("daemon-event", &event)
            .map_err(|e| format!("Emit event: {e}"))?;
    }
}

/// Returns the current daemon state snapshot as a direct command result.
/// The React app calls this once on mount (after setting up event listeners)
/// to populate the initial UI state, avoiding the race condition where the
/// background subscriber emits the snapshot before JS listeners are registered.
#[tauri::command]
fn ipc_get_snapshot() -> Result<DaemonSnapshot, String> {
    let folders_resp = ipc_send(&IpcRequest {
        cmd: "list".into(),
        folder: None,
        name: None,
        settings: None,
        account: None,
    })?;
    let status_resp = ipc_send(&IpcRequest {
        cmd: "status".into(),
        folder: None,
        name: None,
        settings: None,
        account: None,
    })?;
    let s = status_resp.status.ok_or("No status in response")?;
    Ok(DaemonSnapshot {
        ok: true,
        folders: folders_resp.folders,
        status: Some(s),
    })
}

// ── App entry point ────────────────────────────────────────────────────────────

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_shell::init())
        .setup(|app| {
            // On Windows the daemon is bundled as a sidecar and started automatically.
            // On Linux/macOS the user is expected to run cernbox-syncd themselves.
            #[cfg(windows)]
            {
                app.manage(DaemonState {
                    child: Mutex::new(None),
                });
                if let Err(e) = start_daemon(app.handle()) {
                    eprintln!("Warning: could not start cernbox-syncd sidecar: {e}");
                }
            }
            // Start the background event subscriber (all platforms).
            start_event_subscriber(app.handle().clone());
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            ipc_list,
            ipc_add,
            ipc_update,
            ipc_remove,
            ipc_sync,
            ipc_status,
            ipc_stop,
            ipc_get_settings,
            ipc_set_settings,
            ipc_get_account,
            ipc_set_account,
            ipc_get_snapshot,
            list_local_dir,
            create_local_dir,
            read_text_file,
            open_log_file,
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
