package sched

import (
	"context"
	"log"
	"time"

	"gobot/core"
	"gobot/store/sched"
)

// Scheduler 定时任务服务: AI 可自主创建定时提醒/消息
type Scheduler struct {
	store *sched.Store
	bot   *core.Bot
	stop  chan struct{}
}

func New(st *sched.Store, bot *core.Bot) *Scheduler {
	return &Scheduler{store: st, bot: bot, stop: make(chan struct{})}
}

func (s *Scheduler) Name() string { return "scheduler" }

// Start 启动调度循环
func (s *Scheduler) Start() {
	go s.loop()
}

func (s *Scheduler) loop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.runDue()
		case <-s.stop:
			return
		}
	}
}

func (s *Scheduler) Close() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
}

func (s *Scheduler) runDue() {
	now := time.Now().Unix()
	tasks, err := s.store.Due(now)
	if err != nil {
		log.Printf("[sched] 查询到期任务失败: %v", err)
		return
	}
	for _, t := range tasks {
		s.execute(t)
	}
}

// execute 执行定时任务
func (s *Scheduler) execute(t *sched.Task) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 发送消息
	msg := []core.Segment{core.TextSegment(t.Content)}
	var sendErr error
	if t.Scope == "group" {
		sendErr = s.bot.Sender.SendGroupMsg(t.GroupID, t.UserID, msg)
	} else {
		sendErr = s.bot.Sender.SendPrivateMsg(t.UserID, msg)
	}
	if sendErr != nil {
		log.Printf("[sched] 任务#%d 发送失败: %v", t.ID, sendErr)
	}

	// 重复任务: 更新下次时间
	if t.Type == "repeat" && t.Interval > 0 {
		next := t.TargetAt + t.Interval
		if next <= time.Now().Unix() {
			next = time.Now().Unix() + t.Interval
		}
		if err := s.store.UpdateTarget(t.ID, next, t.RunCount+1); err != nil {
			log.Printf("[sched] 任务#%d 更新时间失败: %v", t.ID, err)
		}
		log.Printf("[sched] 重复任务#%d 下次: %s", t.ID, sched.FormatTime(next))
	} else {
		// 一次性任务: 停用
		if err := s.store.Disable(t.ID); err != nil {
			log.Printf("[sched] 任务#%d 停用失败: %v", t.ID, err)
		}
		log.Printf("[sched] 一次性任务#%d 已执行: %s", t.ID, t.Content)
	}
	_ = ctx
}

// Handle 处理定时命令: /sched list /sched cancel
func (s *Scheduler) Handle(ctx context.Context, bot *core.Bot, ev *core.Event) bool {
	if ev.Type != "message" {
		return false
	}
	// 命令处理由 AI 服务统一做, 这里提供取消命令
	return false
}
