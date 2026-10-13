// Package printer holds the vendor-neutral printer vocabulary shared by the server and
// the edge agent. It has no dependencies so either side can import it freely.
package printer

// State is the coarse, vendor-neutral state of a printer.
// The raw vendor codes stay available alongside it so decoding can be tuned per model.
type State string

const (
	StateUnknown   State = "unknown"
	StateIdle      State = "idle"
	StatePrinting  State = "printing"
	StateBusy      State = "busy"
	StateMediaLow  State = "media_low"
	StateMediaOut  State = "media_out"
	StateRibbonLow State = "ribbon_low"
	StateRibbonOut State = "ribbon_out"
	StateHeadError State = "head_error"
	StateError     State = "error"
	StateOffline   State = "offline"
	StateDisabled  State = "disabled"

	// Receipt and office printers.
	StateDoorOpen State = "door_open"
	StatePaperJam State = "paper_jam"
	StateTonerLow State = "toner_low"
	StateTonerOut State = "toner_out"
)

// BlocksUse reports whether the state represents an active problem that blocks use.
func (s State) BlocksUse() bool {
	switch s {
	case StateMediaOut, StateRibbonOut, StateHeadError, StateError, StateOffline, StateDisabled,
		StateDoorOpen, StatePaperJam, StateTonerOut:
		return true
	}
	return false
}

// Busy reports whether the printer is currently producing output.
func (s State) Busy() bool {
	return s == StatePrinting || s == StateBusy
}
