// +build darwin

package client

import (
	"net"
)

// getAllUpInterfaces returns every up non-loopback interface — tunnel/virtual
// included. Used by -la and the VPN auto-escalation.
func getAllUpInterfaces() ([]string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, in := range interfaces {
		if in.Flags&net.FlagLoopback == 0 && in.Flags&net.FlagUp == 1 {
			out = append(out, in.Name)
		}
	}
	return out, nil
}

// Gets all physical interfaces based on filter results, ignoring all VM, Loopback and Tunnel interfaces.
func getAllPhysicalInterface() ([]string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	// -la: include tunnel/virtual adapters (VPN/ExitLag support)
	if ConfigGlobal.ListenAllInterfaces {
		return getAllUpInterfaces()
	}

	var outInterfaces []string

	for _, _interface := range interfaces {
		if _interface.Flags&net.FlagLoopback == 0 && _interface.Flags&net.FlagUp == 1 && isPhysicalInterface(_interface.HardwareAddr.String()) {
			outInterfaces = append(outInterfaces, _interface.Name)
		}
	}

	return outInterfaces, nil
}
