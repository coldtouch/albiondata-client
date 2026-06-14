package client

import (
	"fmt"
	"reflect"

	"github.com/ao-data/albiondata-client/log"
)

// dumpJoinParams logs every parameter on an OpJoin / opChangeCluster response
// for reverse-engineering. Originally written to identify which param carried
// the zone name after the April 2026 opcode shift (resolved 2026-04-27 — param
// 8 for OpJoin, param 0 for opChangeCluster).
//
// Now gated on ConfigGlobal.Debug since opChangeCluster fires on EVERY zone
// transition (multiple per minute in ZvZ portal fights) — running unconditionally
// would log 30+ INFO lines per cluster change in production. The reflect.ValueOf
// loop + log.Infof per param is too expensive for a hot path.
//
// Re-enable for the next protocol-shift investigation by running with --debug.
func dumpJoinParams(label string, code int16, params map[uint8]interface{}) {
	if !ConfigGlobal.Debug {
		return
	}
	log.Infof("[ZONE-DIAG] %s opcode=%d — %d params:", label, code, len(params))
	for k, v := range params {
		if k == 253 || k == 255 {
			continue
		}
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
			n := rv.Len()
			preview := ""
			for i := 0; i < n && i < 5; i++ {
				if i > 0 {
					preview += ", "
				}
				preview += fmt.Sprintf("%v", rv.Index(i).Interface())
			}
			if n > 5 {
				preview += fmt.Sprintf("... (%d more)", n-5)
			}
			log.Infof("[ZONE-DIAG]   %d: %T len=%d [%s]", k, v, n, preview)
		} else {
			log.Infof("[ZONE-DIAG]   %d: %T = %v", k, v, v)
		}
	}
}
