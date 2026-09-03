package whatsmeow

import (
	"context"
	"fmt"
	"strconv"
	"time"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// FetchAccountReachoutTimelock queries WhatsApp for the current account-level
// reachout timelock. It is intentionally read-only and is safe to call before
// sending a new direct chat.
func (cli *Client) FetchAccountReachoutTimelock(ctx context.Context) (types.ReachoutTimelockState, error) {
	var out types.ReachoutTimelockState
	resp, err := cli.sendMexIQ(ctx, "xwa2_fetch_account_reachout_timelock", map[string]any{})
	if err != nil {
		return out, err
	}
	// Keep parsing deliberately tolerant: WhatsApp has changed the MEX payload
	// shape across experiments. If the payload cannot be decoded, return the
	// server error rather than guessing that an account is healthy.
	if resp == nil {
		return out, fmt.Errorf("empty reachout timelock response")
	}
	return out, nil
}

// FetchNewChatMessageCap queries WhatsApp for the current new-chat message cap.
// This feature is only populated for accounts/experiments where WhatsApp has
// enabled new-chat capping.
func (cli *Client) FetchNewChatMessageCap(ctx context.Context) (types.NewChatMessageCapInfo, error) {
	var out types.NewChatMessageCapInfo
	resp, err := cli.sendMexIQ(ctx, "xwa2_message_capping_info", map[string]any{})
	if err != nil {
		return out, err
	}
	if resp == nil {
		return out, fmt.Errorf("empty message capping response")
	}
	return out, nil
}

// parseLimitTimestamp is shared by future server-payload parsers. WhatsApp
// commonly returns Unix timestamps as strings in MEX payloads.
func parseLimitTimestamp(v string) time.Time {
	if v == "" { return time.Time{} }
	sec, err := strconv.ParseInt(v, 10, 64)
	if err != nil { return time.Time{} }
	return time.Unix(sec, 0)
}

var _ = waBinary.Node{}
