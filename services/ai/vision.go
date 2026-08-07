package ai

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"gobot/core"
)

// describeImages 用识图模型转述图片内容为文字
func (s *Service) describeImages(ctx context.Context, imageDataURLs []string) string {
	if len(imageDataURLs) == 0 || s.cfg.Memory.VisionModel == "" {
		return ""
	}
	// 获取当前 provider 的 base_url/api_key
	baseURL, apiKey, _, err := s.models.Endpoint()
	if err != nil || baseURL == "" {
		log.Printf("[ai] 识图失败: 无 provider 配置")
		return ""
	}
	// 使用识图模型转述
	visionCfg := s.cfg.AI
	visionCfg.Model = s.cfg.Memory.VisionModel
	visionCfg.BaseURL = baseURL
	visionCfg.APIKey = apiKey

	vc := NewClient(&visionCfg)
	parts := []ContentPart{{Type: "text", Text: "请用简洁中文描述这张图片的主要内容。"}}
	for _, u := range imageDataURLs {
		parts = append(parts, ContentPart{Type: "image_url", ImageURL: &ImageURLPart{URL: u}})
	}
	msg := Message{Role: "user", Content: parts}

	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resp, err := vc.Complete(cctx, []Message{msg}, nil)
	if err != nil {
		log.Printf("[ai] 识图转述失败: %v", err)
		return ""
	}
	return strings.TrimSpace(resp.TextContent())
}

// saveMemories 从对话中提取记忆保存
func (s *Service) saveMemories(ctx context.Context, ev *core.Event, userMsg, reply string) {
	scope, gid, uid := s.sessionScope(ev)

	// 用户消息(含图片描述)值得记忆: 仅保存有意义的信息
	toSave := []string{}
	if m := extractMemorable(userMsg); m != "" {
		toSave = append(toSave, m)
	}

	// 从用户消息中提取"我XXX"类个人偏好/事实
	for _, seg := range ev.Message {
		if seg.Type != "text" {
			continue
		}
		text, _ := seg.Data["text"].(string)
		for _, pref := range extractPreferences(text) {
			toSave = append(toSave, pref)
		}
	}

	for _, m := range toSave {
		if err := s.memory.Save(ctx, scope, gid, uid, m); err != nil {
			log.Printf("[ai] 记忆保存失败: %v", err)
		}
	}
}

// extractMemorable 提取值得记住的简短信息(不超过60字)
func extractMemorable(msg string) string {
	t := strings.TrimSpace(msg)
	if t == "" || len([]rune(t)) > 200 {
		return ""
	}
	// 去掉 CQ 码
	t = stripCQ(t)
	t = strings.TrimSpace(t)
	if t == "" {
		return ""
	}
	// 记忆存储格式带来源上下文
	return "[对话] " + t
}

// extractPreferences 提取用户个人偏好/事实: "我叫/我是/我喜欢/我不喜欢/我住在/我今年..."
func extractPreferences(text string) []string {
	var out []string
	patterns := []string{"我叫", "我是", "我喜欢", "我爱", "我不喜欢", "我讨厌", "我住在", "我今年", "我的名字", "我目前在", "我想学"}
	for _, p := range patterns {
		if idx := strings.Index(text, p); idx >= 0 {
			seg := strings.TrimSpace(text[idx:])
			if len([]rune(seg)) > 1 && len([]rune(seg)) <= 80 {
				out = append(out, seg)
			}
		}
	}
	return out
}

func stripCQ(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '[' && i+2 < len(runes) && runes[i+1] == 'C' && runes[i+2] == 'Q' {
			// 跳到 ] 之后
			for i < len(runes) && runes[i] != ']' {
				i++
			}
			continue
		}
		b.WriteRune(runes[i])
	}
	return b.String()
}

func countLines(s string) int {
	return strings.Count(s, "\n") + 1
}

var _ = fmt.Sprintf
