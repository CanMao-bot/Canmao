package ai

import (
	"context"
	"log"
	"time"

	"gobot/core"
)

type backgroundJob struct {
	ctx      context.Context
	name     string
	deadline time.Time
	fn       func(context.Context)
}

// A single worker bounds auxiliary model concurrency. A full queue drops optional
// learning, never a reply or an explicit remember/forget operation.
func (s *Service) enqueueBackground(ctx context.Context, name string, timeout time.Duration, fn func(context.Context)) bool {
	s.backgroundOnce.Do(func() {
		s.backgroundStarted = s.bot.GoBackground(s.backgroundLoop)
	})
	if !s.backgroundStarted || ctx.Err() != nil {
		return false
	}
	select {
	case s.backgroundJobs <- backgroundJob{ctx, name, time.Now().Add(timeout), fn}:
		return true
	default:
		log.Printf("[ai] background_skipped name=%s reason=queue_full", name)
		return false
	}
}

func (s *Service) backgroundLoop(lifetime context.Context) {
	for {
		select {
		case <-lifetime.Done():
			return
		case job := <-s.backgroundJobs:
			if lifetime.Err() != nil {
				return
			}
			s.runBackgroundJob(lifetime, job)
		}
	}
}

func (s *Service) runBackgroundJob(lifetime context.Context, job backgroundJob) {
	defer func() {
		if recover() != nil {
			log.Printf("[ai] background_failed name=%s reason=panic", job.name)
		}
	}()
	ctx, cancel := context.WithDeadline(job.ctx, job.deadline)
	defer cancel()
	stop := context.AfterFunc(lifetime, cancel)
	defer stop()
	if ctx.Err() != nil || s.checkRunGroupAI(ctx) != nil {
		return
	}
	job.fn(ctx)
}

func (s *Service) scheduleMood(ctx context.Context, ev *core.Event) {
	if s.moodMgr == nil || !s.cfg.Mood.Enabled {
		return
	}
	ctx = core.WithEvent(ctx, ev)
	ctx = context.WithValue(ctx, groupAIGuardKey{}, ev.IsGroup())
	s.enqueueBackground(ctx, "mood", 8*time.Second, func(jobCtx context.Context) {
		state := s.moodMgr.DetectAndApplyContext(jobCtx, ev.Text())
		if state == nil || jobCtx.Err() != nil || s.checkRunGroupAI(jobCtx) != nil {
			return
		}
		// The target comes exclusively from the real message event, never the LLM.
		if ev.IsGroup() && (state.Value < s.moodMgr.punishMoodThreshold || state.Emotion == "angry") {
			s.moodMgr.NoteAggressor(ev.GroupID, ev.UserID)
		}
	})
}
