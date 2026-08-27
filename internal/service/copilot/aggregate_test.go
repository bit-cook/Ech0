// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package service

import (
	"context"
	"strings"
	"testing"
	"time"

	commonModel "github.com/lin-snow/ech0/internal/model/common"
	echoModel "github.com/lin-snow/ech0/internal/model/echo"
)

// pagingEchoSvc 是 EchoService 的测试替身，只覆写 QueryEchos：按 created_at 倒序的固定数据集
// 分页返回。它**故意把每页压到 serverMaxPageSize（小于 collectRange 的请求值）**——以此压测
// collectRange 是否「按 total 续翻直到取尽」，而非依赖「本页是否满」来终止（那正是漏数据 bug 的根因）。
// 其余方法不实现（嵌入 nil 接口，未调用即不 panic），collectRange 只用到 QueryEchos。
type pagingEchoSvc struct {
	EchoService                  // 嵌入接口（nil）：满足类型，未覆写的方法被调用才 panic
	all         []echoModel.Echo // 已按 CreatedAt 倒序（最新在前），模拟真实查询返回序
	gotUserID   string           // 记录最近一次 QueryEchos 收到的 DTO.UserID（验证检索按作者收口）
}

// serverMaxPageSize 是替身的「服务端实际页上限」，刻意小于 aggregatePageSize，
// 使每页返回数 < 请求数，从而暴露任何「靠本页未满判定取尽」的终止逻辑回归。
const serverMaxPageSize = 40

func (f *pagingEchoSvc) QueryEchos(
	_ context.Context,
	dto commonModel.EchoQueryDto,
) (commonModel.PageQueryResult[[]echoModel.Echo], error) {
	f.gotUserID = dto.UserID
	ps := dto.PageSize
	if ps < 1 || ps > serverMaxPageSize { // 压到服务端实际上限（小于请求值）
		ps = serverMaxPageSize
	}
	page := max(dto.Page, 1)
	start := (page - 1) * ps
	total := int64(len(f.all))
	if start >= len(f.all) {
		return commonModel.PageQueryResult[[]echoModel.Echo]{Items: nil, Total: total}, nil
	}
	end := min(start+ps, len(f.all))
	return commonModel.PageQueryResult[[]echoModel.Echo]{Items: f.all[start:end], Total: total}, nil
}

// makeEchos 生成 n 条按时间倒序（最新在前）的 Echo，跨多个月分布。
func makeEchos(n int) []echoModel.Echo {
	echos := make([]echoModel.Echo, 0, n)
	base := time.Date(2025, 7, 1, 12, 0, 0, 0, time.UTC)
	for i := range n {
		// 每条间隔约 1.8 天，n=102 时跨越 ~6 个月（7~12 月）。
		ts := base.Add(time.Duration(i) * 43 * time.Hour)
		echos = append(echos, echoModel.Echo{ID: string(rune('a' + i%26)), Content: "echo", CreatedAt: ts.Unix()})
	}
	// 倒序（最新在前），模拟 QueryEchos 的 created_at DESC。
	for l, r := 0, len(echos)-1; l < r; l, r = l+1, r-1 {
		echos[l], echos[r] = echos[r], echos[l]
	}
	return echos
}

