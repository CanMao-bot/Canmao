package dashscope

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Config DashScope 多模态(图像生成/TTS)配置
type Config struct {
	BaseURL string // 默认 https://dashscope.aliyuncs.com
	APIKey  string
	// 图像生成
	ImageModel string // 如 wanx-v1
	// TTS
	TTSEndpoint string // 如 qwen3-tts-flash
	Timeout     int
}

// Client DashScope 多模态客户端
type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://dashscope.aliyuncs.com"
	}
	if cfg.ImageModel == "" {
		cfg.ImageModel = "wanx-v1"
	}
	if cfg.TTSEndpoint == "" {
		cfg.TTSEndpoint = "qwen3-tts-flash"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 180
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: time.Duration(cfg.Timeout) * time.Second}}
}

func (c *Client) auth() string { return "Bearer " + c.cfg.APIKey }

// GenerateImage 生成图片, 返回图片 URL 列表
// 自动适配: qwen-image-2.0 系列走同步多模态接口, 其他(wanx等)走异步 text2image
func (c *Client) GenerateImage(ctx context.Context, prompt string, n int) ([]string, error) {
	if strings.Contains(c.cfg.ImageModel, "qwen-image-2.0") {
		return c.generateImageSync(ctx, prompt, n)
	}
	return c.generateImageAsync(ctx, prompt, n)
}

// generateImageSync qwen-image-2.0 系列同步接口
func (c *Client) generateImageSync(ctx context.Context, prompt string, n int) ([]string, error) {
	if n <= 0 {
		n = 1
	}
	payload := map[string]interface{}{
		"model": c.cfg.ImageModel,
		"input": map[string]interface{}{
			"messages": []map[string]interface{}{
				{
					"role":    "user",
					"content": []map[string]interface{}{{"text": prompt}},
				},
			},
		},
		"parameters": map[string]interface{}{
			"size": "1024*1024", "n": n,
			"prompt_extend": true, "watermark": false,
		},
	}
	resp, err := c.postJSON(ctx, c.cfg.BaseURL+"/api/v1/services/aigc/multimodal-generation/generation", payload, false)
	if err != nil {
		return nil, err
	}
	out, _ := resp["output"].(map[string]interface{})
	choices, _ := out["choices"].([]interface{})
	var urls []string
	for _, ch := range choices {
		cm, _ := ch.(map[string]interface{})
		msg, _ := cm["message"].(map[string]interface{})
		content, _ := msg["content"].([]interface{})
		for _, cpart := range content {
			pm, _ := cpart.(map[string]interface{})
			if img, ok := pm["image"].(string); ok && img != "" {
				urls = append(urls, img)
			}
		}
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("图像生成无结果: %v", resp)
	}
	return urls, nil
}

// generateImageAsync wanx/qwen-image-plus 等异步 text2image 接口
func (c *Client) generateImageAsync(ctx context.Context, prompt string, n int) ([]string, error) {
	if n <= 0 {
		n = 1
	}
	if n > 4 {
		n = 4
	}
	payload := map[string]interface{}{
		"model": c.cfg.ImageModel,
		"input": map[string]interface{}{"prompt": prompt},
		"parameters": map[string]interface{}{
			"size": "1024*1024",
			"n":    n,
		},
	}
	// 创建任务(必须带 X-DashScope-Async: enable)
	taskResp, err := c.postJSON(ctx, c.cfg.BaseURL+"/api/v1/services/aigc/text2image/image-synthesis", payload, true)
	if err != nil {
		return nil, err
	}
	taskID, _ := taskResp["output"].(map[string]interface{})["task_id"].(string)
	if taskID == "" {
		return nil, fmt.Errorf("未获取到任务ID: %v", taskResp)
	}

	// 轮询任务结果(最多3分钟)
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
		st, err := c.getJSON(ctx, c.cfg.BaseURL+"/api/v1/tasks/"+taskID)
		if err != nil {
			return nil, err
		}
		out, _ := st["output"].(map[string]interface{})
		status, _ := out["task_status"].(string)
		switch status {
		case "SUCCEEDED":
			var urls []string
			if results, ok := out["results"].([]interface{}); ok {
				for _, r := range results {
					if m, ok := r.(map[string]interface{}); ok {
						if u, ok := m["url"].(string); ok && u != "" {
							urls = append(urls, u)
						}
					}
				}
			}
			if len(urls) == 0 {
				return nil, fmt.Errorf("任务成功但无图片URL")
			}
			return urls, nil
		case "FAILED":
			return nil, fmt.Errorf("图像生成失败: %v", out)
		}
	}
	return nil, fmt.Errorf("图像生成超时")
}

// SynthesizeSpeech TTS 语音合成, 返回音频 URL
func (c *Client) SynthesizeSpeech(ctx context.Context, text, voice string) (string, error) {
	if voice == "" {
		voice = "Cherry"
	}
	payload := map[string]interface{}{
		"model": c.cfg.TTSEndpoint,
		"input": map[string]interface{}{"text": text, "voice": voice},
	}
	resp, err := c.postJSON(ctx, c.cfg.BaseURL+"/api/v1/services/aigc/multimodal-generation/generation", payload, false)
	if err != nil {
		return "", err
	}
	out, _ := resp["output"].(map[string]interface{})
	audio, _ := out["audio"].(map[string]interface{})
	url, _ := audio["url"].(string)
	if url == "" {
		return "", fmt.Errorf("TTS 未返回音频URL: %v", resp)
	}
	return url, nil
}

func (c *Client) postJSON(ctx context.Context, url string, payload interface{}, async bool) (map[string]interface{}, error) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.auth())
	if async {
		req.Header.Set("X-DashScope-Async", "enable")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DashScope API %d: %s", resp.StatusCode, string(data))
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *Client) getJSON(ctx context.Context, url string) (map[string]interface{}, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", c.auth())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DashScope API %d: %s", resp.StatusCode, string(data))
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}
