// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package cache

import (
	"os"
	"testing"

	"github.com/lin-snow/ech0/pkg/leakcheck"
)

// TestMain 在本包测试跑完后跑一遍 goroutineleak 检测：缓存后端带后台 goroutine，
// 且 singleflight 会把并发调用方挂在同一个 channel 上——漏唤醒就是永久挂死。
func TestMain(m *testing.M) {
	os.Exit(leakcheck.Run(m))
}
