package types

import "time"

// ReachoutTimelockState describes WhatsApp's account-level reachout restriction.
type ReachoutTimelockState struct {
	IsActive          bool
	EnforcementType   string
	TimeEnforcementEnds time.Time
}

// NewChatMessageCapInfo describes the server-reported new-chat message cap.
// The exact fields may vary by WhatsApp account/experiment; zero values mean
// that the server did not provide a value.
type NewChatMessageCapInfo struct {
	Limit     int
	Used      int
	Remaining int
	ResetAt   time.Time
	IsCapped  bool
}
