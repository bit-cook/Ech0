// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	model "github.com/lin-snow/ech0/internal/model/setting"
	openai "github.com/sashabaranov/go-openai"
)

// openaiProvider 适配 OpenAI 兼容协议（OpenAI / DeepSeek / Qwen / Moonshot / Ollama 等）。
//
// Prompt cache：OpenAI 兼容端是服务端自动缓存（前缀 >1024 token 自动命中），无客户端字段可设，
// 故无需像 Anthropic 那样显式打 cache_control 断点——工具循环里重复的 system+工具定义前缀会被
// 服务端自动复用。
type openaiProvider struct {
	setting model.AgentSetting
}

func (p *openaiProvider) client() *openai.Client {
	cfg := openai.DefaultConfig(p.setting.ApiKey)
	if p.setting.BaseURL != "" {
		cfg.BaseURL = p.setting.BaseURL
	}
	return openai.NewClientWithConfig(cfg)
}

// buildMessages 把内部 tool-aware Message 映射为 OpenAI ChatCompletionMessage。
func (p *openaiProvider) buildMessages(in []Message) []openai.ChatCompletionMessage {
	msgs := make([]openai.ChatCompletionMessage, 0, len(in))
	for _, m := range in {
		msg := openai.ChatCompletionMessage{
			Role:       toOpenAIRole(m.Role),
			ToolCallID: m.ToolCallID,
		}
		// 带图消息走 multi-part（Content 与 MultiContent 互斥）；否则用纯文本 Content。
		if len(m.Images) > 0 {
			msg.MultiContent = openAIImageParts(m.Content, m.Images)
		} else {
			msg.Content = m.Content
		}
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, openai.ToolCall{
				ID:   tc.ID,
				Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{
					Name:      tc.Name,
					Arguments: string(tc.Args),
				},
			})
		}
		msgs = append(msgs, msg)
	}
	return msgs
}

// openAIImageParts 把文本 + 图片拼成 OpenAI 的多模态 content parts：首块文本（可空则略过），
// 随后每张图一块 image_url（Base64 转 data URL，否则用直链）。
func openAIImageParts(text string, images []ImagePart) []openai.ChatMessagePart {
	parts := make([]openai.ChatMessagePart, 0, len(images)+1)
	if text != "" {
		parts = append(parts, openai.ChatMessagePart{Type: openai.ChatMessagePartTypeText, Text: text})
	}
	for _, img := range images {
		url := img.URL
		if img.Base64 != "" {
			url = "data:" + img.MediaType + ";base64," + img.Base64
		}
		if url == "" {
			continue
		}
		parts = append(parts, openai.ChatMessagePart{
			Type:     openai.ChatMessagePartTypeImageURL,
			ImageURL: &openai.ChatMessageImageURL{URL: url},
		})
	}
	return parts
}

// buildTools 把内部 ToolDef 映射为 OpenAI Tool（function calling）。
func (p *openaiProvider) buildTools(defs []ToolDef) []openai.Tool {
	if len(defs) == 0 {
		return nil
	}
	tools := make([]openai.Tool, 0, len(defs))
	for _, d := range defs {
		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        d.Name,
				Description: d.Description,
				Parameters:  json.RawMessage(d.Parameters),
			},
		})
	}
	return tools
}

func (p *openaiProvider) Complete(ctx context.Context, req Request) (Response, error) {
	chatReq := openai.ChatCompletionRequest{
		Model:    p.setting.Model,
		Messages: p.buildMessages(req.Messages),
		Tools:    p.buildTools(req.Tools),
	}
	if req.Temperature != nil {
		chatReq.Temperature = *req.Temperature
	}
	// 刻意用已被 go-openai 标弃用的 MaxTokens（即 max_tokens）而非 MaxCompletionTokens：
	// max_completion_tokens 是 OpenAI 自家的新字段，DeepSeek / Qwen / Moonshot / Ollama 等
	// OpenAI 兼容端多数只认 max_tokens。对应的 SA1019 在 .golangci.yaml 里按文件+规则精确屏蔽。
	if req.MaxTokens > 0 {
		chatReq.MaxTokens = req.MaxTokens
	}

	resp, err := p.client().CreateChatCompletion(ctx, chatReq)
	if err != nil {
		return Response{}, err
	}
	if len(resp.Choices) == 0 {
		return Response{}, errors.New("openai: empty response")
	}
	return Response{Text: resp.Choices[0].Message.Content}, nil
}

