// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package service

import (
	"context"
	"errors"
	"time"

	"github.com/lin-snow/ech0/internal/agent"
	commonModel "github.com/lin-snow/ech0/internal/model/common"
	model "github.com/lin-snow/ech0/internal/model/setting"
	coreSetting "github.com/lin-snow/ech0/internal/setting"
	urlUtil "github.com/lin-snow/ech0/internal/util/url"
	"github.com/lin-snow/ech0/pkg/viewer"
)

// agentTestTimeout 是 LLM 连通性探测的整体超时，避免坏 endpoint 把请求挂死。
const agentTestTimeout = 15 * time.Second

// normalizeAgentProtocol 把协议字段归一到受支持的取值：未识别的接口协议（含已下线的 gemini）
// 一律按 OpenAI 兼容协议处理。UpdateAgentSettings 与 TestAgentConnection 共用。
func normalizeAgentProtocol(protocol string) string {
	switch protocol {
	case string(commonModel.OpenAI), string(commonModel.OpenAIResponses), string(commonModel.Anthropic):
		return protocol
	default:
		return string(commonModel.OpenAI)
	}
}

// GetAgentInfo 获取 Agent 信息（公开读，缺省值由 setting 引擎处理）。
func (settingService *SettingService) GetAgentInfo(setting *model.AgentSetting) error {
	v, err := coreSetting.Get(context.Background(), settingService.durableKV, coreSetting.Agent)
	if err != nil {
		return err
	}
	*setting = v
	return nil
}

// GetAgentSettings 获取 Agent 设置（管理员可见全量）。
func (settingService *SettingService) GetAgentSettings(
	ctx context.Context,
	setting *model.AgentSetting,
) error {
	// 检查用户权限
	userid := viewer.MustFromContext(ctx).UserID()
	user, err := settingService.commonService.CommonGetUserByUserId(ctx, userid)
	if err != nil {
		return err
	}
	if !user.IsAdmin {
		return errors.New(commonModel.NO_PERMISSION_DENIED)
	}

	v, err := coreSetting.Get(ctx, settingService.durableKV, coreSetting.Agent)
	if err != nil {
		return err
	}
	*setting = v
	return nil
}

// UpdateAgentSettings 更新 Agent 设置
func (settingService *SettingService) UpdateAgentSettings(
	ctx context.Context,
	newSetting *model.AgentSettingDto,
) error {
	// 检查用户权限
	userid := viewer.MustFromContext(ctx).UserID()
	user, err := settingService.commonService.CommonGetUserByUserId(ctx, userid)
	if err != nil {
		return err
	}
	if !user.IsAdmin {
		return errors.New(commonModel.NO_PERMISSION_DENIED)
	}

	setting := model.AgentSetting{
		Enable:     newSetting.Enable,
		Protocol:   normalizeAgentProtocol(newSetting.Protocol),
		Model:      newSetting.Model,
		ApiKey:     newSetting.ApiKey,
		Prompt:     newSetting.Prompt,
		BaseURL:    urlUtil.TrimURL(newSetting.BaseURL),
		Multimodal: newSetting.Multimodal,
		// 负数视为未配置，归零走保守默认。
		ContextWindow: max(0, newSetting.ContextWindow),
	}
	return coreSetting.Set(ctx, settingService.durableKV, coreSetting.Agent, setting)
}

// TestAgentConnection 用提交的 Agent 配置发起一次最小探活（不落库），验证 LLM 是否真正可用。
// 不依赖 Enable（允许保存前先测）；真正的探活在 agent.Ping 内完成。
func (settingService *SettingService) TestAgentConnection(
	ctx context.Context,
	newSetting *model.AgentSettingDto,
) error {
	userid := viewer.MustFromContext(ctx).UserID()
	user, err := settingService.commonService.CommonGetUserByUserId(ctx, userid)
	if err != nil {
		return err
	}
	if !user.IsAdmin {
		return errors.New(commonModel.NO_PERMISSION_DENIED)
	}

	setting := model.AgentSetting{
		Enable:   true, // 连通性测试不依赖启用开关
		Protocol: normalizeAgentProtocol(newSetting.Protocol),
		Model:    newSetting.Model,
		ApiKey:   newSetting.ApiKey,
		BaseURL:  urlUtil.TrimURL(newSetting.BaseURL),
	}

	ctx, cancel := context.WithTimeout(ctx, agentTestTimeout)
	defer cancel()
	return agent.Ping(ctx, setting)
}
