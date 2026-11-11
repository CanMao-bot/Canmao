package community

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gobot/core"
)

// Friend consent is a business workflow, independent of Agent tool approval.
type friendConfirmation struct {
	owner     int64
	expiresAt time.Time
	result    chan bool
	messageID int64
}

func (s *Service) HandleControl(ctx context.Context, bot *core.Bot, ev *core.Event) bool {
	if ctx.Err() != nil || ev.Type != "message" || !ev.IsPrivate() || !s.master(ev.UserID) || !s.AllowEvent(ev) {
		return false
	}
	command := strings.TrimSpace(ev.Text())
	if ev.ReplyID() == 0 || (command != "同意" && command != "拒绝") {
		return false
	}
	s.friendMu.Lock()
	var req *friendConfirmation
	var nonce string
	for key, pending := range s.friendRequests {
		if pending.messageID == ev.ReplyID() {
			req = pending
			nonce = key
			break
		}
	}
	if req == nil || req.owner != ev.UserID || !time.Now().Before(req.expiresAt) {
		s.friendMu.Unlock()
		return false
	}
	delete(s.friendRequests, nonce)
	req.result <- command == "同意"
	s.friendMu.Unlock()
	return true
}

func (s *Service) friendRequest(ctx context.Context, ev *core.Event) bool {
	if ctx.Err() != nil {
		return true
	}
	select {
	case s.friendSlots <- struct{}{}:
		defer func() { <-s.friendSlots }()
	default:
		return true
	}
	c, ok := s.bot.Sender.(friendApprover)
	if !ok {
		return true
	}
	owner, err := strconv.ParseInt(s.bot.Cfg.Bot.MasterID, 10, 64)
	if err != nil || owner <= 0 || ev.UserID <= 0 || ev.Flag == "" {
		return true
	}
	var randomID [8]byte
	if _, err = rand.Read(randomID[:]); err != nil {
		return true
	}
	ok, err = s.store.Claim("friend:"+ev.Flag, s.now())
	if err != nil || !ok {
		return true
	}
	nonce := hex.EncodeToString(randomID[:])
	req := &friendConfirmation{owner: owner, expiresAt: time.Now().Add(s.friendTimeout), result: make(chan bool, 1)}
	s.friendMu.Lock()
	s.friendRequests[nonce] = req
	s.friendMu.Unlock()
	defer func() {
		s.friendMu.Lock()
		delete(s.friendRequests, nonce)
		s.friendMu.Unlock()
	}()
	comment := []rune(ev.Comment)
	if len(comment) > 500 {
		comment = comment[:500]
	}
	msg := fmt.Sprintf("【好友申请】\n申请人：%d\n验证信息：%s\n请主人引用本条消息回复「同意」或「拒绝」。", ev.UserID, string(comment))
	s.friendMu.Lock()
	req.messageID, err = s.bot.SendTracked(ctx, &core.Event{DetailType: "private", UserID: owner}, msg)
	s.friendMu.Unlock()
	if err != nil {
		return true
	}
	timer := time.NewTimer(s.friendTimeout)
	defer timer.Stop()
	var approved bool
	select {
	case approved = <-req.result:
	case <-timer.C:
		_ = s.store.Audit(0, owner, "friend_not_executed", id(ev.UserID))
		return true
	case <-ctx.Done():
		return true
	}
	if ctx.Err() != nil || !s.master(owner) || !time.Now().Before(req.expiresAt) || !s.AllowEvent(ev) {
		return true
	}
	if err = s.store.Audit(0, owner, "friend_executing", id(ev.UserID)); err != nil {
		return true
	}
	err = c.SetFriendAddRequest(ev.Flag, approved, "")
	status := "friend_rejected"
	if approved {
		status = "friend_approved"
	}
	if err != nil {
		status = "friend_failed_or_uncertain"
	}
	_ = s.store.Audit(0, owner, status, id(ev.UserID))
	copyEv := *ev
	copyEv.Type, copyEv.DetailType, copyEv.UserID, copyEv.GroupID = "message", "private", owner, 0
	s.respond(&copyEv, "好友申请已处理", err)
	return true
}
