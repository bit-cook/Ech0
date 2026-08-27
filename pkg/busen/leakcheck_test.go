// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package busen

import (
	"os"
	"testing"

	"github.com/lin-snow/ech0/pkg/leakcheck"
)

// TestMain 在本包测试跑完后跑一遍 goroutineleak 检测：异步邮箱、并行 worker
// 与三种 shutdown 模式的正确性最终都体现在「goroutine 是否收尾」上。
func TestMain(m *testing.M) {
	os.Exit(leakcheck.Run(m))
}