func (p *openaiProvider) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	ch := make(chan Event)
	go p.stream(ctx, req, ch)
	return ch, nil
}

// stream 真流式消费 OpenAI SSE：文本增量实时 emit，工具调用按 index 跨 chunk 累积
// arguments 分片，拼装完整后 emit EventToolCall。
func (p *openaiProvider) stream(ctx context.Context, req Request, ch chan<- Event) {
	defer close(ch)

	chatReq := openai.ChatCompletionRequest{
		Model:    p.setting.Model,
		Messages: p.buildMessages(req.Messages),
		Tools:    p.buildTools(req.Tools),
		Stream:   true,
	}
	if req.Temperature != nil {
		chatReq.Temperature = *req.Temperature
	}
	// 同 Complete：兼容性优先，继续发 max_tokens（理由见 Complete 内注释）。
	if req.MaxTokens > 0 {
		chatReq.MaxTokens = req.MaxTokens
	}

	stream, err := p.client().CreateChatCompletionStream(ctx, chatReq)
	if err != nil {
		send(ctx, ch, Event{Kind: EventError, Err: err})
		return
	}
	defer func() { _ = stream.Close() }()

	// acc 按 tool_call 的 index 累积分片：id/name 首帧给出，arguments 跨帧拼接。
	acc := newToolCallAccumulator()
	// guard 防「模型把工具调用当正文吐出来」泄漏给用户（详见 toolCallLeakGuard）。
	guard := &toolCallLeakGuard{}
	// splitter 把内联在正文里的 <think> 推理段从答案里拆出来（推理模型经 OpenAI 兼容端的怪癖）。
	splitter := &reasoningSplitter{}

	for {
		resp, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			send(ctx, ch, Event{Kind: EventError, Err: recvErr})
			return
		}
		if len(resp.Choices) == 0 {
			continue
		}
		delta := resp.Choices[0].Delta

		// 独立 reasoning_content 字段（DeepSeek reasoner 等）：直接当推理上浮。
		if delta.ReasoningContent != "" {
			if !send(ctx, ch, Event{Kind: EventReasoningDelta, Text: delta.ReasoningContent}) {
				return
			}
		}
		if delta.Content != "" {
			answer, reasoning := splitter.feed(delta.Content)
			if reasoning != "" && !send(ctx, ch, Event{Kind: EventReasoningDelta, Text: reasoning}) {
				return
			}
			// 只有答案段才过工具调用泄漏守卫（推理段里的 <tool_call> 字样不应误触发）。
			if answer != "" {
				safe, tripped := guard.feed(answer)
				if tripped {
					send(ctx, ch, Event{Kind: EventError, Err: errTextToolCallLeak})
					return
				}
				if safe != "" && !send(ctx, ch, Event{Kind: EventTextDelta, Text: safe}) {
					return
				}
			}
		}
		acc.add(delta.ToolCalls)
	}

	// 流结束：先放行拆分器暂留的尾巴（未闭合 <think> 归推理，否则归答案），答案段再过守卫；
	// 最后放行守卫暂留的尾巴（确认非工具调用语法），再把累积完整的工具调用依次上浮。
	ansRest, reaRest := splitter.flush()
	if reaRest != "" && !send(ctx, ch, Event{Kind: EventReasoningDelta, Text: reaRest}) {
		return
	}
	if ansRest != "" {
		safe, tripped := guard.feed(ansRest)
		if tripped {
			send(ctx, ch, Event{Kind: EventError, Err: errTextToolCallLeak})
			return
		}
		if safe != "" && !send(ctx, ch, Event{Kind: EventTextDelta, Text: safe}) {
			return
		}
	}
	if rest := guard.flush(); rest != "" {
		if !send(ctx, ch, Event{Kind: EventTextDelta, Text: rest}) {
			return
		}
	}
	for _, tc := range acc.finish() {
		if !send(ctx, ch, Event{Kind: EventToolCall, ToolCall: tc}) {
			return
		}
	}
	send(ctx, ch, Event{Kind: EventDone})
}