// 回归：collectRange 必须取尽整个区间，而非因服务层下调页大小而只拿回第一页。
// 这正是「下半年总结只覆盖 12 月」bug 的根因——之前请求 PageSize=200 被重置为 10，
// 且终止条件误用「本页未满」。
func TestCollectRange_PaginatesAll(t *testing.T) {
	const n = 102
	svc := &pagingEchoSvc{all: makeEchos(n)}
	s := &CopilotService{echoService: svc}

	echos, total, truncated, err := s.collectRange(context.Background(), "", nil, 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != n {
		t.Fatalf("total = %d, want %d", total, n)
	}
	if len(echos) != n {
		t.Fatalf("collected %d echos, want %d（疑似只拿回第一页）", len(echos), n)
	}
	if truncated {
		t.Fatalf("truncated = true, want false（未触顶硬上限）")
	}
}

// collectRange 必须把当前用户 ID 透传进 QueryEchos 的 DTO，使区间聚合（年终/区间总结、
// stats_overview）只覆盖本人发布的 Echo——多用户实例下不混入他人内容。
func TestCollectRange_ScopesByUser(t *testing.T) {
	svc := &pagingEchoSvc{all: makeEchos(3)}
	s := &CopilotService{echoService: svc}

	if _, _, _, err := s.collectRange(context.Background(), "user-42", nil, 0, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.gotUserID != "user-42" {
		t.Fatalf("QueryEchos saw UserID = %q, want %q（区间聚合未按作者收口）", svc.gotUserID, "user-42")
	}
}

// queryEchos（search_echos 的 SQL 路径）同样必须按当前用户收口。
func TestQueryEchos_ScopesByUser(t *testing.T) {
	svc := &pagingEchoSvc{all: makeEchos(3)}
	s := &CopilotService{echoService: svc}

	if _, _, err := s.queryEchos(context.Background(), "user-7", "三体", nil, 0, 0, 5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.gotUserID != "user-7" {
		t.Fatalf("QueryEchos saw UserID = %q, want %q（点查未按作者收口）", svc.gotUserID, "user-7")
	}
}

// 触顶 maxAggregateEchos 时应停在上限并标记 truncated。
func TestCollectRange_TruncatesAtCap(t *testing.T) {
	svc := &pagingEchoSvc{all: makeEchos(maxAggregateEchos + 50)}
	s := &CopilotService{echoService: svc}

	echos, total, truncated, err := s.collectRange(context.Background(), "", nil, 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(echos) != maxAggregateEchos {
		t.Fatalf("collected %d, want cap %d", len(echos), maxAggregateEchos)
	}
	if total != int64(maxAggregateEchos+50) {
		t.Fatalf("total = %d, want %d", total, maxAggregateEchos+50)
	}
	if !truncated {
		t.Fatalf("truncated = false, want true（超过硬上限）")
	}
}

// chunkEchosByBudget：每块体量 ≤ budget（单条超额自成一块除外），且全部 Echo 按序无丢无重。
func TestChunkEchosByBudget(t *testing.T) {
	echos := makeEchos(50)
	budget := estimateTokens(formatEchoLine(echos[0], time.UTC)) * 7 // 约 7 条一块
	chunks := chunkEchosByBudget(echos, budget, time.UTC)
	if len(chunks) < 2 {
		t.Fatalf("got %d chunk(s), expected multiple", len(chunks))
	}

	seen := 0
	for _, ch := range chunks {
		if len(ch) == 0 {
			t.Fatalf("empty chunk")
		}
		tok := 0
		for _, e := range ch {
			tok += estimateTokens(formatEchoLine(e, time.UTC))
		}
		if len(ch) > 1 && tok > budget {
			t.Fatalf("multi-echo chunk exceeds budget: %d > %d", tok, budget)
		}
		seen += len(ch)
	}
	if seen != len(echos) {
		t.Fatalf("chunks cover %d echos, want %d（顺序/完整性被破坏）", seen, len(echos))
	}
}

// formatEchosByMonth：按月分组、带计数小标题，且月份齐全。
func TestFormatEchosByMonth(t *testing.T) {
	echos := makeEchos(102)
	out := formatEchosByMonth(echos, time.UTC)
	for _, m := range []string{"## 2025-07", "## 2025-08", "## 2025-09", "## 2025-10", "## 2025-11", "## 2025-12"} {
		if !strings.Contains(out, m) {
			t.Fatalf("月份小标题缺失：%q\n%s", m, out)
		}
	}
}

// formatEchoLine：标签 / 配图计数 / 纯图标记都进入材料。
func TestFormatEchoLine_Enrichment(t *testing.T) {
	withText := echoModel.Echo{
		Content:   "今天读完一本书",
		CreatedAt: time.Date(2025, 3, 4, 0, 0, 0, 0, time.UTC).Unix(),
		Tags:      []echoModel.Tag{{Name: "读书"}},
	}
	line := formatEchoLine(withText, time.UTC)
	if !strings.Contains(line, "#读书") || !strings.Contains(line, "今天读完一本书") {
		t.Fatalf("缺标签或正文：%q", line)
	}
}
