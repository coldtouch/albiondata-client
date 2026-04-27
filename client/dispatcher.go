package client

import (
	"encoding/json"
	"net/http"
	"sync"

	"strings"

	"github.com/ao-data/albiondata-client/lib"
	"github.com/ao-data/albiondata-client/log"
)

type dispatcher struct{}

var (
	wsHub *WSHub
	dis   *dispatcher
)

// uploaderCache reuses constructed uploaders across send calls. Without this
// cache, sendMsgToPublicUploaders / sendMsgToPrivateUploaders rebuilt the entire
// uploader chain (including a brand-new nats.Connect and a fresh http.Transport)
// for EVERY dispatched message — leaking a NATS TCP connection per message in
// the NATS path and defeating HTTP keep-alive in the POW path.
//
// Keyed by the (resolved) comma-separated target URL string. The Public list's
// magic placeholder gets resolved to a per-region URL up to 4 distinct keys
// (one per game-server region we've seen this run), so the cache stays bounded.
var (
	uploaderCacheMu sync.RWMutex
	uploaderCache   = make(map[string][]uploader)
)

// getOrCreateUploaders returns the cached uploader chain for targets, or builds
// and caches it on the first call. Double-check locking ensures createUploaders
// runs at most once per unique target string even under concurrent send calls
// from the worker pool.
func getOrCreateUploaders(targets string) []uploader {
	uploaderCacheMu.RLock()
	if cached, ok := uploaderCache[targets]; ok {
		uploaderCacheMu.RUnlock()
		return cached
	}
	uploaderCacheMu.RUnlock()

	uploaderCacheMu.Lock()
	defer uploaderCacheMu.Unlock()
	if cached, ok := uploaderCache[targets]; ok {
		return cached
	}
	created := createUploaders(strings.Split(targets, ","))
	uploaderCache[targets] = created
	return created
}

func createDispatcher() {
	dis = &dispatcher{}

	if ConfigGlobal.EnableWebsockets {
		wsHub = newHub()
		go wsHub.run()
		go runHTTPServer()
	}
}

func createUploaders(targets []string) []uploader {
	var uploaders []uploader
	for _, target := range targets {
		if target == "" {
			continue
		}
		if len(target) < 4 {
			log.Infof("Got an ingest target that was less than 4 characters, not a valid ingest target: %v", target)
			continue
		}

		if target[0:8] == "http+pow" ||  target[0:9] == "https+pow" {
			uploaders = append(uploaders, newHTTPUploaderPow(target))
		} else if target[0:4] == "http" || target[0:5] == "https" {
			uploaders = append(uploaders, newHTTPUploader(target))
		} else if target[0:4] == "nats" {
			uploaders = append(uploaders, newNATSUploader(target))
		} else {
			log.Infof("An invalid ingest target was specified: %v", target)
		}
	}

	return uploaders
}

func sendMsgToPublicUploaders(upload interface{}, topic string, state *albionState, identifier string) {
	data, err := json.Marshal(upload)
	if err != nil {
		log.Errorf("Error while marshalling payload for %v: %v", err, topic)
		return
	}

	var PublicIngestBaseUrls = ConfigGlobal.PublicIngestBaseUrls
	// http+pow://albion-online-data.com is used as a magic placeholder for every realm there is
	if strings.Contains(ConfigGlobal.PublicIngestBaseUrls, "https+pow://albion-online-data.com") {
		// we replace the placeholder with the correct one based on the serverID from albionState
		PublicIngestBaseUrls = strings.Replace(PublicIngestBaseUrls, "https+pow://albion-online-data.com", state.GetAODataIngestBaseURL(), -1)
	}

	publicUploaders := getOrCreateUploaders(PublicIngestBaseUrls)
	privateUploaders := getOrCreateUploaders(ConfigGlobal.PrivateIngestBaseUrls)

	sendMsgToUploaders(data, topic, publicUploaders, state, identifier)
	sendMsgToUploaders(data, topic, privateUploaders, state, identifier)

	// If websockets are enabled, send the data there too
	if ConfigGlobal.EnableWebsockets {
		sendMsgToWebSockets(data, topic)
	}
}

func sendMsgToPrivateUploaders(upload lib.PersonalizedUpload, topic string, state *albionState, identifier string) {
	if ConfigGlobal.DisableUpload {
		log.Info("Upload is disabled.")
		return
	}

	// TODO: Re-enable this when issue #14 is fixed
	// Will personalize with blanks for now in order to allow people to see the format
	// if state.CharacterName == "" || state.CharacterId == "" {
	// 	log.Error("The player name or id has not been set. Please restart the game and make sure the client is running.")
	// 	notification.Push("The player name or id has not been set. Please restart the game and make sure the client is running.")
	// 	return
	// }

	upload.Personalize(state.GetCharacterId(), state.GetCharacterName())

	data, err := json.Marshal(upload)
	if err != nil {
		log.Errorf("Error while marshalling payload for %v: %v", err, topic)
		return
	}

	privateUploaders := getOrCreateUploaders(ConfigGlobal.PrivateIngestBaseUrls)
	if len(privateUploaders) > 0 {
		sendMsgToUploaders(data, topic, privateUploaders, state, identifier)
	}

	// If websockets are enabled, send the data there too
	if ConfigGlobal.EnableWebsockets {
		sendMsgToWebSockets(data, topic)
	}
}

func sendMsgToUploaders(msg []byte, topic string, uploaders []uploader, state *albionState, identifier string) {
	if ConfigGlobal.DisableUpload {
		log.Info("Upload is disabled.")
		return
	}

	for _, u := range uploaders {
		u.sendToIngest(msg, topic, state, identifier)
	}
}

func runHTTPServer() {
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWs(wsHub, w, r)
	})

	err := http.ListenAndServe(":8099", nil)

	if err != nil {
		log.Panic("ListenAndServe: ", err)
	}
}

// sendMsgToWebSockets wraps a pre-serialised JSON payload in a `{"topic":..., "data":...}`
// envelope and hands it to the WS hub. The old implementation used three string
// concatenations (topic, msg→string, closing brace) which allocated three heap
// objects per broadcast — a real cost at 100+ events/sec during ZvZ.
//
// This version builds the envelope in one []byte with a pre-sized buffer, so
// only the final []byte is allocated. `topic` is short and comes from a fixed
// set ("marketorders.deduped" etc.), never player input, so it's safe to emit
// raw without JSON-escaping. `msg` is already valid JSON from json.Marshal.
func sendMsgToWebSockets(msg []byte, topic string) {
	// Fixed overhead of the envelope characters: {"topic":"","data":}
	const envelopeOverhead = len(`{"topic":"","data":}`)
	out := make([]byte, 0, envelopeOverhead+len(topic)+len(msg))
	out = append(out, `{"topic":"`...)
	out = append(out, topic...)
	out = append(out, `","data":`...)
	out = append(out, msg...)
	out = append(out, '}')
	wsHub.broadcast <- out
}
