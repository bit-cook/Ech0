// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

// Package agent 是 Ech0 的 LLM 核心：把多家协议（OpenAI 兼容 / Anthropic）的
// 生成能力收口为统一的 Provider 抽象。对外暴露两个入口：
//   - Generate：非流式、无工具，用于近期总结（summary）；
//   - Run：ReAct 工具循环（function calling），模型一轮内自主决定是否检索，用于 Chat。
package agent

import (
	"context"
	"errors"

	commonModel "github.com/lin-snow/ech0/internal/model/common"
	model "github.com/lin-snow/ech0/internal/model/setting"
)

const (
	// GEN_RECENT 是近期总结的缓存 key。
	GEN_RECENT = "gen_recent"
)

// validate 校验 AgentSetting 是否可用于生成。
func validate(setting model.AgentSetting) error {
	if !setting.Enable {
		return errors.New(commonModel.AGENT_NOT_ENABLED)
	}
	if setting.Model == "" {
		return errors.New(commonModel.AGENT_MODEL_MISSING)
	}
	if setting.Protocol == "" {
		return errors.New(commonModel.AGENT_PROTOCOL_NOT_FOUND)
	}
	if setting.ApiKey == "" && !allowsEmptyAPIKey(setting.Protocol) {
		// OpenAI 兼容 / OpenAI Responses 场景下 Ollama 等本地服务允许空 ApiKey
		return errors.New(commonModel.AGENT_API_KEY_MISSING)
	}
	return nil
}

// allowsEmptyAPIKey 标记哪些协议容许空 ApiKey（本地 OpenAI 兼容服务，如 Ollama）。
func allowsEmptyAPIKey(protocol string) bool {
	return protocol == string(commonModel.OpenAI) || protocol == string(commonModel.OpenAIResponses)
}

// applyPrompt 在 usePrompt 且配置了自定义 Prompt 时，把它作为 user 消息追加在末尾
// （维持历史行为）。
func applyPrompt(setting model.AgentSetting, in []Message, usePrompt bool) []Message {
	if setting.Prompt != "" && usePrompt {
		in = append(in, Message{Role: RoleUser, Content: setting.Prompt})
	}
	return in
}

// Generate 调用配置的 LLM 提供商生成回复（非流式）。temperature 为 nil 时不设置。
func Generate(
	ctx context.Context,
	setting model.AgentSetting,
	in []Message,
	usePrompt bool,
	temperature *float32,
) (string, error) {
	if err := validate(setting); err != nil {
		return "", err
	}

	provider, err := providerFor(setting)
	if err != nil {
		return "", err
	}

	resp, err := provider.Complete(ctx, Request{
		Messages:    applyPrompt(setting, in, usePrompt),
		Temperature: temperature,
	})
	if err != nil {
		return "", err
	}
	// 剥离推理模型内联的 <think> 块，避免思维过程混进答案（如近期总结 Widget）。
	return stripReasoning(resp.Text), nil
}

// Ping 用给定配置发起一次最小的非流式请求，验证协议 / BaseURL / ApiKey / Model 是否真正可用
// （连通性测试）。与 Generate 不同：不要求 Enable（允许保存前先测），其余必填项仍校验。
//
// MaxTokens 取 16 而非 1：Anthropic 在 max_tokens=1 时可能未吐出任何文本即触顶，触发
// Complete 的「empty text」判定，造成「其实连通却报错」的假阴性；16 token 足够拿到回包，
// 成本仍可忽略。返回 nil 即视为连通。
func Ping(ctx context.Context, setting model.AgentSetting) error {
	if setting.Model == "" {
		return errors.New(commonModel.AGENT_MODEL_MISSING)
	}
	if setting.Protocol == "" {
		return errors.New(commonModel.AGENT_PROTOCOL_NOT_FOUND)
	}
	if setting.ApiKey == "" && !allowsEmptyAPIKey(setting.Protocol) {
		// OpenAI 兼容 / OpenAI Responses 场景下 Ollama 等本地服务允许空 ApiKey（与 validate 保持一致）
		return errors.New(commonModel.AGENT_API_KEY_MISSING)
	}

	provider, err := providerFor(setting)
	if err != nil {
		return err
	}

	_, err = provider.Complete(ctx, Request{
		Messages:  []Message{{Role: RoleUser, Content: "ping"}},
		MaxTokens: 16,
	})
	return err
}
