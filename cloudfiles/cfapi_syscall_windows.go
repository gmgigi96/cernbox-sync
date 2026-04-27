//go:build windows

package cloudfiles

// CfConnectSyncRoot accessed via syscall.SyscallN instead of cgo.
//
// The cgo-routed call to CfConnectSyncRoot crashes inside cldapi.dll
// (PC ~0x7ff...e7ab6468) at the same instruction that succeeds when the
// identical wrapper is invoked from a standalone C executable. Strong
// signal that Go's vectored exception handler is intercepting an
// internal access cldapi.dll expects to recover via Windows SEH. The
// LazyDLL/LazyProc syscall path bypasses cgo's exception interception
// and matches the calling convention cldapi.dll expects directly.

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
	cfCallbackTypeFetchData = 0
	cfCallbackTypeNone      = -1
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
	ole32DLL       = windows.NewLazySystemDLL("ole32.dll")
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
