package community

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gobot/core"
	data "gobot/store/community"
)

type crop struct {
	name          string
	price, reward int64
	grow          time.Duration
}

var crops = []crop{{"小麦", 20, 50, 5 * time.Minute}, {"土豆", 60, 140, 20 * time.Minute}, {"南瓜", 150, 350, time.Hour}}

type mount struct {
	name         string
	price, bonus int64
}

var mounts = []mount{{"纸飞机", 500, 5}, {"云朵猫", 2000, 10}, {"星河鲸", 10000, 20}}

type rod struct {
	name       string
	price      int64
	durability int
}

var rods = []rod{{"木鱼竿", 150, 30}, {"银鱼竿", 1000, 100}}

func daily(seed string, max int) int {
	v := sha256.Sum256([]byte(seed))
	return int(binary.BigEndian.Uint64(v[:8]) % uint64(max))
}
func amount(text string) (int64, error) {
	v, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	if err != nil || v <= 0 || v > data.MaxMoney {
		return 0, errors.New("数量须为 1 到 1000000000000 之间的整数")
	}
	return v, nil
}
func target(text string) (int64, error) {
	v, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	if err != nil || v <= 0 {
		return 0, errors.New("请使用有效的用户编号")
	}
	return v, nil
}
func (s *Service) entertainment(ev *core.Event, text string) bool {
	feature := ""
	cmd, arg := split(text)
	switch cmd {
	case "签到", "查询":
		feature = "sign"
	case "今日人品", "抽签":
		feature = "fortune"
	case "赠送":
		feature = "transfer"
	case "存款", "取款", "我的存款":
		feature = "bank"
	case "我的发言":
		feature = "speech"
	case "金币排行", "货币排行", "经验排行", "银行排行", "签到排行", "连续签到排行", "今日发言排行", "本月发言排行", "累计发言排行", "钓鱼排行":
		feature = "rank"
	case "我的农场", "种子商店", "开耕", "种植", "全部种植", "农场收割", "农场全部收割":
		feature = "farm"
	case "坐骑商店", "购买坐骑", "我的坐骑", "当前坐骑", "设置坐骑", "取消坐骑", "查看坐骑":
		feature = "mount"
	case "鱼竿商店", "购买鱼竿", "我的鱼竿", "钓鱼", "提竿":
		feature = "fishing"
	default:
		if strings.HasPrefix(text, ".r") || dicePattern.MatchString(text) {
			feature = "dice"
			cmd = "dice"
		} else {
			// Compact aliases used by the original plugin, with an unambiguous fixed prefix.
			for _, prefix := range []string{"全部种植", "农场收割", "购买坐骑", "设置坐骑", "查看坐骑", "购买鱼竿", "开耕", "种植"} {
				if strings.HasPrefix(text, prefix) && len(text) > len(prefix) {
					return s.entertainment(ev, prefix+" "+strings.TrimSpace(strings.TrimPrefix(text, prefix)))
				}
			}
			return false
		}
	}
	if !s.enabled(ev.GroupID, feature) {
		s.bot.Reply(ev, "此功能当前未开启")
		return true
	}
	scope := s.scope(ev)
	now := s.now().In(s.loc)
	day := now.Format("2006-01-02")
	change := func(action string, fn func(*data.Account) (string, error)) {
		msg, err := s.store.Change(scope, []int64{ev.UserID}, ev.UserID, action, func(m map[int64]*data.Account) (string, error) { return fn(m[ev.UserID]) })
		s.respond(ev, msg, err)
	}
	switch cmd {
	case "签到":
		change("sign", func(a *data.Account) (string, error) {
			if a.LastSign == day {
				return "今天已签到，明天再来吧", nil
			}
			if a.LastSign == now.AddDate(0, 0, -1).Format("2006-01-02") {
				a.Streak++
			} else {
				a.Streak = 1
			}
			a.LastSign = day
			a.Signs++
			seed := scope + ":" + id(ev.UserID) + ":" + day
			coins := int64(100 + daily(seed+"coins", 101))
			xp := int64(10 + daily(seed+"xp", 11))
			for _, m := range mounts {
				if a.Mount == m.name {
					coins += m.bonus
				}
			}
			a.Coins += coins
			a.XP += xp
			return fmt.Sprintf("签到成功！金币 +%d，经验 +%d\n连续 %d 天，累计 %d 天；余额 %d", coins, xp, a.Streak, a.Signs, a.Coins), nil
		})
	case "查询", "我的存款", "我的发言", "我的坐骑", "当前坐骑", "我的鱼竿", "我的农场":
		a, err := s.store.Account(scope, ev.UserID)
		if err != nil {
			s.respond(ev, "", err)
			return true
		}
		msg := ""
		switch cmd {
		case "查询":
			msg = fmt.Sprintf("金币 %d | 银行 %d\n经验 %d | 等级 %d\n连续签到 %d 天 | 累计 %d 天", a.Coins, a.Bank, a.XP, a.XP/100+1, currentStreak(a, now), a.Signs)
		case "我的存款":
			msg = fmt.Sprintf("银行存款：%d（无利息、无真实货币）", a.Bank)
		case "我的发言":
			if a.SpeechDay != day {
				a.Today = 0
			}
			if a.SpeechMonth != now.Format("2006-01") {
				a.Month = 0
			}
			msg = fmt.Sprintf("今日 %d 条 | 本月 %d 条 | 累计 %d 条\n发言奖励每30秒最多一次，每日最多30次", a.Today, a.Month, a.Total)
		case "我的坐骑":
			msg = "拥有坐骑：" + strings.Join(a.Mounts, "、")
			if len(a.Mounts) == 0 {
				msg = "尚未拥有坐骑，请查看「坐骑商店」"
			}
		case "当前坐骑":
			msg = "当前坐骑：" + a.Mount
			if a.Mount == "" {
				msg = "当前未设置坐骑"
			}
		case "我的鱼竿":
			msg = fmt.Sprintf("鱼竿：%s | 耐久 %d\n累计钓鱼 %d 条", a.Rod, a.Durability, a.Fish)
			if a.BiteAt > 0 {
				msg += "\n鱼线已抛出，请留意提竿时间"
			}
		case "我的农场":
			if len(a.Plots) == 0 {
				a.Plots = []data.Plot{{}}
			}
			lines := []string{"我的农场（最多9格）"}
			for i, p := range a.Plots {
				state := "空地"
				if p.Crop != "" {
					if p.Ready <= now.Unix() {
						state = p.Crop + " 已成熟"
					} else {
						state = fmt.Sprintf("%s 还需 %s", p.Crop, time.Duration(p.Ready-now.Unix())*time.Second)
					}
				}
				lines = append(lines, fmt.Sprintf("%d. %s", i+1, state))
			}
			msg = strings.Join(lines, "\n")
		}
		s.bot.Reply(ev, msg)
	case "今日人品":
		s.bot.Reply(ev, fmt.Sprintf("今日人品：%d / 100（每日固定）", daily(scope+id(ev.UserID)+day+"luck", 101)))
	case "抽签":
		signs := []string{"大吉：今天适合迈出第一步", "中吉：耐心会带来好结果", "小吉：留心身边的小惊喜", "平：照顾好自己，稳稳前进", "末吉：放慢脚步，明天还有机会"}
		s.bot.Reply(ev, signs[daily(scope+id(ev.UserID)+day+"fortune", len(signs))]+"\n仅供娱乐")
	case "dice":
		msg, err := roll(text)
		s.respond(ev, msg, err)
	case "赠送":
		if !ev.IsGroup() {
			s.bot.Reply(ev, "赠送请在群内使用，双方必须是该群成员")
			return true
		}
		f := strings.Fields(arg)
		if at := ev.AtQQs(); len(at) > 0 && len(f) == 1 {
			f = append([]string{id(at[0])}, f...)
		}
		if len(f) != 2 {
			s.bot.Reply(ev, "用法：赠送 对方QQ 数量（最低100）")
			return true
		}
		uid, err := target(f[0])
		if err != nil {
			s.respond(ev, "", err)
			return true
		}
		n, err := amount(f[1])
		if err != nil || n < 100 {
			s.bot.Reply(ev, "转账数量须为100到1000000000000之间的整数")
			return true
		}
		if uid == ev.UserID || uid == ev.SelfID || !s.member(ev.GroupID, uid) {
			s.bot.Reply(ev, "只能赠送给本群其他成员")
			return true
		}
		fee := n * int64(s.bot.Cfg.Community.TransferFeeBPS) / 10000
		msg, err := s.store.Change(scope, []int64{ev.UserID, uid}, ev.UserID, "transfer", func(m map[int64]*data.Account) (string, error) {
			from, to := m[ev.UserID], m[uid]
			if from.Coins < n {
				return "", errors.New("金币不足")
			}
			from.Coins -= n
			to.Coins += n - fee
			return fmt.Sprintf("已赠送 %s %d 金币，手续费 %d；余额 %d", id(uid), n-fee, fee, from.Coins), nil
		})
		s.respond(ev, msg, err)
	case "存款", "取款":
		n, err := amount(arg)
		if err != nil {
			s.respond(ev, "", err)
			return true
		}
		change("bank", func(a *data.Account) (string, error) {
			if cmd == "存款" {
				if a.Coins < n {
					return "", errors.New("金币不足")
				}
				a.Coins -= n
				a.Bank += n
			} else {
				if a.Bank < n {
					return "", errors.New("存款不足")
				}
				a.Bank -= n
				a.Coins += n
			}
			return fmt.Sprintf("%s成功；金币 %d，存款 %d", cmd, a.Coins, a.Bank), nil
		})
	case "金币排行", "货币排行", "经验排行", "银行排行", "签到排行", "连续签到排行", "今日发言排行", "本月发言排行", "累计发言排行", "钓鱼排行":
		field := map[string]string{"金币排行": "coins", "货币排行": "coins", "经验排行": "xp", "银行排行": "bank", "签到排行": "signs", "连续签到排行": "streak", "今日发言排行": "today", "本月发言排行": "month", "累计发言排行": "total", "钓鱼排行": "fish"}[cmd]
		var list []data.Ranked
		var err error
		if field == "today" || field == "month" {
			period := day
			if field == "month" {
				period = now.Format("2006-01")
			}
			list, err = s.store.SpeechRank(scope, field, period)
		} else if field == "streak" {
			list, err = s.store.StreakRank(scope, day, now.AddDate(0, 0, -1).Format("2006-01-02"))
		} else {
			list, err = s.store.Rank(scope, field)
		}
		lines := []string{cmd + "（当前数据范围，前10名）"}
		for i, r := range list {
			lines = append(lines, fmt.Sprintf("%d. %d：%d", i+1, r.UserID, r.Value))
		}
		if len(list) == 0 {
			lines = append(lines, "暂无记录")
		}
		s.respond(ev, strings.Join(lines, "\n"), err)
	case "种子商店":
		lines := []string{"种子商店"}
		for _, c := range crops {
			lines = append(lines, fmt.Sprintf("%s：种子%d，收获%d，成熟%s", c.name, c.price, c.reward, c.grow))
		}
		lines = append(lines, "开耕：首格免费，其余每格500；最多9格")
		s.bot.Reply(ev, strings.Join(lines, "\n"))
	case "开耕", "种植", "全部种植", "农场收割", "农场全部收割":
		change("farm", func(a *data.Account) (string, error) { return farm(a, cmd, arg, now) })
	case "坐骑商店", "查看坐骑":
		lines := []string{"坐骑商店（装备后每日签到额外奖励）"}
		for _, m := range mounts {
			if cmd == "查看坐骑" && m.name != arg {
				continue
			}
			lines = append(lines, fmt.Sprintf("%s：%d金币，签到额外+%d", m.name, m.price, m.bonus))
		}
		s.bot.Reply(ev, strings.Join(lines, "\n"))
	case "购买坐骑", "设置坐骑", "取消坐骑":
		change("mount", func(a *data.Account) (string, error) {
			if cmd == "取消坐骑" {
				a.Mount = ""
				return "已取消坐骑", nil
			}
			var item *mount
			for i := range mounts {
				if mounts[i].name == arg {
					item = &mounts[i]
				}
			}
			if item == nil {
				return "", errors.New("坐骑不存在，请查看坐骑商店")
			}
			owned := false
			for _, n := range a.Mounts {
				if n == arg {
					owned = true
				}
			}
			if cmd == "设置坐骑" {
				if !owned {
					return "", errors.New("尚未拥有该坐骑")
				}
				a.Mount = arg
				return "已设置坐骑：" + arg, nil
			}
			if owned {
				return "", errors.New("已拥有此坐骑")
			}
			if a.Coins < item.price {
				return "", errors.New("金币不足")
			}
			a.Coins -= item.price
			a.Mounts = append(a.Mounts, arg)
			return "已购买：" + arg + "，可用「设置坐骑 " + arg + "」装备", nil
		})
	case "鱼竿商店":
		lines := []string{"鱼竿商店（替换旧鱼竿不会退费）"}
		for _, r := range rods {
			lines = append(lines, fmt.Sprintf("%s：%d金币，%d耐久", r.name, r.price, r.durability))
		}
		s.bot.Reply(ev, strings.Join(lines, "\n"))
	case "购买鱼竿", "钓鱼", "提竿":
		change("fishing", func(a *data.Account) (string, error) { return fishing(a, cmd, arg, now) })
	}
	return true
}
func split(text string) (string, string) {
	f := strings.Fields(text)
	if len(f) == 0 {
		return "", ""
	}
	return f[0], strings.TrimSpace(strings.TrimPrefix(text, f[0]))
}
func currentStreak(a data.Account, now time.Time) int {
	if a.LastSign != now.Format("2006-01-02") && a.LastSign != now.AddDate(0, 0, -1).Format("2006-01-02") {
		return 0
	}
	return a.Streak
}
func farm(a *data.Account, cmd, arg string, now time.Time) (string, error) {
	if len(a.Plots) == 0 {
		a.Plots = []data.Plot{{}}
	}
	if cmd == "开耕" {
		n, err := strconv.Atoi(arg)
		if err != nil || n != len(a.Plots)+1 || n > 9 {
			return "", errors.New("请按顺序开耕下一格（2至9）")
		}
		if a.Coins < 500 {
			return "", errors.New("开耕需要500金币")
		}
		a.Coins -= 500
		a.Plots = append(a.Plots, data.Plot{})
		return fmt.Sprintf("第%d格已开耕", n), nil
	}
	all := cmd == "全部种植" || cmd == "农场全部收割"
	plant := cmd == "种植" || cmd == "全部种植"
	index := -1
	cropName := arg
	if !all {
		f := strings.Fields(strings.ReplaceAll(arg, "-", " "))
		n := 0
		var err error
		if len(f) > 0 {
			n, err = strconv.Atoi(f[0])
		}
		if len(f) == 0 || err != nil || n < 1 || n > len(a.Plots) {
			return "", errors.New("格号无效，请查看我的农场")
		}
		index = n - 1
		if plant {
			if len(f) != 2 {
				return "", errors.New("用法：种植 格号 作物")
			}
			cropName = f[1]
		}
	}
	var item *crop
	if plant {
		for i := range crops {
			if crops[i].name == cropName {
				item = &crops[i]
			}
		}
		if item == nil {
			return "", errors.New("未知作物，请查看种子商店")
		}
	}
	count := 0
	gain := int64(0)
	for i := range a.Plots {
		if !all && i != index {
			continue
		}
		p := &a.Plots[i]
		if plant {
			if p.Crop != "" {
				if !all {
					return "", errors.New("此格已有作物，请先收割")
				}
				continue
			}
			if a.Coins < item.price {
				if !all {
					return "", errors.New("金币不足")
				}
				break
			}
			a.Coins -= item.price
			*p = data.Plot{Crop: item.name, Ready: now.Add(item.grow).Unix()}
			count++
		} else {
			if p.Crop == "" || p.Ready > now.Unix() {
				continue
			}
			for _, c := range crops {
				if c.name == p.Crop {
					gain += c.reward
				}
			}
			*p = data.Plot{}
			count++
		}
	}
	if count == 0 {
		return "", errors.New("没有可操作的地块或金币不足")
	}
	a.Coins += gain
	if plant {
		return fmt.Sprintf("已种植%d格%s", count, item.name), nil
	}
	return fmt.Sprintf("收割%d格，获得%d金币", count, gain), nil
}
func fishing(a *data.Account, cmd, arg string, now time.Time) (string, error) {
	if cmd == "购买鱼竿" {
		if a.EscapeAt > now.Unix() {
			return "", errors.New("请先完成当前钓鱼")
		}
		for _, r := range rods {
			if r.name == arg {
				if a.Coins < r.price {
					return "", errors.New("金币不足")
				}
				a.Coins -= r.price
				a.Rod = r.name
				a.Durability = r.durability
				a.BiteAt = 0
				a.EscapeAt = 0
				return "已购买：" + r.name, nil
			}
		}
		return "", errors.New("未知鱼竿，请查看鱼竿商店")
	}
	if cmd == "钓鱼" {
		if a.EscapeAt > now.Unix() {
			return "", errors.New("已经在钓鱼，请使用提竿")
		}
		if a.Durability <= 0 {
			return "", errors.New("请先购买有耐久的鱼竿")
		}
		a.Durability--
		wait := 30 + rand.IntN(61)
		a.BiteAt = now.Add(time.Duration(wait) * time.Second).Unix()
		a.EscapeAt = a.BiteAt + 120
		return fmt.Sprintf("已抛竿！%d秒后鱼会咬钩，此后2分钟内「提竿」。剩余耐久%d", wait, a.Durability), nil
	}
	if a.BiteAt == 0 {
		return "", errors.New("请先使用钓鱼抛竿")
	}
	if now.Unix() < a.BiteAt {
		return fmt.Sprintf("还没咬钩，还需%d秒", a.BiteAt-now.Unix()), nil
	}
	if now.Unix() > a.EscapeAt {
		a.BiteAt = 0
		a.EscapeAt = 0
		return "鱼已脱钩，可以重新钓鱼", nil
	}
	reward := int64(30 + rand.IntN(91))
	a.Coins += reward
	a.Fish++
	a.BiteAt = 0
	a.EscapeAt = 0
	return fmt.Sprintf("钓到一条鱼，兑换%d金币！累计%d条", reward, a.Fish), nil
}

