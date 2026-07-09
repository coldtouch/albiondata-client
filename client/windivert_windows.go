//go:build windows

package client

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/ao-data/albiondata-client/log"
	photon "github.com/ao-data/photon-spectator"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
)

// ============================================================================
// WinDivert capture — the "works regardless" path for ExitLag / VPN users.
//
// npcap sees packets at the NDIS (adapter) layer. ExitLag's driver redirects
// game traffic below/at that layer, so on some setups NO pcap-visible adapter
// ever carries decrypted UDP 5056 — the adapter auto-escalation finds nothing.
// WinDivert instead registers a Windows Filtering Platform (WFP) callout
// INSIDE the TCP/IP stack: outbound game packets are seen before any
// redirection driver touches them, inbound ones after ExitLag decapsulates.
// SNIFF+RECV_ONLY means we only copy matching packets — nothing is modified,
// dropped or delayed.
//
// The only cost: opening the WinDivert driver requires Administrator.
//
// Driver provenance: WinDivert 2.2.2 (official release zip from
// github.com/basil00/WinDivert, x64). Embedded and extracted at runtime to
// DataDir()\windivert so self-updated exes work without a reinstall.
//   WinDivert.dll    sha256 c1e060ee19444a259b2162f8af0f3fe8c4428a1c6f694dce20de194ac8d7d9a2
//   WinDivert64.sys  sha256 8da085332782708d8767bcace5327a6ec7283c17cfb85e40b03cd2323a90ddc2
// ============================================================================

//go:embed windivert_bin/WinDivert.dll
var winDivertDLLBytes []byte

//go:embed windivert_bin/WinDivert64.sys
var winDivertSysBytes []byte

const (
	winDivertLayerNetwork = 0
	winDivertFlagSniff    = 0x0001
	winDivertFlagRecvOnly = 0x0004

	winDivertInvalidHandle = ^uintptr(0)

	// Game traffic is UDP on port 5056 (Photon). TCP 5056 exists as a fallback
	// transport but is not used by live servers; keep the kernel filter tight.
	winDivertFilter = "udp and (udp.SrcPort == 5056 or udp.DstPort == 5056)"
)

// winDivertAddress is an opaque, 8-byte-aligned stand-in for WINDIVERT_ADDRESS
// (80 bytes in v2.2). We never read its fields in sniff mode.
type winDivertAddress [16]uint64

var (
	winDivertLoadOnce sync.Once
	winDivertLoadErr  error
	procOpen          uintptr
	procRecv          uintptr
	procClose         uintptr
)

// extractWinDivert writes the embedded DLL + driver next to each other under
// the writable data dir (WinDivert.dll loads WinDivert64.sys from its own
// directory) and returns the DLL path.
func extractWinDivert() (string, error) {
	dir := filepath.Join(DataDir(), "windivert")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	files := map[string][]byte{
		"WinDivert.dll":   winDivertDLLBytes,
		"WinDivert64.sys": winDivertSysBytes,
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		existing, err := os.ReadFile(path)
		if err == nil && bytes.Equal(existing, content) {
			continue // already extracted and identical
		}
		if err := os.WriteFile(path, content, 0755); err != nil {
			return "", fmt.Errorf("extract %s: %w", name, err)
		}
	}
	return filepath.Join(dir, "WinDivert.dll"), nil
}

func loadWinDivert() error {
	winDivertLoadOnce.Do(func() {
		dllPath, err := extractWinDivert()
		if err != nil {
			winDivertLoadErr = err
			return
		}
		handle, err := syscall.LoadLibrary(dllPath)
		if err != nil {
			winDivertLoadErr = fmt.Errorf("load %s: %w", dllPath, err)
			return
		}
		for name, dst := range map[string]*uintptr{
			"WinDivertOpen":  &procOpen,
			"WinDivertRecv":  &procRecv,
			"WinDivertClose": &procClose,
		} {
			addr, err := syscall.GetProcAddress(handle, name)
			if err != nil {
				winDivertLoadErr = fmt.Errorf("resolve %s: %w", name, err)
				return
			}
			*dst = addr
		}
	})
	return winDivertLoadErr
}

