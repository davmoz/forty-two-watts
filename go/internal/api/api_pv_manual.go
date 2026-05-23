package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/frahlg/forty-two-watts/go/internal/control"
	"github.com/frahlg/forty-two-watts/go/internal/telemetry"
)

// PV manual-hold endpoint. Pins a PV curtail cap for a bounded
// duration, overriding whatever the planner's slot directive says
// about PVLimitW. Primary use case: operator-side verification that
// the curtail action actually reaches the inverter, without waiting
// for the MPC to organically trigger a negative-price slot.
//
// Body fields:
//   - driver:    optional. "" (or missing) = site-aggregate hold,
//                splits LimitW across SupportsPVCurtail drivers
//                proportionally to live |PV|. Non-empty = scope hold
//                to that one driver only.
//   - limit_w:   optional. Absolute power cap, ≥ 0. Mutually
//                exclusive with limit_pct.
//   - limit_pct: optional. Percent (0–100) of live |PV|. Converted
//                to W using the driver's live reading (driver-scoped
//                hold) or the sum of curtail-capable PV drivers'
//                live |PV| (site-aggregate hold). Mutually exclusive
//                with limit_w.
//   - hold_s:    required, 1..1800.
//
// Sibling of api_battery_manual.go.

type pvManualHoldRequest struct {
	Driver   string   `json:"driver,omitempty"`
	LimitW   *float64 `json:"limit_w,omitempty"`
	LimitPct *float64 `json:"limit_pct,omitempty"`
	HoldS    int      `json:"hold_s"`
}

type pvManualHoldResponse struct {
	Active      bool    `json:"active"`
	Driver      string  `json:"driver,omitempty"`
	LimitW      float64 `json:"limit_w,omitempty"`
	ExpiresAtMs int64   `json:"expires_at_ms,omitempty"`
}

const maxPVManualHoldS = 30 * 60

func (s *Server) handlePVManualHold(w http.ResponseWriter, r *http.Request) {
	if s.deps.Ctrl == nil || s.deps.CtrlMu == nil {
		writeJSON(w, 503, map[string]string{"error": "control state not available"})
		return
	}
	var req pvManualHoldRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if req.HoldS <= 0 {
		writeJSON(w, 400, map[string]string{"error": "hold_s must be > 0"})
		return
	}
	if req.HoldS > maxPVManualHoldS {
		writeJSON(w, 400, map[string]string{"error": "hold_s exceeds maximum (1800)"})
		return
	}
	if (req.LimitW == nil) == (req.LimitPct == nil) {
		writeJSON(w, 400, map[string]string{"error": "exactly one of limit_w or limit_pct required"})
		return
	}

	// Snapshot what we need from State + Tel under their respective locks.
	s.deps.CtrlMu.Lock()
	supports := map[string]bool{}
	for d, v := range s.deps.Ctrl.SupportsPVCurtail {
		if v {
			supports[d] = true
		}
	}
	s.deps.CtrlMu.Unlock()

	if req.Driver != "" && !supports[req.Driver] {
		writeJSON(w, 400, map[string]string{"error": "driver does not advertise pv-curtail support"})
		return
	}
	if len(supports) == 0 {
		writeJSON(w, 400, map[string]string{"error": "no drivers advertise pv-curtail support"})
		return
	}

	limitW, err := resolvePVLimitW(req, supports, s.deps.Tel)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if limitW < 0 {
		limitW = 0
	}

	expires := time.Now().Add(time.Duration(req.HoldS) * time.Second)
	hold := control.PVManualHold{
		Driver:    req.Driver,
		LimitW:    limitW,
		ExpiresAt: expires,
	}
	s.deps.CtrlMu.Lock()
	s.deps.Ctrl.SetPVManualHold(hold)
	s.deps.CtrlMu.Unlock()

	slog.Info("pv manual hold installed",
		"driver", req.Driver,
		"limit_w", limitW,
		"hold_s", req.HoldS,
	)
	writeJSON(w, 200, pvManualHoldResponseFrom(hold, true))
}

func (s *Server) handlePVManualHoldClear(w http.ResponseWriter, r *http.Request) {
	if s.deps.Ctrl == nil || s.deps.CtrlMu == nil {
		writeJSON(w, 503, map[string]string{"error": "control state not available"})
		return
	}
	s.deps.CtrlMu.Lock()
	s.deps.Ctrl.ClearPVManualHold()
	s.deps.CtrlMu.Unlock()
	slog.Info("pv manual hold cleared")
	writeJSON(w, 200, pvManualHoldResponse{Active: false})
}

func (s *Server) handlePVManualHoldGet(w http.ResponseWriter, r *http.Request) {
	if s.deps.Ctrl == nil || s.deps.CtrlMu == nil {
		writeJSON(w, 503, map[string]string{"error": "control state not available"})
		return
	}
	s.deps.CtrlMu.Lock()
	h, active := s.deps.Ctrl.GetPVManualHold(time.Now())
	s.deps.CtrlMu.Unlock()
	writeJSON(w, 200, pvManualHoldResponseFrom(h, active))
}

// resolvePVLimitW converts the request to an absolute watt cap. When
// the operator sent limit_pct, we apply it to the live |PV| of the
// scoped driver (or sum across SupportsPVCurtail drivers for an
// aggregate hold).
func resolvePVLimitW(req pvManualHoldRequest, supports map[string]bool, tel *telemetry.Store) (float64, error) {
	if req.LimitW != nil {
		if *req.LimitW < 0 {
			return 0, apiError("limit_w must be >= 0")
		}
		return *req.LimitW, nil
	}
	pct := *req.LimitPct
	if pct < 0 || pct > 100 {
		return 0, apiError("limit_pct must be in [0, 100]")
	}
	if tel == nil {
		return 0, apiError("telemetry not available; use limit_w instead of limit_pct")
	}
	var basis float64
	for _, r := range tel.ReadingsByType(telemetry.DerPV) {
		if req.Driver != "" {
			if r.Driver != req.Driver {
				continue
			}
		} else if !supports[r.Driver] {
			continue
		}
		if r.RawW >= 0 {
			continue
		}
		basis += -r.RawW
	}
	if basis <= 0 {
		// Don't reject: 0% of 0 W is 0 W, which is a valid "force off"
		// verification command. The pct only matters proportionally.
		return 0, nil
	}
	return basis * pct / 100.0, nil
}

func pvManualHoldResponseFrom(h control.PVManualHold, active bool) pvManualHoldResponse {
	resp := pvManualHoldResponse{Active: active}
	if !active {
		return resp
	}
	resp.Driver = h.Driver
	resp.LimitW = h.LimitW
	if !h.ExpiresAt.IsZero() {
		resp.ExpiresAtMs = h.ExpiresAt.UnixMilli()
	}
	return resp
}
