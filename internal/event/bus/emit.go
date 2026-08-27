// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package bus

import (
	"context"
	"log/slog"

	"github.com/lin-snow/ech0/internal/event"
	"github.com/lin-snow/ech0/pkg/busen"
	logUtil "github.com/lin-snow/ech0/pkg/log"
)

// Emit 按事件类型发布到总线（busen 按精确 Go 类型路由，无需 topic）。若事件实现 event.Keyed，
// 则带上排序 key 以获得 busen 的 per-key 局部有序。
func Emit[T any](ctx context.Context, b *busen.Bus, evt T) error {
	var opts []busen.PublishOption
	if k, ok := any(evt).(event.Keyed); ok {
		if key := k.OrderingKey(); key != "" {
			opts = append(opts, busen.WithKey(key))
		}
	}
	return b.Publish(ctx, evt, opts...)
}

// Notify 发布一个“最佳努力”副作用事件：失败仅以 Warn 记录（带事件名），绝不影响主流程。
// 适用于 webhook / 索引 / 缓存失效等旁路通知 —— 既不该阻断业务，也不该静默吞掉错误。
func Notify[T any](ctx context.Context, b *busen.Bus, evt T) {
	if err := Emit(ctx, b, evt); err != nil {
		name := ""
		if n, ok := any(evt).(event.Named); ok {
			name = n.EventName()
		}
		logUtil.GetLogger().Warn("event publish failed", slog.String("event", name), logUtil.Err(err))
	}
}
