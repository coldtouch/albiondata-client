package client

import (
	"encoding/gob"
	"os"

	"github.com/ao-data/albiondata-client/log"
	photon "github.com/ao-data/photon-spectator"
)

// routerWorkerCount bounds the number of goroutines processing operations
// concurrently. Prevents goroutine explosion during market-flood or bulk
// vault opens (GC-1 / GC-8).
const (
	routerWorkerCount = 16
	routerQueueSize   = 1024
)

//Router struct definitions
type Router struct {
	albionstate         *albionState
	newOperation        chan operation
	recordPhotonCommand chan photon.PhotonCommand
	quit                chan bool
	workQueue           chan operation
}

func newRouter() *Router {
	return &Router{
		albionstate:         &albionState{LocationId: ""},
		newOperation:        make(chan operation, 1000),
		recordPhotonCommand: make(chan photon.PhotonCommand, 1000),
		quit:                make(chan bool, 1),
		workQueue:           make(chan operation, routerQueueSize),
	}
}

func (r *Router) run() {
	var encoder *gob.Encoder
	var file *os.File
	if ConfigGlobal.RecordPath != "" {
		var err error
		file, err = os.Create(ConfigGlobal.RecordPath)
		if err != nil {
			log.Error("Could not open commands output file ", err)
		} else {
			encoder = gob.NewEncoder(file)
		}
	}

	// Bounded worker pool — goroutines exit when workQueue is closed.
	for i := 0; i < routerWorkerCount; i++ {
		go func() {
			for op := range r.workQueue {
				op.Process(r.albionstate)
			}
		}()
	}

	for {
		select {
		case <-r.quit:
			log.Debug("Closing router...")
			close(r.workQueue) // drains workers gracefully
			if file != nil {
				err := file.Close()
				if err != nil {
					log.Error("Could not close commands output file ", err)
				}
			}
			return
		case op := <-r.newOperation:
			select {
			case r.workQueue <- op:
			default:
				log.Warn("[Router] Work queue full — dropping operation")
			}
		case command := <-r.recordPhotonCommand:
			if encoder != nil {
				err := encoder.Encode(command)
				if err != nil {
					log.Error("Could not encode command ", err)
				}
			}
		}
	}
}