var dicePattern = regexp.MustCompile(`^\.(?:(\d*)r)?(\d*)d(\d*)((?:[+-]\d+){0,10})$`)
var modifierPattern = regexp.MustCompile(`[+-]\d+`)

func roll(text string) (string, error) {
	m := dicePattern.FindStringSubmatch(text)
	bad := errors.New("骰子用法：.rd / .r3d6 / .2r5d20+3；最多10轮、共100颗，面数2至1000，修正绝对值不超过1000000")
	if m == nil {
		return "", bad
	}
	parse := func(v string, fallback int) int {
		if v == "" {
			return fallback
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return -1
		}
		return n
	}
	times, count, sides := parse(m[1], 1), parse(m[2], 1), parse(m[3], 100)
	if times < 1 || times > 10 || count < 1 || count > 100 || times*count > 100 || sides < 2 || sides > 1000 {
		return "", bad
	}
	mod := 0
	for _, part := range modifierPattern.FindAllString(m[4], -1) {
		n, err := strconv.Atoi(part)
		if err != nil || n > 1000000 || n < -1000000 {
			return "", bad
		}
		mod += n
	}
	lines := []string{}
	for i := 0; i < times; i++ {
		sum := mod
		values := []string{}
		for j := 0; j < count; j++ {
			v := 1 + rand.IntN(sides)
			sum += v
			values = append(values, strconv.Itoa(v))
		}
		lines = append(lines, fmt.Sprintf("[%s] 修正%+d = %d", strings.Join(values, ","), mod, sum))
	}
	return strings.Join(lines, "\n"), nil
}
