// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package bus

import (
	"os"
	"testing"

	"github.com/lin-snow/ech0/pkg/leakcheck"
)

// TestMain 在本包测试跑完后跑一遍 goroutineleak 检测：订阅者可以是异步的，
// 退订与关停路径必须把投递 goroutine 收干净。
func TestMain(m *testing.M) {
	os.Exit(leakcheck.Run(m))
}
