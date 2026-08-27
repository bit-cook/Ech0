// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package webhook

import (
	"os"
	"testing"

	"github.com/lin-snow/ech0/pkg/leakcheck"
)

// TestMain 在本包测试跑完后跑一遍 goroutineleak 检测：分发器为每个 webhook 起投递
// goroutine，重试与超时路径一旦漏收就会随事件量线性堆积。
func TestMain(m *testing.M) {
	os.Exit(leakcheck.Run(m))
}
