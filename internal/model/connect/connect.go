// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package model

import (
	uuidUtil "github.com/lin-snow/ech0/internal/util/uuid"
	"gorm.io/gorm"
)

// Connect 定义可读取的连接信息
type Connect struct {
	ServerName  string `json:"server_name"`  // 服务器名称
	ServerURL   string `json:"server_url"`   // 服务器地址
	Logo        string `json:"logo"`         // 站点logo
	TotalEchos  int    `json:"total_echos"`  // 总共发布数量
	TodayEchos  int    `json:"today_echos"`  // 今日发布数量
	SysUsername string `json:"sys_username"` // 系统管理员用户名
	Version     string `json:"version"`      // 实例版本
}

// Connected 定义添加的连接信息
type Connected struct {
	ID         string `gorm:"type:char(36);primaryKey" json:"id"`
	ConnectURL string `                  json:"connect_url"` // 连接地址
}

// ConnectedHealth 管理后台展示的单个互联项健康状态（由本机后端探测远端 /api/connect）
type ConnectedHealth struct {
	ID         string `json:"id"`
	ConnectURL string `json:"connect_url"`
	Status     string `json:"status"` // online | offline
	Version    string `json:"version"`
}

func (c *Connected) BeforeCreate(_ *gorm.DB) error {
	if c.ID == "" {
		c.ID = uuidUtil.NewV7()
	}
	return nil
}
