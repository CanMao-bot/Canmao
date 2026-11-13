package perm

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
)

// GroupAISettings has independent owner authorization and optional trigger overrides.
type GroupAISettings struct {
	Enabled          bool
	EnabledBy        int64
	MentionOnly      *bool
	ReplyProbability *float64
}
type GroupAIPatch struct {
	Enabled          *bool
	MentionOnly      *bool
	ReplyProbability *float64
	ResetTriggers    bool
}

func (s *Store) GroupAI(groupID int64) (GroupAISettings, error) {
	var g GroupAISettings
	var enabled int
	var mention sql.NullBool
	var probability sql.NullFloat64
	err := s.db.QueryRow(`SELECT g.ai_enabled,COALESCE(p.enabled_by,0),p.mention_only,p.reply_probability FROM groups g LEFT JOIN group_ai_policy p ON p.group_id=g.group_id WHERE g.group_id=?`, groupID).Scan(&enabled, &g.EnabledBy, &mention, &probability)
	if errors.Is(err, sql.ErrNoRows) {
		return g, nil
	}
	if err != nil {
		return g, err
	}
	g.Enabled = enabled != 0 && g.EnabledBy > 0
	if mention.Valid {
		g.MentionOnly = &mention.Bool
	}
	if probability.Valid {
		g.ReplyProbability = &probability.Float64
	}
	return g, nil
}

// ConfigureGroupAI is deliberately distinct from bot/group administrator settings.
// Caller supplies the actual event sender and the owner from trusted configuration.
func (s *Store) ConfigureGroupAI(groupID, actorID, masterID int64, patch GroupAIPatch) error {
	if groupID <= 0 || masterID <= 0 || actorID != masterID {
		return errors.New("仅配置中的主人可修改群 AI 设置")
	}
	if patch.ReplyProbability != nil {
		p := *patch.ReplyProbability
		if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
			return errors.New("回复概率须为0到1之间的数值")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Specify both flags so an old database DEFAULT 1 cannot enable either capability.
	if _, err = tx.Exec(`INSERT OR IGNORE INTO groups(group_id,ai_enabled,bot_enabled,updated_at) VALUES(?,0,0,unixepoch())`, groupID); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO group_ai_policy(group_id,enabled_by) VALUES(?,0)`, groupID); err != nil {
		return err
	}
	if patch.Enabled != nil {
		if _, err = tx.Exec(`UPDATE groups SET ai_enabled=?,updated_at=unixepoch() WHERE group_id=?`, *patch.Enabled, groupID); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE group_ai_policy SET enabled_by=? WHERE group_id=?`, actorID, groupID); err != nil {
			return err
		}
	}
	if patch.ResetTriggers {
		if _, err = tx.Exec(`UPDATE group_ai_policy SET mention_only=NULL,reply_probability=NULL WHERE group_id=?`, groupID); err != nil {
			return err
		}
	}
	if patch.MentionOnly != nil {
		if _, err = tx.Exec(`UPDATE group_ai_policy SET mention_only=? WHERE group_id=?`, *patch.MentionOnly, groupID); err != nil {
			return err
		}
	}
	if patch.ReplyProbability != nil {
		if _, err = tx.Exec(`UPDATE group_ai_policy SET reply_probability=? WHERE group_id=?`, *patch.ReplyProbability, groupID); err != nil {
			return err
		}
	}
	detail := ""
	if patch.Enabled != nil {
		detail += fmt.Sprintf("enabled=%v ", *patch.Enabled)
	}
	if patch.MentionOnly != nil {
		detail += fmt.Sprintf("mention_only=%v ", *patch.MentionOnly)
	}
	if patch.ReplyProbability != nil {
		detail += fmt.Sprintf("probability=%g ", *patch.ReplyProbability)
	}
	if patch.ResetTriggers {
		detail += "reset_triggers"
	}
	if _, err = tx.Exec(`INSERT INTO group_ai_audit(group_id,actor_id,detail,at) VALUES(?,?,?,unixepoch())`, groupID, actorID, detail); err != nil {
		return err
	}
	return tx.Commit()
}
