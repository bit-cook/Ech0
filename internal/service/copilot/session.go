// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lin-snow/ech0/internal/agent"
	commonModel "github.com/lin-snow/ech0/internal/model/common"
	embeddingModel "github.com/lin-snow/ech0/internal/model/embedding"
	logUtil "github.com/lin-snow/ech0/pkg/log"
	"github.com/lin-snow/ech0/pkg/viewer"
)

// maxStoredChatMessages 是单个用户持久化会话保留的最大消息条数（超出取最近 N 条）。
const maxStoredChatMessages = 50

// maxHistoryTokens 是注入模型的会话历史 token 预算（保守固定值，与模型窗口解耦）。
// 微博客问答通常很短，4000 token ≈ 十几轮，留足窗口给 system + 本轮工具结果 + 本轮问题。
const maxHistoryTokens = 4000

// toolDefTokenEstimate 是注入模型的工具定义（search_echos + summarize_echos 的描述 + JSON Schema）
// 的粗略 token 估算，计入固定开销以收紧历史预算（整请求护栏，避免 system + 工具定义 + 历史叠加超窗）。
const toolDefTokenEstimate = 640

// minHistoryTokens 是历史预算下限：即便固定开销很大，也至少给历史留这点空间（保留最近若干轮）。
const minHistoryTokens = 500

// estimateTokens 是不引 tokenizer 的廉价启发式：按 rune 数估算（CJK≈1 token/字，
// 拉丁会高估 → 偏早截断，安全无害）。仅用于历史裁剪预算，不要求精确。
func estimateTokens(s string) int { return utf8.RuneCountInString(s) }

// historyForModel 把展示用会话投影成喂模型的历史（展示 transcript 与模型 context 分离）：
//   - 取 Role+Content；旧轮丢弃 Sources（已过时，模型需旧细节会经 search_echos 自行重检索），
//     但「最近一条带 Sources 的 assistant」把其检索结果折进文本（复用 formatSearchResults），
//     兜住「追问上一轮结果细节」的场景；
//   - 跳过 Content 为空且无折入内容的消息（如模型未产文本的空 assistant 轮）；
//   - 从最近往回按 budgetTokens 累加截断（计入折入的 sources 文本），始终至少保留最近一条；
//   - 返回时恢复时间正序。
func historyForModel(msgs []ChatMessage, locale string, budgetTokens int, loc *time.Location) []agent.Message {
	if len(msgs) == 0 {
		return nil
	}

	// 定位最近一条带非空 Sources 的 assistant；仅此一轮把检索原文折进文本。
	lastSourced := -1
	for i, msg := range slices.Backward(msgs) {
		if msg.Role == "assistant" && len(msg.Sources) > 0 {
			lastSourced = i
			break
		}
	}

	// contentOf 取消息文本，并在 lastSourced 处折入最近一轮的检索依据。
	contentOf := func(i int) string {
		c := strings.TrimSpace(msgs[i].Content)
		if i != lastSourced {
			return c
		}
		// 历史折叠用持久化的 sources 文本快照，不再回查 Extension（旧轮无需精确）。
		note := fmt.Sprintf(recentSourcesNoteFor(locale), formatSearchResults(msgs[i].Sources, nil, loc))
		if c == "" {
			return note
		}
		return c + "\n\n" + note
	}

	// 反向遍历累加 token，超预算即停（但至少留最近一条，避免极小预算下返回空）。
	collected := make([]agent.Message, 0, len(msgs))
	used := 0
	for i, msg := range slices.Backward(msgs) {
		content := contentOf(i)
		if content == "" {
			continue
		}
		if t := estimateTokens(content); used+t > budgetTokens && len(collected) > 0 {
			break
		} else {
			used += t
		}
		collected = append(collected, agent.Message{Role: roleFromString(msg.Role), Content: content})
	}

	// collected 为逆序（最近在前），反转回时间正序。
	for l, r := 0, len(collected)-1; l < r; l, r = l+1, r-1 {
		collected[l], collected[r] = collected[r], collected[l]
	}
	return collected
}

// roleFromString 把持久化的角色字符串映射成 agent.Role（持久化里只有 user/assistant）。
func roleFromString(r string) agent.Role {
	if r == "assistant" {
		return agent.RoleAssistant
	}
	return agent.RoleUser
}

