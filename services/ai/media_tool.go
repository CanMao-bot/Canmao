package ai

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"gobot/core"
	"gobot/services/dashscope"
)

// mediaClient 图像生成/TTS 客户端(由 main 注入)
var mediaClient *dashscope.Client

// SetMediaClient 注入多模态客户端并注册工具
func (s *Service) SetMediaClient(c *dashscope.Client, voice string) {
	mediaClient = c
	if c == nil {
		return
	}
	s.registerMediaTools(voice)
}

// generateImageTool AI 图像生成工具
func (s *Service) generateImageTool() Tool {
	t := NewTool("generate_image", "根据文字描述生成一张图片。用户想要图片、插画、海报等时使用。会生成图片并返回保存的信息。",
		map[string]*ToolParam{
			"prompt": {Type: "string", Description: "图片内容描述(详细的中文描述, 包含主体、风格、场景等)"},
			"n":      {Type: "integer", Description: "生成数量(1-4, 可选, 默认1)"},
		},
		[]string{"prompt"},
		s.generateImageCallback)
	t.Risk = RiskMedium
	return t
}

func (s *Service) generateImageCallback(ctx context.Context, args map[string]interface{}) (string, error) {
	if mediaClient == nil {
		return "图像生成功能不可用", nil
	}
	prompt, _ := args["prompt"].(string)
	if prompt == "" {
		return "错误: prompt 不能为空", nil
	}
	n := 1
	if v, ok := args["n"].(float64); ok && v > 0 {
		n = int(v)
	}
	urls, err := mediaClient.GenerateImage(ctx, prompt, n)
	if err != nil {
		return "图像生成失败: " + err.Error(), nil
	}
	// 下载图片保存到文件目录
	var saved []string
	var paths []string
	for _, u := range urls {
		if p, name, err := s.saveMediaImage(ctx, u); err == nil {
			saved = append(saved, name)
			paths = append(paths, p)
		}
	}
	if len(saved) == 0 {
		return "图片已生成但保存失败, URL: " + strings.Join(urls, "\n"), nil
	}
	// 发送图片到目标会话
	if ev, ok := ctx.Value("event").(*core.Event); ok && ev != nil {
		var segs []core.Segment
		for _, p := range paths {
			segs = append(segs, core.ImageSegment(p))
		}
		var sendErr error
		if ev.IsGroup() {
			sendErr = s.bot.Sender.SendGroupMsg(ev.GroupID, ev.UserID, segs)
		} else {
			sendErr = s.bot.Sender.SendPrivateMsg(ev.UserID, segs)
		}
		if sendErr != nil {
			return "✅ 图片已生成并保存: " + strings.Join(saved, ", ") + "\n(发送失败: " + sendErr.Error() + ")", nil
		}
		return "✅ 已生成图片并发送: " + strings.Join(saved, ", "), nil
	}
	return "✅ 已生成图片并保存: " + strings.Join(saved, ", "), nil
}

// saveMediaImage 下载图片保存, 返回(文件路径, 文件名)
func (s *Service) saveMediaImage(ctx context.Context, url string) (string, string, error) {
	if s.fileDir == "" {
		return "", "", fmt.Errorf("文件目录未配置")
	}
	// 用通用 http 客户端下载
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	// 生成文件名
	name := fmt.Sprintf("img_%d.png", time.Now().Unix())
	path := s.fileDir + "/" + name
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", "", err
	}
	return path, name, nil
}

// ttsTool TTS 语音合成工具
func (s *Service) ttsTool(voice string) Tool {
	t := NewTool("tts_speech", "将文字合成为语音(音频)。用户想要语音播报、朗读内容时使用。",
		map[string]*ToolParam{
			"text":  {Type: "string", Description: "要合成的文字内容"},
			"voice": {Type: "string", Description: "音色(可选)"},
		},
		[]string{"text"},
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			if mediaClient == nil {
				return "TTS 功能不可用", nil
			}
			text, _ := args["text"].(string)
			if text == "" {
				return "错误: text 不能为空", nil
			}
			v, _ := args["voice"].(string)
			if v == "" {
				v = voice
			}
			audioURL, err := mediaClient.SynthesizeSpeech(ctx, text, v)
			if err != nil {
				return "语音合成失败: " + err.Error(), nil
			}
			// 下载音频保存到本地
			audioFile, err := s.saveMediaAudio(ctx, audioURL)
			if err != nil {
				return "✅ 语音已生成但保存失败: " + audioURL + "\n" + err.Error(), nil
			}
			// 发送到目标会话
			if ev, ok := ctx.Value("event").(*core.Event); ok && ev != nil {
				msg := []core.Segment{core.RecordSegment(audioFile)}
				var sendErr error
				if ev.IsGroup() {
					sendErr = s.bot.Sender.SendGroupMsg(ev.GroupID, ev.UserID, msg)
				} else {
					sendErr = s.bot.Sender.SendPrivateMsg(ev.UserID, msg)
				}
				if sendErr != nil {
					return "语音已生成但发送失败: " + sendErr.Error() + "\n文件: " + audioFile, nil
				}
				return "✅ 语音已发送, 保存为: " + audioFile, nil
			}
			return "✅ 语音已生成, 文件: " + audioFile, nil
		})
	t.Risk = RiskLow
	return t
}

// saveMediaAudio 下载音频保存到文件目录, 返回文件路径
func (s *Service) saveMediaAudio(ctx context.Context, url string) (string, error) {
	if s.fileDir == "" {
		return "", fmt.Errorf("文件目录未配置")
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	// 推断格式: URL 通常带 .mp3/.wav 后缀, 否则用 mp3
	ext := "mp3"
	u := resp.Request.URL.String()
	if strings.Contains(u, ".wav") {
		ext = "wav"
	} else if strings.Contains(u, ".silk") {
		ext = "silk"
	} else if strings.Contains(u, ".amr") {
		ext = "amr"
	}
	name := fmt.Sprintf("tts_%d.%s", time.Now().Unix(), ext)
	path := s.fileDir + "/" + name
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Service) registerMediaTools(voice string) {
	s.tools = append(s.tools, s.generateImageTool())
	s.tools = append(s.tools, s.ttsTool(voice))
	log.Printf("[media] 图像生成 + TTS 工具已注册")
}
