//go:build windows

package cloudfiles

// CfConnectSyncRoot and CfExecute accessed via syscall.SyscallN instead of cgo.
//
// Both functions use Windows SEH internally inside cldapi.dll. When called
// via cgo, Go's vectored exception handler (VEH) intercepts the exceptions
// before the SEH handler runs, causing access violations at fixed offsets
// inside cldapi.dll. Routing through LazyDLL/LazyProc/SyscallN bypasses
// cgo's VEH integration and matches the calling convention cldapi.dll expects.

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	cldapiOnce sync.Once
	cldapiDLL  *windows.LazyDLL
	pConnect   *windows.LazyProc
	pExecute   *windows.LazyProc
)

func loadCldapiSyscall() error {
	var err error
	cldapiOnce.Do(func() {
		cldapiDLL = windows.NewLazyDLL("cldapi.dll")
		if e := cldapiDLL.Load(); e != nil {
			err = fmt.Errorf("LoadLibrary(cldapi.dll): %w", e)
			return
		}
		pConnect = cldapiDLL.NewProc("CfConnectSyncRoot")
		if e := pConnect.Find(); e != nil {
			err = fmt.Errorf("GetProcAddress(CfConnectSyncRoot): %w", e)
			return
		}
		pExecute = cldapiDLL.NewProc("CfExecute")
		if e := pExecute.Find(); e != nil {
			err = fmt.Errorf("GetProcAddress(CfExecute): %w", e)
			return
		}
	})
	return err
}

// cfCallbackRegistration mirrors CF_CALLBACK_REGISTRATION: a {Type, Callback}
// pair, with the array terminated by a Type==CF_CALLBACK_TYPE_NONE entry.
// Field layout: int32 Type, 4 bytes pad on x64, void* Callback (8 bytes).
type cfCallbackRegistration struct {
	Type     int32
	_        int32 // explicit alignment pad — keep struct == 16 bytes on x64
	Callback uintptr
}

const (
	cfCallbackTypeFetchData         = 0
	cfCallbackTypeFetchPlaceholders = 2
	cfCallbackTypeNone              = -1
)

// connectSyncRootSyscall calls CfConnectSyncRoot via the Go syscall machinery.
// Caller is responsible for SeManageVolumePrivilege and CoInitializeEx; we
// keep those in the C wrapper for now since they're shared with
// CfRegisterSyncRoot.
func connectSyncRootSyscall(path string, callbacks []cfCallbackRegistration) (int64, error) {
	if err := loadCldapiSyscall(); err != nil {
		return 0, err
	}

	wpath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, fmt.Errorf("widen path: %w", err)
	}

	if len(callbacks) == 0 || callbacks[len(callbacks)-1].Type != cfCallbackTypeNone {
		callbacks = append(callbacks, cfCallbackRegistration{Type: cfCallbackTypeNone})
	}

	var key uintptr
	r1, _, callErr := syscall.SyscallN(
		pConnect.Addr(),
		uintptr(unsafe.Pointer(wpath)),
		uintptr(unsafe.Pointer(&callbacks[0])),
		0, // CallbackContext
		0, // CF_CONNECT_FLAG_NONE
		uintptr(unsafe.Pointer(&key)),
	)
	hr := int32(r1)
	if hr < 0 {
		return 0, fmt.Errorf("CfConnectSyncRoot: HRESULT 0x%08x (errno=%v)", uint32(hr), callErr)
	}
	return int64(key), nil
}