// ChatMessage 是持久化在 KeyValue 里的一条聊天消息（仅做展示恢复，模型不读历史）。
//
// Reasoning / ReasoningMs 仅 assistant 轮有意义：推理过程文本与其耗时（毫秒），用于前端折叠展示
// 「已思考（用时 X 秒）」。**只供展示回显，绝不进模型上下文**——historyForModel 只取 Content，
// 故 reasoning 天然不会被回灌给模型。
type ChatMessage struct {
	Role        string                        `json:"role"`
	Content     string                        `json:"content"`
	Sources     []embeddingModel.SearchResult `json:"sources,omitempty"`
	Reasoning   string                        `json:"reasoning,omitempty"`
	ReasoningMs int64                         `json:"reasoning_ms,omitempty"`
}

// chatSessionKey 按 userID 生成会话键（每个用户一条会话）。
func chatSessionKey(userID string) string {
	return commonModel.ChatSessionKeyPrefix + userID
}

// loadSession 读取某用户的持久化会话；userID 为空、未命中或解析失败都返回 nil（best-effort）。
func (s *CopilotService) loadSession(ctx context.Context, userID string) []ChatMessage {
	if userID == "" {
		return nil
	}
	raw, err := s.durableKV.Get(ctx, chatSessionKey(userID))
	if err != nil {
		return nil
	}
	var msgs []ChatMessage
	if err := json.Unmarshal([]byte(raw), &msgs); err != nil {
		return nil
	}
	return msgs
}

// appendTurn 把本轮消息追加进用户会话并落盘（封顶最近 maxStoredChatMessages 条）。
// userID 为空直接跳过；任何失败仅告警，不返回错误（best-effort，不影响主流程）。
func (s *CopilotService) appendTurn(ctx context.Context, userID string, turn ...ChatMessage) {
	if userID == "" {
		return
	}
	msgs := append(s.loadSession(ctx, userID), turn...)
	if len(msgs) > maxStoredChatMessages {
		msgs = msgs[len(msgs)-maxStoredChatMessages:]
	}
	payload, err := json.Marshal(msgs)
	if err != nil {
		logUtil.GetLogger().Warn("failed to marshal chat session",
			slog.String("module", "copilot"), logUtil.Err(err))
		return
	}
	if err := s.durableKV.Set(ctx, chatSessionKey(userID), string(payload)); err != nil {
		logUtil.GetLogger().Warn("failed to persist chat session",
			slog.String("module", "copilot"), logUtil.Err(err))
	}
}

// assistantTurn 收束本轮 assistant 的可持久化产物：答案、来源、推理过程及其耗时。
type assistantTurn struct {
	answer      string
	sources     []embeddingModel.SearchResult
	reasoning   string
	reasoningMs int64
}

// persistTurn 在一轮问答正常收尾时把 user/assistant 两条消息追加进持久化会话。
// 「答案为空且无来源」视为失败/空轮次（模型一个 token 都没产出）：直接跳过落盘，
// 既不在持久化会话里留下永久空白气泡，也让前端可就地重发后历史仍保持干净
// （重发会以同一问题重新跑一轮并在成功时落盘）。
func (s *CopilotService) persistTurn(ctx context.Context, userID, question string, turn assistantTurn) {
	if strings.TrimSpace(turn.answer) == "" && len(turn.sources) == 0 {
		return
	}
	s.appendTurn(ctx, userID,
		ChatMessage{Role: "user", Content: question},
		ChatMessage{
			Role:        "assistant",
			Content:     turn.answer,
			Sources:     turn.sources,
			Reasoning:   turn.reasoning,
			ReasoningMs: turn.reasoningMs,
		},
	)
}

// GetSession 返回当前登录用户的持久化会话（无会话时返回空切片，便于前端拿到数组）。
func (s *CopilotService) GetSession(ctx context.Context) ([]ChatMessage, error) {
	userID := viewer.MustFromContext(ctx).UserID()
	msgs := s.loadSession(ctx, userID)
	if msgs == nil {
		return []ChatMessage{}, nil
	}
	return msgs, nil
}

// ClearSession 删除当前登录用户的持久化会话（不可恢复，由前端二次确认）。
func (s *CopilotService) ClearSession(ctx context.Context) error {
	userID := viewer.MustFromContext(ctx).UserID()
	if userID == "" {
		return nil
	}
	return s.durableKV.Delete(ctx, chatSessionKey(userID))
}