// toolCallAccumulator 累积流式 tool_call 分片：OpenAI 把同一个调用的 arguments
// 按 index 跨多个 chunk 切片下发，需按 index 拼回完整 JSON。
type toolCallAccumulator struct {
	order []int
	byIdx map[int]*ToolCall
	args  map[int][]byte
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{
		byIdx: make(map[int]*ToolCall),
		args:  make(map[int][]byte),
	}
}

func (a *toolCallAccumulator) add(deltas []openai.ToolCall) {
	for _, d := range deltas {
		idx := 0
		if d.Index != nil {
			idx = *d.Index
		}
		tc, ok := a.byIdx[idx]
		if !ok {
			tc = &ToolCall{}
			a.byIdx[idx] = tc
			a.order = append(a.order, idx)
		}
		if d.ID != "" {
			tc.ID = d.ID
		}
		if d.Function.Name != "" {
			tc.Name = d.Function.Name
		}
		if d.Function.Arguments != "" {
			a.args[idx] = append(a.args[idx], d.Function.Arguments...)
		}
	}
}

func (a *toolCallAccumulator) finish() []ToolCall {
	out := make([]ToolCall, 0, len(a.order))
	for _, idx := range a.order {
		tc := a.byIdx[idx]
		args := a.args[idx]
		if len(args) == 0 {
			args = []byte("{}")
		}
		out = append(out, ToolCall{ID: tc.ID, Name: tc.Name, Args: json.RawMessage(args)})
	}
	return out
}

func toOpenAIRole(r Role) string {
	switch r {
	case RoleSystem:
		return openai.ChatMessageRoleSystem
	case RoleAssistant:
		return openai.ChatMessageRoleAssistant
	case RoleTool:
		return openai.ChatMessageRoleTool
	default:
		return openai.ChatMessageRoleUser
	}
}

// send 向 ch 发送事件，ctx 取消时返回 false（Provider 据此停止）。
func send(ctx context.Context, ch chan<- Event, ev Event) bool {
	select {
	case ch <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// errTextToolCallLeak 在正文里检测到工具调用语法时返回：这是端点配置问题（未把模型自家
// 格式归一化成结构化 tool_calls），而非内容本身。响亮失败 + 可定位，胜过泄漏原始语法或静默降级。
var errTextToolCallLeak = errors.New(
	"检测到模型以文本形式返回工具调用：端点未归一化为结构化 tool_calls。请在推理服务启用对应的 " +
		"tool-call parser（如 vLLM 的 --enable-auto-tool-choice --tool-call-parser）后重试")

// textToolCallMarkers 是「模型把工具调用当正文吐出来」的特征串（Hermes/Qwen 等文本格式）。
// 正常的微博客回顾回答不会出现这些标记，误伤概率可忽略。
var textToolCallMarkers = []string{"<tool_call>", "<function="}

// toolCallLeakGuard 在流式正文里探测工具调用语法泄漏：安全文本照常放行；末尾「可能是半个标记」
// 的尾巴暂留到能判定为止（防标记跨 chunk 被拆开漏过）；一旦拼出完整标记即 tripped。
// 模型无关、不解析各家格式——职责仅是「别泄漏 + 让端点配置问题暴露出来」。
type toolCallLeakGuard struct {
	pending string
}

// feed 吃一段正文增量，返回可安全外放的文本；tripped=true 表示检测到工具调用语法泄漏。
func (g *toolCallLeakGuard) feed(text string) (safe string, tripped bool) {
	g.pending += text
	for _, m := range textToolCallMarkers {
		if strings.Contains(g.pending, m) {
			g.pending = ""
			return "", true
		}
	}
	hold := markerPrefixHold(g.pending)
	safe, g.pending = g.pending[:len(g.pending)-hold], g.pending[len(g.pending)-hold:]
	return safe, false
}

// flush 返回收尾时剩余的暂留文本（确认不是任何标记的前缀，可安全放行）。
func (g *toolCallLeakGuard) flush() string {
	s := g.pending
	g.pending = ""
	return s
}

// markerPrefixHold 返回 s 末尾「可能是某个标记前缀」的最长长度——这部分需暂留待后续 chunk 判定。
func markerPrefixHold(s string) int {
	hold := 0
	for _, m := range textToolCallMarkers {
		n := min(len(m), len(s))
		for k := n; k > hold; k-- {
			if strings.HasPrefix(m, s[len(s)-k:]) {
				hold = k
				break
			}
		}
	}
	return hold
}
