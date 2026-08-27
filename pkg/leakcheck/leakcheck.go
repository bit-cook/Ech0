// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

// Package leakcheck 把 Go 1.27 起 GA 的 goroutineleak profile 接成一道测试门禁：
// 一个包的测试跑完后，如果运行时判定还有永远无法被唤醒的 goroutine，就让该测试二进制失败。
//
// 只依赖标准库，因此 internal/* 与 pkg/*（自持库，不得反向依赖 internal）可以共用同一份实现。
package leakcheck

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"runtime/pprof"
	"strconv"
	"strings"
	"testing"
)

// Run 跑完 m 的测试后做一次 goroutine 泄漏检测，返回该测试二进制应当退出的状态码。用法：
//
//	func TestMain(m *testing.M) { os.Exit(leakcheck.Run(m)) }
//
// 泄漏的判据来自运行时：goroutine 阻塞在某个并发原语上，而该原语从任何可运行 goroutine
// （及其可能唤醒的 goroutine）都不可达，于是永远不会被唤醒。
//
// 两处刻意的取舍：
//   - profile 是进程级的、由 GC 计算，所以只在全部测试结束后取一次。
//   - 测试已经失败时跳过检测——失败路径经常主动丢下 goroutine，泄漏报告只会淹没真正的失败。
//
// 判据基于可达性，意味着仍被全局变量或可运行 goroutine 引用的挂死 goroutine 不计入泄漏
// （运行时侧的固有限制）：这是零假阳性的下界检测，不是完备检测。
func Run(m *testing.M) int {
	code := m.Run()
	if code != 0 {
		return code
	}

	report, count, err := profile()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL\tgoroutine leak check: %v\n", err)
		return 1
	}
	if count == 0 {
		return 0
	}

	fmt.Fprintf(os.Stderr, "FAIL\tgoroutine leak check: %d leaked goroutine(s)\n%s", count, report)
	return 1
}

// profile 返回 debug=1 形式的 profile 正文与泄漏计数。WriteTo 本身会触发检测所需的 GC。
func profile() (string, int, error) {
	p := pprof.Lookup("goroutineleak")
	if p == nil {
		return "", 0, errors.New(`pprof profile "goroutineleak" is unavailable`)
	}

	var buf bytes.Buffer
	if err := p.WriteTo(&buf, 1); err != nil {
		return "", 0, fmt.Errorf("write goroutineleak profile: %w", err)
	}

	report := buf.String()
	header, _, _ := strings.Cut(report, "\n")
	count, err := parseTotal(header)
	if err != nil {
		return report, 0, err
	}
	return report, count, nil
}

// parseTotal 读取 profile 头行的计数，头行形如 "goroutineleak profile: total 3"。
func parseTotal(header string) (int, error) {
	prefix, total, ok := strings.CutLast(header, " ")
	if !ok || !strings.HasSuffix(prefix, "total") {
		return 0, fmt.Errorf("unexpected goroutineleak profile header %q", header)
	}
	count, err := strconv.Atoi(total)
	if err != nil {
		return 0, fmt.Errorf("unexpected goroutineleak profile header %q", header)
	}
	return count, nil
}
