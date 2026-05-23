package api

import (
	"context"
	"net/http"
	"time"
)

// POST /api/loadpoints/{id}/force_start fires the generic
// `charge_start` action on the loadpoint's bound vehicle driver,
// bypassing the auto-wake's cooldown + stretched-backoff throttle.
//
// Used when the auto-wake has given up (5+ failed attempts → 10 min
// cooldown) and the operator knows the car is now reachable — e.g.
// they just woke it from the Tesla app or plug-cycled — and want
// charging to resume immediately rather than waiting for the next
// auto-wake window. Bound timeout (15 s) is enough for a Tesla BLE
// proxy hop including a one-shot wake; longer roundtrips return the
// proxy's error to the caller.
//
// Generic across vehicle drivers: any driver that implements the
// cross-driver `charge_start` (or its `ev_start` alias) action picks
// this up unchanged. The handler delegates to
// loadpoint.Controller.ForceStartVehicle, which owns the throttle
// reset + wake-kick arming so behaviour is identical to the auto-wake
// path minus the cooldown gate.
func (s *Server) handleLoadpointForceStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, 400, map[string]string{"error": "loadpoint id required"})
		return
	}
	if s.deps.LoadpointCtrl == nil {
		writeJSON(w, 503, map[string]string{"error": "loadpoint controller not configured"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	driver, err := s.deps.LoadpointCtrl.ForceStartVehicle(ctx, id)
	if driver == "" && err == nil {
		writeJSON(w, 404, map[string]string{"error": "no vehicle driver bound to loadpoint"})
		return
	}
	if err != nil {
		writeJSON(w, 502, map[string]string{
			"error":  "vehicle driver send failed",
			"driver": driver,
			"detail": err.Error(),
		})
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok":             true,
		"loadpoint_id":   id,
		"vehicle_driver": driver,
		"action":         "charge_start",
	})
}
