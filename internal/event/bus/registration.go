// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package bus

import (
	"context"

	"github.com/lin-snow/ech0/pkg/busen"
)

// Registration 在给定总线上注册一条订阅并返回其取消函数。
type Registration func(*busen.Bus) (func(), error)

// Subscriber 是一组事件订阅的提供者，由各领域订阅者实现。
type Subscriber interface {
	Registrations() []Registration
}

// On 构造一条按 Go 类型路由的订阅（busen.Subscribe[T]），把强类型事件交给 handler。
func On[T any](handler func(context.Context, T) error, opts ...busen.SubscribeOption) Registration {
	return func(b *busen.Bus) (func(), error) {
		return b.Subscribe(func(ctx context.Context, e busen.Event[T]) error {
			return handler(ctx, e.Value)
		}, opts...)
	}
}

// OnWithMeta 同 On，但把 busen 信封的 Meta（source 等元数据）一并交给 handler。
// webhook 桥接需要 Meta 来填充中立观察，故走它而非 On —— On 只透传 Value。
func OnWithMeta[T any](
	handler func(context.Context, T, map[string]string) error,
	opts ...busen.SubscribeOption,
) Registration {
	return func(b *busen.Bus) (func(), error) {
		return b.Subscribe(func(ctx context.Context, e busen.Event[T]) error {
			return handler(ctx, e.Value, e.Meta)
		}, opts...)
	}
}
