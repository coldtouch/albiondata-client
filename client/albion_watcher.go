package client

import (
	"fmt"
	"sync"
	"time"

	"github.com/ao-data/albiondata-client/log"
)

// vpnEscalateAfterSec: how long the physical adapters may stay silent before
// capture auto-widens to every up adapter (VPN/ExitLag zero-config support).
const vpnEscalateAfterSec = 60

type albionProcessWatcher struct {
	known     []int
	devices   []string
	listeners map[int][]*listener
	quit      chan bool
	r         *Router
	mu        sync.Mutex // guards devices + listeners + winDivert state (escalation goroutine)

	// winDivertClose stops the kernel-level capture (tier 3); nil when inactive.
	// The WinDivert listener has no pcap handle so it is deliberately NOT part
	// of apw.listeners (stop() would nil-deref its handle).
	winDivertClose   func()
	winDivertStarted bool
}

func newAlbionProcessWatcher() *albionProcessWatcher {
	return &albionProcessWatcher{
		listeners: make(map[int][]*listener),
		quit:      make(chan bool),
		r:         newRouter(),
	}
}

func (apw *albionProcessWatcher) run() error {
	log.Print("Watching Albion")
	physicalInterfaces, err := getAllPhysicalInterface()
	if err != nil {
		return err
	}
	apw.devices = physicalInterfaces
	log.Debugf("Will listen to these devices: %v", apw.devices)
	go apw.r.run()
	if ConfigGlobal.ForceWinDivert {
		go apw.startWinDivertTier("forced by -windivert")
	}
	go apw.autoEscalateForVPN()

	for {
		select {
		case <-apw.quit:
			apw.closeWatcher()
			return nil
		default:
			apw.mu.Lock()
			empty := len(apw.listeners) == 0
			apw.mu.Unlock()
			if empty {
				apw.createListeners()
			}
			time.Sleep(time.Second)
		}
	}
}

// autoEscalateForVPN widens capture to EVERY up adapter when the physical
// adapters have decoded zero game traffic for vpnEscalateAfterSec. This is
// the zero-config replacement for run-vpn-mode.bat's -la flag: VPN/ExitLag
// users' decrypted game packets surface on tunnel/TAP/virtual adapters that
// the physical-only default never watches. Double-capture cannot happen: we
// only escalate when the physical path is provably silent, and the encrypted
// tunnel on the physical NIC never matches the port-5056 BPF filter.
func (apw *albionProcessWatcher) autoEscalateForVPN() {
	if !ConfigGlobal.ListenAllInterfaces {
		time.Sleep(vpnEscalateAfterSec * time.Second)
		if photonPacketsSeen.Load() > 0 {
			return // game traffic flows on the physical adapters — nothing to do
		}
		apw.escalateToAllAdapters()
	} else {
		// -la already listens on everything; only the WinDivert tier remains.
		time.Sleep(vpnEscalateAfterSec * time.Second)
	}

	// Tier 3: another silence window after the adapter widening. If npcap sees
	// nothing on ANY adapter (ExitLag's driver can hide traffic below the NDIS
	// layer entirely), capture at the WFP layer instead — that path sees game
	// packets regardless of which driver redirects them. Needs Administrator.
	time.Sleep(vpnEscalateAfterSec * time.Second)
	if photonPacketsSeen.Load() > 0 {
		return
	}
	apw.startWinDivertTier(fmt.Sprintf("no game traffic on any adapter after %ds", 2*vpnEscalateAfterSec))
}

// escalateToAllAdapters widens pcap capture to every up adapter (tier 2).
func (apw *albionProcessWatcher) escalateToAllAdapters() {
	all, err := getAllUpInterfaces()
	if err != nil {
		log.Debugf("[VPN-Auto] Adapter enumeration failed: %v", err)
		return
	}
	apw.mu.Lock()
	existing := map[string]bool{}
	for _, d := range apw.devices {
		existing[d] = true
	}
	apw.mu.Unlock()
	var extra []string
	for _, d := range all {
		if !existing[d] {
			extra = append(extra, d)
		}
	}
	if len(extra) == 0 {
		return
	}
	log.Infof("[VPN-Auto] No game traffic on the physical adapters after %ds — also listening on %d tunnel/virtual adapter(s) (VPN/ExitLag support). Harmless if the game just isn't running yet.", vpnEscalateAfterSec, len(extra))
	for _, device := range extra {
		l := newListener(apw.r)
		go func(dev string, ln *listener) {
			// Tunnel adapters can refuse pcap opens — fail soft instead of
			// letting startOnline's log.Panic kill the whole client.
			defer func() {
				if r := recover(); r != nil {
					log.Debugf("[VPN-Auto] Could not listen on %s: %v", dev, r)
				}
			}()
			ln.startOnline(dev, 5056)
		}(device, l)
		apw.mu.Lock()
		apw.listeners[5056] = append(apw.listeners[5056], l)
		apw.devices = append(apw.devices, device)
		apw.mu.Unlock()
	}
}

// startWinDivertTier starts kernel-level capture once; safe to call from both
// the -windivert force path and the auto-escalation path.
//
// On success WinDivert becomes the SOLE capture path: all pcap listeners are
// stopped and createListeners() is suppressed. WinDivert sees every packet the
// adapters see (and more), so running both would double-count loot pickups and
// combat damage.
func (apw *albionProcessWatcher) startWinDivertTier(reason string) {
	apw.mu.Lock()
	if apw.winDivertStarted {
		apw.mu.Unlock()
		return
	}
	apw.winDivertStarted = true
	apw.mu.Unlock()

	closeFn, err := startWinDivertCapture(apw.r)
	if err != nil {
		log.Warnf("[VPN-Auto] WinDivert capture unavailable (%s): %v", reason, err)
		return
	}
	log.Infof("[VPN-Auto] WinDivert kernel capture ACTIVE (%s) — game traffic is now read inside the Windows network stack, below any VPN/ExitLag driver.", reason)

	apw.mu.Lock()
	for port := range apw.listeners {
		for _, l := range apw.listeners[port] {
			l.stop()
		}
		delete(apw.listeners, port)
	}
	apw.winDivertClose = closeFn
	apw.mu.Unlock()
}

func (apw *albionProcessWatcher) closeWatcher() {
	log.Print("Albion watcher closed")

	apw.mu.Lock()
	for port := range apw.listeners {
		for _, l := range apw.listeners[port] {
			l.stop()
		}

		delete(apw.listeners, port)
	}
	if apw.winDivertClose != nil {
		apw.winDivertClose()
		apw.winDivertClose = nil
	}
	apw.mu.Unlock()

	apw.r.quit <- true
}

func (apw *albionProcessWatcher) createListeners() {
	filtered := [1]int{5056} // keep overdesign to listen on many ports

	apw.mu.Lock()
	defer apw.mu.Unlock()
	if apw.winDivertClose != nil {
		return // kernel capture is the sole path — don't re-create pcap listeners
	}
	for _, port := range filtered {
		for _, device := range apw.devices {
			l := newListener(apw.r)
			go l.startOnline(device, port)

			apw.listeners[port] = append(apw.listeners[port], l)
		}
	}
}
