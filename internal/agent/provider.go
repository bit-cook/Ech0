// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package agent

import (
	"context"
	"errors"

	commonModel "github.com/lin-snow/ech0/internal/model/common"
	model "github.com/lin-snow/ech0/internal/model/setting"
)

// Provider 是某个 LLM 协议（OpenAI 兼容 / Anthropic）的适配层。
//
// 各家 SDK 的差异——尤其流式 tool_call 的分片拼接——封死在各自实现内部，
// 对上层只暴露统一的 Complete（非流式）与 Stream（语义 Event 流）。
type Provider interface {
	// Complete 非流式生成，返回完整文本（Generate 近期总结用，不涉及工具）。
	Complete(ctx context.Context, req Request) (Response, error)
	// Stream 流式生成，返回只读 Event channel，结束时关闭；
	// 文本以 EventTextDelta 实时上浮，工具调用拼装完整后以 EventToolCall 上浮，
	// 传输/协议错误以 EventError 上浮。创建期错误经返回值回传。
	Stream(ctx context.Context, req Request) (<-chan Event, error)
}

// providerFor 按 AgentSetting.Protocol 选择 Provider 实现。
// 未知协议（含已下线的 gemini）返回 AGENT_PROTOCOL_NOT_FOUND。
func providerFor(setting model.AgentSetting) (Provider, error) {
	switch setting.Protocol {
	case string(commonModel.OpenAI):
		return &openaiProvider{setting: setting}, nil
	case string(commonModel.OpenAIResponses):
		return &openaiResponsesProvider{setting: setting}, nil
	case string(commonModel.Anthropic):
		return &anthropicProvider{setting: setting}, nil
	default:
		return nil, errors.New(commonModel.AGENT_PROTOCOL_NOT_FOUND)
	}
}
