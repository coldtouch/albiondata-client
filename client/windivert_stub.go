//go:build !windows

package client

import "fmt"

// startWinDivertCapture is Windows-only; ExitLag/VPN redirection drivers are a
// Windows phenomenon and the WinDivert driver only exists there.
func startWinDivertCapture(_ *Router) (func(), error) {
	return nil, fmt.Errorf("WinDivert capture is only available on Windows")
}
