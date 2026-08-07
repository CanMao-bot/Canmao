package main

import (
	"context"
	"fmt"
	"strings"

	"gobot/pluginapi"
)

type echoPlugin struct {
	pluginapi.Base
	sender pluginapi.Sender
}

var Plugin = func() pluginapi.Plugin {
	return &echoPlugin{}
}

func (e *echoPlugin) Name() string { return "echo" }

func (e *echoPlugin) Init(s pluginapi.Sender) error {
	e.sender = s
	return nil
}

func (e *echoPlugin) OnEvent(ctx context.Context, ev *pluginapi.Event) (string, bool) {
	t := strings.TrimSpace(ev.Message)
	if strings.HasPrefix(t, "/echo ") {
		return strings.TrimSpace(strings.TrimPrefix(t, "/echo ")), true
	}
	return "", false
}

func (e *echoPlugin) Tools() []pluginapi.Tool {
	return []pluginapi.Tool{
		{
			Name:        "calc_add",
			Description: "计算两个数字的和",
			Risk:        1, // 低风险
			Parameters: map[string]*pluginapi.Param{
				"a": {Type: "number", Description: "第一个数字", Required: true},
				"b": {Type: "number", Description: "第二个数字", Required: true},
			},
			Call: func(ctx context.Context, args map[string]interface{}) (string, error) {
				a := args["a"].(float64)
				b := args["b"].(float64)
				return fmt.Sprintf("%.0f + %.0f = %.0f", a, b, a+b), nil
			},
		},
	}
}
