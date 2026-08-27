// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package job_test

import (
	"os"
	"testing"

	"github.com/lin-snow/ech0/pkg/leakcheck"
)

// TestMain 在本包测试跑完后跑一遍 goroutineleak 检测：调度器持有长跑 runner，
// 取消与关停必须让它们真正退出。
func TestMain(m *testing.M) {
	os.Exit(leakcheck.Run(m))
}
