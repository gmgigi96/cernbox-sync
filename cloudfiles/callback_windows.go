//go:build windows

package cloudfiles

/*
#include <stdlib.h>
#include "cfapi.h"
*/
import "C"

import (
	"context"
	"io"
	"sync"
	"unicode/utf16"
	"unsafe"
)

// providerRegistry maps a CF connection key to the Go-side Provider that
// owns it. The C callback only knows the connection key (an integer), so
// this is how we get back to the Go object that holds Config.Fetch.
//
// Entries are added in winProvider.Start (via registerProvider) and
// removed in winProvider.Stop (via unregisterProvider). The map is small
// — usually one entry per running daemon — so a global RWMutex is fine.
var (
	providerMu       sync.RWMutex
	providersByConnKey = map[int64]*winProvider{}
)

func registerProvider(connKey int64, p *winProvider) {
	providerMu.Lock()
	providersByConnKey[connKey] = p
	providerMu.Unlock()
}

func unregisterProvider(connKey int64) {
	providerMu.Lock()
	delete(providersByConnKey, connKey)
	providerMu.Unlock()
}

func lookupProvider(connKey int64) *winProvider {
	providerMu.RLock()
	defer providerMu.RUnlock()
	return providersByConnKey[connKey]
}

// chunkSize is the maximum bytes we hand to CfExecute(TRANSFER_DATA) in a
// single call. The CF API itself takes ranges up to 4 MiB; we use 1 MiB to
// stream big files steadily without ballooning memory.
const chunkSize = 1 << 20 // 1 MiB

// goFetchData is the C → Go bridge for CF_CALLBACK_TYPE_FETCH_DATA. The OS
// invokes it (indirectly, via on_fetch_data in cfapi_windows.c) on a Win32
// thread-pool thread. The body returns immediately after spawning a worker
// goroutine; the heavy lifting (download + chunked CfExecute) happens off
// the callback thread so the OS can dispatch other events.
//
//export goFetchData
func goFetchData(
	connectionKey C.int64_t,
	transferKey C.int64_t,
	fileIdentity unsafe.Pointer,
	fileIdentityLen C.int32_t,
	requiredOffset C.int64_t,
	requiredLength C.int64_t,
) {
	// Copy the FileIdentity bytes out of the OS-owned buffer before we
	// hand the data off to a goroutine — cldapi.dll reuses that memory.
	identity := identityToString(fileIdentity, int(fileIdentityLen))

	connKey := int64(connectionKey)
	xferKey := int64(transferKey)
	off := int64(requiredOffset)
	length := int64(requiredLength)

	go handleFetch(connKey, xferKey, identity, off, length)
}

// handleFetch is the worker-goroutine half of the callback bridge. It
// calls the Provider's Fetch function, then drives chunked
// CfExecute(TRANSFER_DATA) calls until the requested range has been
// delivered (or an error short-circuits the loop).
func handleFetch(connKey, xferKey int64, relPath string, offset, length int64) {
	p := lookupProvider(connKey)
	if p == nil {
		// The provider is gone (likely a race with Stop). Tell the OS the
		// fetch failed so the user-mode read returns instead of hanging.
		failTransfer(connKey, xferKey, offset, length, ntStatusUnsuccessful)
		return
	}

	rc, err := p.cfg.Fetch(context.Background(), relPath)
	if err != nil {
		failTransfer(connKey, xferKey, offset, length, ntStatusUnsuccessful)
		return
	}
	defer rc.Close()

	// Skip up to `offset` bytes so subsequent reads start at the right
	// place. The OS may ask for any byte range, not just from zero.
	if offset > 0 {
		if _, err := io.CopyN(io.Discard, rc, offset); err != nil {
			failTransfer(connKey, xferKey, offset, length, ntStatusUnsuccessful)
			return
		}
	}

	buf := make([]byte, chunkSize)
	delivered := int64(0)
	for delivered < length {
		want := length - delivered
		if want > int64(len(buf)) {
			want = int64(len(buf))
		}
		n, err := io.ReadFull(rc, buf[:want])
		if n > 0 {
			cur := offset + delivered
			hr := int32(C.cf_execute_transfer(
				C.int64_t(connKey),
				C.int64_t(xferKey),
				C.int64_t(cur),
				C.int64_t(n),
				unsafe.Pointer(&buf[0]),
				0, // STATUS_SUCCESS
			))
			if hr != 0 {
				return // OS-side already saw the partial; further chunks would re-fail.
			}
			delivered += int64(n)
		}
		if err != nil {
			if delivered < length {
				failTransfer(connKey, xferKey, offset+delivered, length-delivered, ntStatusUnsuccessful)
			}
			return
		}
	}
}

// failTransfer signals an error completion to the OS so the user-mode read
// returns with an error instead of hanging on the placeholder.
func failTransfer(connKey, xferKey, offset, length int64, status int32) {
	C.cf_execute_transfer(
		C.int64_t(connKey),
		C.int64_t(xferKey),
		C.int64_t(offset),
		C.int64_t(length),
		nil,
		C.int32_t(status),
	)
}

// ntStatusUnsuccessful is STATUS_UNSUCCESSFUL — generic failure NTSTATUS.
const ntStatusUnsuccessful int32 = -1073741823 // 0xC0000001

// identityToString decodes a UTF-16 byte buffer (no null terminator) into
// a Go string. Used to reverse fileIdentity() on the callback path.
func identityToString(p unsafe.Pointer, byteLen int) string {
	if p == nil || byteLen <= 0 {
		return ""
	}
	src := unsafe.Slice((*uint16)(p), byteLen/2)
	// Copy out: the underlying buffer is owned by cldapi.dll and may be
	// reused after the callback returns.
	codeUnits := make([]uint16, len(src))
	copy(codeUnits, src)
	return string(utf16.Decode(codeUnits))
}
