// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package model

import (
	uuidUtil "github.com/lin-snow/ech0/internal/util/uuid"
	"gorm.io/gorm"
)

// Webhook 定义 Webhook 设置实体
type Webhook struct {
	ID          string `gorm:"type:char(36);primaryKey" json:"id"` // Webhook ID
	Name        string `                    json:"name"`           // Webhook 名称
	URL         string `                    json:"url"`            // Webhook URL
	Secret      string `                    json:"-"`              // 签名密钥，用于请求验证（HMAC等）
	IsActive    bool   `gorm:"default:true" json:"is_active"`      // 启用/禁用状态
	LastStatus  string `                    json:"last_status"`    // 最近调用状态（如 success, failed）
	LastTrigger int64  `                    json:"last_trigger"`   // 最近触发时间
	CreatedAt   int64  `gorm:"autoCreateTime" json:"created_at"`   // 创建时间
	UpdatedAt   int64  `gorm:"autoUpdateTime" json:"updated_at"`   // 更新时间
}

func (w *Webhook) BeforeCreate(_ *gorm.DB) error {
	if w.ID == "" {
		w.ID = uuidUtil.NewV7()
	}
	return nil
}
