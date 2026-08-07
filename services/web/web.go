package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/go-shiori/go-readability"
	"golang.org/x/net/html"

	"gobot/services/ai"
)

// Config web 能力配置
type Config struct {
	Proxy   string // HTTP 代理(可选)
	Timeout int    // 请求超时秒
}

type Service struct {
	cfg    Config
	http   *http.Client
}

func New(cfg Config) *Service {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30
	}
	tr := &http.Transport{}
	if cfg.Proxy != "" {
		if pu, err := url.Parse(cfg.Proxy); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	return &Service{cfg: cfg, http: &http.Client{Timeout: time.Duration(cfg.Timeout) * time.Second, Transport: tr}}
}

// Search 用 DuckDuckGo 搜索, 返回结果列表
func (s *Service) Search(ctx context.Context, query string, limit int) (string, error) {
	if limit <= 0 {
		limit = 8
	}
	u := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	resp, err := s.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("搜索失败: HTTP %d", resp.StatusCode)
	}

	results := parseDDGResults(string(body), limit)
	if len(results) == 0 {
		return "未找到相关结果", nil
	}
	var b strings.Builder
	for i, r := range results {
		b.WriteString(fmt.Sprintf("%d. %s\n   %s\n   %s\n\n", i+1, r.Title, r.URL, r.Snippet))
	}
	return strings.TrimSpace(b.String()), nil
}

type ddgResult struct {
	Title, URL, Snippet string
}

// parseDDGResults 解析 DuckDuckGo HTML 搜索结果
func parseDDGResults(htmlStr string, limit int) []ddgResult {
	doc, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return nil
	}
	var results []ddgResult
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(results) >= limit {
			return
		}
		if n.Type == html.ElementNode && n.Data == "a" && hasClass(n, "result__a") {
			title := getText(n)
			href := getAttr(n, "href")
			// 解析 DDG 重定向链接
			if strings.Contains(href, "uddg=") {
				if u, err := url.Parse(href); err == nil {
					href = u.Query().Get("uddg")
				}
			}
			r := ddgResult{Title: title, URL: href}
			// 找相邻的 snippet
			parent := n.Parent
			if parent != nil {
				var snippetNode *html.Node
				var findSnippet func(*html.Node)
				findSnippet = func(sn *html.Node) {
					if snippetNode != nil {
						return
					}
					for c := sn.FirstChild; c != nil; c = c.NextSibling {
						if c.Type == html.ElementNode && c.Data == "a" && hasClass(c, "result__snippet") {
							snippetNode = c
							return
						}
						findSnippet(c)
					}
				}
				findSnippet(parent)
				if snippetNode != nil {
					r.Snippet = getText(snippetNode)
				}
			}
			results = append(results, r)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return results
}

// FetchPage 抓取网页并提取正文
func (s *Service) FetchPage(ctx context.Context, pageURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", pageURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	resp, err := s.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	article, err := readability.FromReader(resp.Body, mustParseURL(pageURL))
	if err != nil {
		// 正文提取失败, 回退到原始文本
		body, _ := io.ReadAll(resp.Body)
		return stripTags(string(body)), nil
	}
	text := strings.TrimSpace(article.TextContent)
	if len([]rune(text)) > 5000 {
		text = string([]rune(text)[:5000]) + "\n...(内容过长已截断)"
	}
	if article.Title != "" {
		text = "标题: " + article.Title + "\n" + text
	}
	return text, nil
}

// NewSearchTool web_search AI 工具
func (s *Service) NewSearchTool() ai.Tool {
	t := ai.NewTool("web_search", "搜索互联网获取最新信息。当用户询问时事、新闻、最新数据或需要查资料时使用。返回相关网页标题、链接和摘要。",
		map[string]*ai.ToolParam{
			"query": {Type: "string", Description: "搜索关键词"},
			"limit": {Type: "integer", Description: "返回结果数量(可选, 默认8)"},
		},
		[]string{"query"},
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			q, _ := args["query"].(string)
			if q == "" {
				return "错误: query 不能为空", nil
			}
			limit := 8
			if v, ok := args["limit"].(float64); ok && v > 0 {
				limit = int(v)
			}
			return s.Search(ctx, q, limit)
		})
	t.Risk = ai.RiskLow
	return t
}

// NewFetchTool fetch_url AI 工具
func (s *Service) NewFetchTool() ai.Tool {
	t := ai.NewTool("fetch_url", "抓取指定网页的正文内容。当用户需要了解某个网页的具体内容时使用。",
		map[string]*ai.ToolParam{
			"url": {Type: "string", Description: "网页 URL"},
		},
		[]string{"url"},
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			u, _ := args["url"].(string)
			if u == "" {
				return "错误: url 不能为空", nil
			}
			return s.FetchPage(ctx, u)
		})
	t.Risk = ai.RiskLow
	return t
}

// NewBrowserTool 浏览器自动化工具(Playwright)
func (s *Service) NewBrowserTool(scriptPath string) ai.Tool {
	t := ai.NewTool("browser", "用无头浏览器打开网页。适用于需要 JS 渲染的页面、动态内容、截图等 fetch_url 无法处理的场景。支持 open(提取文本)/screenshot(截图)/exec(执行JS)。",
		map[string]*ai.ToolParam{
			"action": {Type: "string", Description: "操作: open=提取文本 / screenshot=截图 / exec=执行JS", Enum: []string{"open", "screenshot", "exec"}},
			"url":    {Type: "string", Description: "网页 URL"},
			"extra":  {Type: "string", Description: "screenshot时: 输出文件路径; exec时: 要执行的JS"},
		},
		[]string{"action", "url"},
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			action, _ := args["action"].(string)
			urlStr, _ := args["url"].(string)
			extra, _ := args["extra"].(string)
			if action == "" || urlStr == "" {
				return "错误: action 和 url 必填", nil
			}
			cmdArgs := []string{scriptPath, action, urlStr}
			if extra != "" {
				cmdArgs = append(cmdArgs, extra)
			}
			out, err := runScript(ctx, cmdArgs)
			if err != nil {
				return "浏览器执行失败: " + err.Error() + "\n" + out, nil
			}
			return out, nil
		})
	t.Risk = ai.RiskMedium
	return t
}

// ---- HTML 辅助 ----

// runScript 执行 Python 脚本并捕获输出
func runScript(ctx context.Context, args []string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "python3", args...)
	out, err := cmd.CombinedOutput()
	if cctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("浏览器执行超时")
	}
	return strings.TrimSpace(string(out)), err
}

func getText(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
	}
	return strings.TrimSpace(b.String())
}

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	for _, a := range n.Attr {
		if a.Key == "class" {
			for _, c := range strings.Fields(a.Val) {
				if c == class {
					return true
				}
			}
		}
	}
	return false
}

var tagRegex = regexp.MustCompile(`<[^>]+>`)

func mustParseURL(s string) *url.URL {
	u, _ := url.Parse(s)
	return u
}

func stripTags(s string) string {
	s = tagRegex.ReplaceAllString(s, " ")
	// 压缩空白
	re := regexp.MustCompile(`\s+`)
	s = re.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
