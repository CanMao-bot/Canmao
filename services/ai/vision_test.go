package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gobot/core"
)

type visionRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
}

type visionQuoteSender struct {
	*securitySender
	quoted *core.MessageInfo
}

func (s *visionQuoteSender) GetMsg(int64) (*core.MessageInfo, error) { return s.quoted, nil }

func visionImageServer(t *testing.T) *httptest.Server {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Some QQ image hosts return application/octet-stream for valid pixels.
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(buf.Bytes())
	}))
	t.Cleanup(server.Close)
	return server
}

func TestNativeImageTurnsIncludePixelsThroughToolFollowup(t *testing.T) {
	for _, scene := range []string{"direct", "quoted", "combined", "image_only"} {
		t.Run(scene, func(t *testing.T) {
			s := securityFixture(t)
			// A legacy caption model must not intercept native image requests.
			s.cfg.Memory.VisionModel = "unavailable-caption-model"
			requests := make(chan visionRequest, 4)
			installTestModel(t, s, func(w http.ResponseWriter, r *http.Request) {
				var req visionRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				requests <- req
				if req.Messages[len(req.Messages)-1].Role == "tool" {
					completeText(w, "已看清图片")
					return
				}
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"vision-tool","type":"function","function":{"name":"vision_probe","arguments":"{}"}}]}}]}`))
			})
			tool := NewTool("vision_probe", "test", nil, nil, func(context.Context, map[string]interface{}) (string, error) { return "ok", nil })
			tool.Risk = RiskLow
			s.AddTool(tool)
			imageServer := visionImageServer(t)
			seg := core.Segment{Type: "image", Data: map[string]interface{}{"url": imageServer.URL}}
			ev := securityEvent(1, 0, "看图")
			wantImages := 1
			if scene != "quoted" {
				ev.Message = append(ev.Message, seg)
			}
			if scene == "quoted" || scene == "combined" {
				s.bot.SetSender(&visionQuoteSender{securitySender: s.bot.Sender.(*securitySender), quoted: &core.MessageInfo{MessageID: 88, UserID: 3, Message: []core.Segment{seg}}})
				ev.Message = append(ev.Message, core.Segment{Type: "reply", Data: map[string]interface{}{"id": "88"}})
				if scene == "combined" {
					wantImages = 2
				}
			}
			if scene == "image_only" {
				ev.RawMessage, ev.Message = "", []core.Segment{seg}
			}
			if !s.Handle(context.Background(), s.bot, ev) {
				t.Fatal("image turn not handled")
			}
			if len(requests) != 2 {
				t.Fatalf("expected one native Agent turn and a tool followup, requests=%d", len(requests))
			}
			for len(requests) > 0 {
				req := <-requests
				if req.Model != "test-model" {
					t.Fatal("image routed to another model", req.Model)
				}
				images := 0
				for _, msg := range req.Messages {
					if msg.Role != "user" {
						continue
					}
					var parts []ContentPart
					if err := json.Unmarshal(msg.Content, &parts); err != nil {
						t.Fatal("user input was reduced to text", err)
					}
					for _, part := range parts {
						if part.Type != "image_url" || part.ImageURL == nil {
							continue
						}
						encoded, ok := strings.CutPrefix(part.ImageURL.URL, "data:image/png;base64,")
						if !ok {
							t.Fatal("image data missing or MIME type incorrect")
						}
						pixels, err := base64.StdEncoding.DecodeString(encoded)
						if err != nil {
							t.Fatal(err)
						}
						if _, err := png.Decode(bytes.NewReader(pixels)); err != nil {
							t.Fatal("request does not contain valid pixels", err)
						}
						images++
					}
				}
				if images != wantImages {
					t.Fatalf("received %d images, want %d", images, wantImages)
				}
			}
			ses, err := s.session.GetCurrent("private", 0, 1, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(ses.Messages) < 2 || !strings.Contains(ses.Messages[0].Content, "附带") || strings.Contains(ses.Messages[0].Content, "base64,") || ses.Messages[len(ses.Messages)-1].Content != "已看清图片" {
				t.Fatal("persistent history must record image presence without inline pixels")
			}
		})
	}
}

func TestCaptionModeIsExplicitAndDoesNotSilentlyDropImages(t *testing.T) {
	s := securityFixture(t)
	s.cfg.AI.ImageMode = "caption"
	ctx := context.Background()
	if _, _, err := s.prepareUserMessage(ctx, "看图", []string{"data:image/png;base64,AA=="}); err == nil {
		t.Fatal("caption mode silently discarded image without a caption model")
	}
	s.cfg.Memory.VisionModel = "caption-model"
	requests := make(chan visionRequest, 2)
	installTestModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		var req visionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		requests <- req
		completeText(w, "红色圆形")
	})
	ctx = s.client.pin(WithModelOverride(ctx, "main-model"), 64000)
	message, history, err := s.prepareUserMessage(ctx, "看图", []string{"data:image/png;base64,AA=="})
	if err != nil || !strings.Contains(message.TextContent(), "红色圆形") || history != message.TextContent() {
		t.Fatal("explicit caption compatibility failed", err)
	}
	if len(requests) != 1 || (<-requests).Model != "caption-model" || modelFromCtx(ctx) != "main-model" {
		t.Fatal("caption request did not preserve model routing")
	}
	installTestModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "caption unavailable", http.StatusNotFound)
	})
	if _, _, err := s.prepareUserMessage(context.Background(), "看图", []string{"data:image/png;base64,AA=="}); err == nil {
		t.Fatal("failed caption silently became an image-free model request")
	}
}

func TestImageTokenBudgetCountsImagesWithoutTokenizingBase64(t *testing.T) {
	text := estimateMessageListTokens([]Message{NewTextMessage("user", "看图")})
	one := estimateMessageListTokens([]Message{NewMultimodalMessage("user", "看图", []string{"data:image/png;base64,AA=="})})
	two := estimateMessageListTokens([]Message{NewMultimodalMessage("user", "看图", []string{"data:image/png;base64,AA==", "data:image/png;base64,AA=="})})
	large := estimateMessageListTokens([]Message{NewMultimodalMessage("user", "看图", []string{"data:image/png;base64," + strings.Repeat("A", 100000)})})
	if one <= text || two <= one || large != one {
		t.Fatal("visual budget must count images independently of base64 text length", text, one, two, large)
	}
}
