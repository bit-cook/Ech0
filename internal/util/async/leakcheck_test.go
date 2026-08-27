// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package async

import (
	"os"
	"testing"

	"github.com/lin-snow/ech0/pkg/leakcheck"
)

// TestMain 在本包测试跑完后跑一遍 goroutineleak 检测：这个包本身就是 worker pool，
// 漏掉一条 worker 就是一条永不回收的栈，而这类回归靠人眼 review 看不出来。
func TestMain(m *testing.M) {
	os.Exit(leakcheck.Run(m))
}
