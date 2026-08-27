// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package leakcheck

import (
	"runtime"
	"strings"
	"testing"
)

func TestParseTotal(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		want    int
		wantErr bool
	}{
		{name: "no leaks", header: "goroutineleak profile: total 0", want: 0},
		{name: "several leaks", header: "goroutineleak profile: total 12", want: 12},
		{name: "missing count", header: "goroutineleak profile: total", wantErr: true},
		{name: "not a number", header: "goroutineleak profile: total many", wantErr: true},
		{name: "foreign header", header: "goroutine profile: 3", wantErr: true},
		{name: "empty", header: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTotal(tt.header)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseTotal(%q) = %d, want error", tt.header, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTotal(%q) error = %v", tt.header, err)
			}
			if got != tt.want {
				t.Fatalf("parseTotal(%q) = %d, want %d", tt.header, got, tt.want)
			}
		})
	}
}

// TestProfileCountsLeakedGoroutine 是这道门禁的端到端证明：一条阻塞在不可达 channel 上的
// goroutine 必须被算作泄漏，且它的栈要出现在报告里（报告是排障时唯一有用的东西）。
func TestProfileCountsLeakedGoroutine(t *testing.T) {
	_, before, err := profile()
	if err != nil {
		t.Fatalf("profile() error = %v", err)
	}

	leakOneGoroutine()

	// 泄漏 goroutine 从「已被调度」到「真正阻塞」之间有一个无法被外部观测的窗口
	// （能观测就意味着还持有引用，那它就不是泄漏了），所以这里有界重试而不是一次断言。
	for attempt := range 100 {
		report, after, err := profile()
		if err != nil {
			t.Fatalf("profile() error = %v", err)
		}
		if after == before+1 {
			if !strings.Contains(report, "leakOneGoroutine") {
				t.Fatalf("report does not name the leaking function:\n%s", report)
			}
			return
		}
		if after != before {
			t.Fatalf("leak count = %d, want %d or %d (attempt %d)", after, before, before+1, attempt)
		}
		runtime.Gosched()
	}
	t.Fatal("leaked goroutine was never reported by the goroutineleak profile")
}

// leakOneGoroutine 泄漏一条 goroutine：两个 channel 都是局部的，函数返回后 blocked
// 从任何可运行 goroutine 都不可达，于是接收方永远不可能被唤醒。
// started 只用来确保这条 goroutine 已经开跑，避免检测跑在它被调度之前。
func leakOneGoroutine() {
	started := make(chan struct{})
	blocked := make(chan struct{})
	go func() {
		close(started)
		<-blocked
	}()
	<-started
}