func winDivertOpen(filter string, layer int32, priority int16, flags uint64) (uintptr, error) {
	filterPtr, err := syscall.BytePtrFromString(filter)
	if err != nil {
		return 0, err
	}
	r1, _, e1 := syscall.SyscallN(procOpen,
		uintptr(unsafe.Pointer(filterPtr)),
		uintptr(layer),
		uintptr(uint16(priority)),
		uintptr(flags))
	if r1 == winDivertInvalidHandle {
		return 0, e1
	}
	return r1, nil
}

func winDivertRecv(handle uintptr, buf []byte, addr *winDivertAddress) (int, error) {
	var recvLen uint32
	r1, _, e1 := syscall.SyscallN(procRecv,
		handle,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		uintptr(unsafe.Pointer(&recvLen)),
		uintptr(unsafe.Pointer(addr)))
	if r1 == 0 {
		return 0, e1
	}
	return int(recvLen), nil
}

func winDivertClose(handle uintptr) {
	_, _, _ = syscall.SyscallN(procClose, handle)
}

// winDivertOpenErrorHint translates the common WinDivertOpen failures into
// actionable user guidance.
func winDivertOpenErrorHint(err error) string {
	errno, ok := err.(syscall.Errno)
	if !ok {
		return ""
	}
	switch errno {
	case 5: // ERROR_ACCESS_DENIED
		return "VPN/ExitLag capture mode needs Administrator — right-click albiondata-client.exe and choose 'Run as administrator'."
	case 577, 1275: // ERROR_INVALID_IMAGE_HASH / ERROR_DRIVER_BLOCKED
		return "Windows blocked the WinDivert driver (signature policy). Secure Boot / WDAC setups may refuse it."
	case 1058: // ERROR_SERVICE_DISABLED
		return "The WinDivert driver service is disabled on this machine."
	}
	return ""
}

// startWinDivertCapture opens a kernel-level sniff on game traffic and feeds
// every packet into a dedicated listener (same decode path as pcap capture).
// Returns a close func that stops the capture.
func startWinDivertCapture(r *Router) (func(), error) {
	if err := loadWinDivert(); err != nil {
		return nil, err
	}

	handle, err := winDivertOpen(winDivertFilter, winDivertLayerNetwork, 0, winDivertFlagSniff|winDivertFlagRecvOnly)
	if err != nil {
		if hint := winDivertOpenErrorHint(err); hint != "" {
			return nil, fmt.Errorf("%v — %s", err, hint)
		}
		return nil, err
	}

	// Same registration the pcap listeners perform (idempotent map store).
	layers.RegisterUDPPortLayerType(layers.UDPPort(5056), photon.PhotonLayerType)

	l := newListener(r)
	l.displayName = "windivert: udp 5056"

	var closed atomic.Bool
	go func() {
		buf := make([]byte, 65575)
		var addr winDivertAddress
		for {
			n, err := winDivertRecv(handle, buf, &addr)
			if err != nil {
				if closed.Load() {
					return
				}
				log.Debugf("[WinDivert] Recv error: %v", err)
				continue
			}
			// WinDivert NETWORK layer delivers the raw IP packet. The game is
			// IPv4-only; processPacket ignores anything without an IPv4 layer.
			if n < 20 || buf[0]>>4 != 4 {
				continue
			}
			data := make([]byte, n)
			copy(data, buf[:n])
			packet := gopacket.NewPacket(data, layers.LayerTypeIPv4, gopacket.DecodeOptions{NoCopy: true})
			l.processPacket(packet)
		}
	}()

	return func() {
		closed.Store(true)
		winDivertClose(handle)
	}, nil
}
