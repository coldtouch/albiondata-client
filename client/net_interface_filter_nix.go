// +build linux

package client

import (
       "net"
       "strings"
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

       var outInterfaces []string

       wantedDevices := strings.Split(ConfigGlobal.ListenDevices, ",")
       // -l option was given, filter for explicit wanted devices
       if (ConfigGlobal.ListenDevices != "") && len(wantedDevices) > 0 {
               for _, wantedDevice := range wantedDevices {
                       for _, inter := range interfaces {
                               if inter.Name == wantedDevice {
                                       outInterfaces = append(outInterfaces, inter.Name)
                               }

                       }
               }
       // NO -l option was given, try to find all physical devices
       } else {
               for _, _interface := range interfaces {
                       // -la: include tunnel/virtual adapters (VPN/ExitLag support)
                       if ConfigGlobal.ListenAllInterfaces {
                               if _interface.Flags&net.FlagLoopback == 0 && _interface.Flags&net.FlagUp == 1 {
                                       outInterfaces = append(outInterfaces, _interface.Name)
                               }
                               continue
                       }
                       if _interface.Flags&net.FlagLoopback == 0 && _interface.Flags&net.FlagUp == 1 && isPhysicalInterface(_interface.HardwareAddr.String()) {
                               outInterfaces = append(outInterfaces, _interface.Name)
                       }
               }
       }



       return outInterfaces, nil
}
