use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::io::{BufRead, BufReader, Write};
use std::os::unix::net::UnixStream;

// ── IPC types (must mirror the Go structs) ────────────────────────────────────

#[derive(Debug, Serialize, Deserialize, Clone)]
pub struct Folder {
    #[serde(rename = "Name")]
    pub name: String,
    #[serde(rename = "LocalRoot")]
    pub local_root: String,
    #[serde(rename = "RemoteBase")]
    pub remote_base: String,
    #[serde(rename = "Username")]
    pub username: String,
    #[serde(rename = "Password")]
    pub password: String,
}

#[derive(Debug, Serialize, Deserialize)]
struct SettingsPayload {
    #[serde(skip_serializing_if = "Option::is_none")]
    log_rotate_max_age: Option<String>,
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
}

#[derive(Debug, Serialize, Deserialize)]
struct StatusPayload {
    syncing: Vec<String>,
    last_sync: HashMap<String, String>,
}

// ── Tauri-facing status type ──────────────────────────────────────────────────

#[derive(Debug, Serialize, Deserialize)]
pub struct SyncStatus {
    pub syncing: Vec<String>,
    pub last_sync: HashMap<String, String>,
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
    let mut stream =
        UnixStream::connect(&path).map_err(|e| format!("Cannot connect to daemon at {path}: {e}"))?;

    let payload = serde_json::to_string(req).map_err(|e| e.to_string())?;
    stream
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

// ── Tauri commands ─────────────────────────────────────────────────────────────

#[tauri::command]
fn ipc_list() -> Result<Vec<Folder>, String> {
    let req = IpcRequest {
        cmd: "list".into(),
        folder: None,
        name: None,
        settings: None,
    };
    let resp = ipc_send(&req)?;
    Ok(resp.folders)
}

#[tauri::command]
fn ipc_add(
    name: String,
    local_root: String,
    remote_base: String,
    username: String,
    password: String,
) -> Result<(), String> {
    let req = IpcRequest {
        cmd: "add".into(),
        folder: Some(Folder {
            name,
            local_root,
            remote_base,
            username,
            password,
        }),
        name: None,
        settings: None,
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
    };
    let resp = ipc_send(&req)?;
    let s = resp.status.ok_or("No status in response")?;
    Ok(SyncStatus {
        syncing: s.syncing,
        last_sync: s.last_sync,
    })
}

#[tauri::command]
fn ipc_stop() -> Result<(), String> {
    let req = IpcRequest {
        cmd: "stop".into(),
        folder: None,
        name: None,
        settings: None,
    };
    ipc_send(&req)?;
    Ok(())
}

#[tauri::command]
fn ipc_get_settings() -> Result<Option<String>, String> {
    let req = IpcRequest {
        cmd: "get-settings".into(),
        folder: None,
        name: None,
        settings: None,
    };
    let resp = ipc_send(&req)?;
    Ok(resp
        .settings
        .and_then(|s| s.log_rotate_max_age))
}

#[tauri::command]
fn ipc_set_settings(log_rotate_max_age: Option<String>) -> Result<(), String> {
    let req = IpcRequest {
        cmd: "set-settings".into(),
        folder: None,
        name: None,
        settings: Some(SettingsPayload { log_rotate_max_age }),
    };
    ipc_send(&req)?;
    Ok(())
}

// ── App entry point ────────────────────────────────────────────────────────────

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_opener::init())
        .invoke_handler(tauri::generate_handler![
            ipc_list,
            ipc_add,
            ipc_remove,
            ipc_sync,
            ipc_status,
            ipc_stop,
            ipc_get_settings,
            ipc_set_settings,
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
