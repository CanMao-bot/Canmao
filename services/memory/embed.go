package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// EmbeddingConfig embedding API 配置
type EmbeddingConfig struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout int
}

// Embedder 向量化客户端(OpenAI 兼容 embeddings 接口)
type Embedder struct {
	cfg  EmbeddingConfig
	http *http.Client
}

func NewEmbedder(cfg EmbeddingConfig) *Embedder {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30
	}
	return &Embedder{cfg: cfg, http: &http.Client{Timeout: time.Duration(timeout) * time.Second}}
}

func (e *Embedder) Configured() bool {
	return strings.HasPrefix(e.cfg.BaseURL, "http") && e.cfg.Model != "" && e.cfg.APIKey != "" && !strings.HasPrefix(e.cfg.APIKey, "YOUR_")
}

// Embed 将文本向量化
func (e *Embedder) Embed(ctx context.Context, text string) ([]float64, error) {
	req := struct {
		Model string `json:"model"`
		Input string `json:"input"`
	}{Model: e.cfg.Model, Input: text}
	body, _ := json.Marshal(req)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", e.cfg.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+e.cfg.APIKey)

	resp, err := e.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("embedding 请求: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding API 返回 %d", resp.StatusCode)
	}
	var result struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("embedding 无结果")
	}
	vec := result.Data[0].Embedding
	if len(vec) == 0 {
		return nil, fmt.Errorf("embedding 返回空向量")
	}
	var norm float64
	for _, v := range vec {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("embedding 返回非有限数")
		}
		norm += v * v
	}
	if norm == 0 || math.IsInf(norm, 0) {
		return nil, fmt.Errorf("embedding 返回无效向量")
	}
	return vec, nil
}

// CosineSimilarity 余弦相似度
func CosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	score := dot / (math.Sqrt(na) * math.Sqrt(nb))
	if math.IsNaN(score) || math.IsInf(score, 0) {
		return 0
	}
	return math.Max(-1, math.Min(1, score))
}