// cfOperationInfo mirrors CF_OPERATION_INFO on x64 (official cfapi.h layout):
//
//	[0x00] ULONG StructSize (4)
//	[0x04] CF_OPERATION_TYPE Type (4, DWORD enum)
//	[0x08] CF_CONNECTION_KEY ConnectionKey (8, struct{LONGLONG Internal})
//	[0x10] CF_TRANSFER_KEY TransferKey (8, LARGE_INTEGER)
//	[0x18] PCORRELATION_VECTOR CorrelationVector (8, pointer — NULL)
//	[0x20] CF_SYNC_STATUS* SyncStatus (8, pointer — NULL)
//	[0x28] CF_REQUEST_KEY RequestKey (8, LARGE_INTEGER)
//	sizeof = 0x30 = 48 bytes
//
// Note: ConnectionKey and TransferKey come BEFORE CorrelationVector/SyncStatus
// — the opposite of what the hand-rolled struct had.
type cfOperationInfo struct {
	StructSize        uint32
	Type              uint32
	ConnectionKey     int64   // CF_CONNECTION_KEY.Internal
	TransferKey       int64   // CF_TRANSFER_KEY.QuadPart
	CorrelationVector uintptr // always NULL
	SyncStatus        uintptr // always NULL
	RequestKey        int64   // CF_REQUEST_KEY.QuadPart — 0 = CF_REQUEST_KEY_DEFAULT
}

// cfOpParamsTransfer mirrors CF_OPERATION_PARAMETERS / TransferData on x64
// (official cfapi.h layout):
//
//	[0x00] ULONG ParamSize (4)
//	[0x04] padding (4) — union is 8-byte aligned due to LARGE_INTEGER members
//	[0x08] CF_OPERATION_TRANSFER_DATA_FLAGS Flags (4, DWORD)
//	[0x0C] NTSTATUS CompletionStatus (4, LONG)
//	[0x10] LPCVOID Buffer (8, pointer) — comes BEFORE Offset/Length!
//	[0x18] LARGE_INTEGER Offset (8)
//	[0x20] LARGE_INTEGER Length (8)
//	sizeof = 0x28 = 40 bytes
//
// Note: Buffer is before Offset/Length per the SDK definition.
type cfOpParamsTransfer struct {
	ParamSize        uint32
	_pad             uint32
	Flags            uint32
	CompletionStatus int32
	Buffer           uintptr
	Offset           int64
	Length           int64
}

const (
	cfOperationTypeTransferData         = 0
	cfOperationTypeTransferPlaceholders = 4
)

// cfOpParamsTransferPlaceholders mirrors CF_OPERATION_PARAMETERS /
// TransferPlaceholders on x64 (cfapi.h):
//
//	[0x00] ULONG ParamSize (4)
//	[0x04] CF_OPERATION_TRANSFER_PLACEHOLDERS_FLAGS Flags (4)
//	[0x08] LARGE_INTEGER PlaceholderTotalCount (8)
//	[0x10] CF_PLACEHOLDER_CREATE_INFO* PlaceholderArray (8)
//	[0x18] ULONG PlaceholderCount (4)
//	[0x1C] ULONG EntriesProcessed (4)
//	[0x20] NTSTATUS CompletionStatus (4)
//	[0x24] padding (4)
//	sizeof = 0x28 = 40 bytes
type cfOpParamsTransferPlaceholders struct {
	ParamSize             uint32
	Flags                 uint32
	PlaceholderTotalCount int64
	PlaceholderArray      uintptr
	PlaceholderCount      uint32
	EntriesProcessed      uint32
	CompletionStatus      int32
	_pad                  uint32
}

// cfOperationTransferPlaceholdersFlagDisable tells the OS not to ask us to
// populate this directory again — equivalent to
// CF_OPERATION_TRANSFER_PLACEHOLDERS_FLAG_DISABLE_ON_DEMAND_POPULATION.
const cfOperationTransferPlaceholdersFlagDisable = 1

// executeTransferPlaceholdersAck answers a FETCH_PLACEHOLDERS callback with
// "I have nothing more to add — disable further population requests for this
// directory." Used by the stub callback handler to keep Windows from waiting
// on enumeration when our population policy is FULL.
func executeTransferPlaceholdersAck(connKey, xferKey int64) error {
	if err := loadCldapiSyscall(); err != nil {
		return err
	}
	opi := cfOperationInfo{
		StructSize:    uint32(unsafe.Sizeof(cfOperationInfo{})),
		Type:          cfOperationTypeTransferPlaceholders,
		ConnectionKey: connKey,
		TransferKey:   xferKey,
	}
	params := cfOpParamsTransferPlaceholders{
		ParamSize: uint32(unsafe.Sizeof(cfOpParamsTransferPlaceholders{})),
		Flags:     cfOperationTransferPlaceholdersFlagDisable,
	}
	r1, _, callErr := syscall.SyscallN(
		pExecute.Addr(),
		uintptr(unsafe.Pointer(&opi)),
		uintptr(unsafe.Pointer(&params)),
	)
	hr := int32(r1)
	if hr < 0 {
		return fmt.Errorf("CfExecute(TRANSFER_PLACEHOLDERS): HRESULT 0x%08x (errno=%v)", uint32(hr), callErr)
	}
	return nil
}

// executeTransferSyscall delivers a chunk of file content (or an error) to
// the OS via CfExecute(TRANSFER_DATA), called through syscall.SyscallN to
// avoid cgo's VEH intercepting cldapi.dll's internal SEH.
func executeTransferSyscall(connKey, xferKey, offset, length int64, buffer unsafe.Pointer, status int32) error {
	if err := loadCldapiSyscall(); err != nil {
		return err
	}

	opi := cfOperationInfo{
		StructSize:    uint32(unsafe.Sizeof(cfOperationInfo{})),
		Type:          cfOperationTypeTransferData,
		ConnectionKey: connKey,
		TransferKey:   xferKey,
	}
	params := cfOpParamsTransfer{
		ParamSize:        uint32(unsafe.Sizeof(cfOpParamsTransfer{})),
		CompletionStatus: status,
		Buffer:           uintptr(buffer),
		Offset:           offset,
		Length:           length,
	}
	r1, _, callErr := syscall.SyscallN(
		pExecute.Addr(),
		uintptr(unsafe.Pointer(&opi)),
		uintptr(unsafe.Pointer(&params)),
	)
	hr := int32(r1)
	if hr < 0 {
		return fmt.Errorf("CfExecute: HRESULT 0x%08x (errno=%v)", uint32(hr), callErr)
	}
	return nil
}

// connectPrereqs enables SeManageVolumePrivilege on the current process
// token and initializes COM to MTA on the calling thread. CfConnectSyncRoot
// requires both. Idempotent; failures here are logged but not fatal —
// CfConnectSyncRoot will report a clean HRESULT instead of crashing.
func connectPrereqs(_ string) error {
	if err := enableManageVolumePrivilege(); err != nil {
		return fmt.Errorf("enable SeManageVolumePrivilege: %w", err)
	}
	hr, err := coInitializeMTA()
	if err != nil && hr != rpcEChangedMode {
		return fmt.Errorf("CoInitializeEx(MTA): %w", err)
	}
	return nil
}

func enableManageVolumePrivilege() error {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(),
		windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer token.Close()

	var luid windows.LUID
	name, _ := windows.UTF16PtrFromString("SeManageVolumePrivilege")
	if err := windows.LookupPrivilegeValue(nil, name, &luid); err != nil {
		return err
	}

	tp := windows.Tokenprivileges{
		PrivilegeCount: 1,
		Privileges: [1]windows.LUIDAndAttributes{
			{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED},
		},
	}
	return windows.AdjustTokenPrivileges(token, false, &tp, 0, nil, nil)
}

const rpcEChangedMode = int32(-2147417850) // 0x80010106

var (
	ole32DLL        = windows.NewLazySystemDLL("ole32.dll")
	pCoInitializeEx = ole32DLL.NewProc("CoInitializeEx")
)

func coInitializeMTA() (int32, error) {
	const COINIT_MULTITHREADED = 0
	r1, _, _ := syscall.SyscallN(pCoInitializeEx.Addr(), 0, COINIT_MULTITHREADED)
	hr := int32(r1)
	if hr < 0 && hr != rpcEChangedMode {
		return hr, fmt.Errorf("HRESULT 0x%08x", uint32(hr))
	}
	return hr, nil
}
