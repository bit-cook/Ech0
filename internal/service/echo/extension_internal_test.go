// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package service

import (
	"encoding/json"
	"testing"

	model "github.com/lin-snow/ech0/internal/model/echo"
	fileModel "github.com/lin-snow/ech0/internal/model/file"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNormalizeEchoExtension_AllTypes 补齐 echo_test.go 未覆盖的扩展类型分支：
// 每种类型的成功归一化与必填字段缺失/越界的拒绝。
func TestNormalizeEchoExtension_AllTypes(t *testing.T) {
	t.Run("empty type returns nil without error", func(t *testing.T) {
		// Type 为空白字符串 -> 视为「无扩展」，返回 nil,nil（即便 Payload 非空）。
		got, err := normalizeEchoExtension(&model.EchoExtension{Type: "   ", Payload: map[string]any{"x": "y"}})
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("nil payload is rejected", func(t *testing.T) {
		_, err := normalizeEchoExtension(&model.EchoExtension{Type: model.Extension_VIDEO})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "payload is required")
	})

	t.Run("video ok", func(t *testing.T) {
		got, err := normalizeEchoExtension(&model.EchoExtension{
			Type:    model.Extension_VIDEO,
			Payload: map[string]any{"videoId": "  abc123  "},
		})
		require.NoError(t, err)
		assert.Equal(t, "abc123", got.Payload["videoId"])
	})

	t.Run("video missing videoId", func(t *testing.T) {
		_, err := normalizeEchoExtension(&model.EchoExtension{
			Type:    model.Extension_VIDEO,
			Payload: map[string]any{"videoId": "   "},
		})
		require.Error(t, err)
	})

	t.Run("github project ok", func(t *testing.T) {
		got, err := normalizeEchoExtension(&model.EchoExtension{
			Type:    model.Extension_GITHUBPROJ,
			Payload: map[string]any{"repoUrl": "https://github.com/lin-snow/ech0"},
		})
		require.NoError(t, err)
		assert.NotEmpty(t, got.Payload["repoUrl"])
	})

	t.Run("github project missing repoUrl", func(t *testing.T) {
		_, err := normalizeEchoExtension(&model.EchoExtension{
			Type:    model.Extension_GITHUBPROJ,
			Payload: map[string]any{},
		})
		require.Error(t, err)
	})

	t.Run("website ok", func(t *testing.T) {
		got, err := normalizeEchoExtension(&model.EchoExtension{
			Type:    model.Extension_WEBSITE,
			Payload: map[string]any{"title": "Ech0", "site": "https://ech0.app"},
		})
		require.NoError(t, err)
		assert.Equal(t, "Ech0", got.Payload["title"])
		assert.NotEmpty(t, got.Payload["site"])
	})

	t.Run("website missing site", func(t *testing.T) {
		_, err := normalizeEchoExtension(&model.EchoExtension{
			Type:    model.Extension_WEBSITE,
			Payload: map[string]any{"title": "Ech0"},
		})
		require.Error(t, err)
	})

	t.Run("music missing url", func(t *testing.T) {
		_, err := normalizeEchoExtension(&model.EchoExtension{
			Type:    model.Extension_MUSIC,
			Payload: map[string]any{"url": "  "},
		})
		require.Error(t, err)
	})

	t.Run("location ok", func(t *testing.T) {
		got, err := normalizeEchoExtension(&model.EchoExtension{
			Type: model.Extension_LOCATION,
			Payload: map[string]any{
				"latitude":    31.23,
				"longitude":   121.47,
				"placeholder": "Shanghai",
			},
		})
		require.NoError(t, err)
		assert.InDelta(t, 31.23, got.Payload["latitude"], 1e-9)
		assert.InDelta(t, 121.47, got.Payload["longitude"], 1e-9)
		assert.Equal(t, "Shanghai", got.Payload["placeholder"])
	})

	t.Run("location missing coordinates", func(t *testing.T) {
		_, err := normalizeEchoExtension(&model.EchoExtension{
			Type:    model.Extension_LOCATION,
			Payload: map[string]any{"placeholder": "x"},
		})
		require.Error(t, err)
	})

	t.Run("location out of range", func(t *testing.T) {
		_, err := normalizeEchoExtension(&model.EchoExtension{
			Type: model.Extension_LOCATION,
			Payload: map[string]any{
				"latitude":    100.0, // > 90
				"longitude":   0.0,
				"placeholder": "x",
			},
		})
		require.Error(t, err)
	})

	t.Run("location missing placeholder", func(t *testing.T) {
		_, err := normalizeEchoExtension(&model.EchoExtension{
			Type: model.Extension_LOCATION,
			Payload: map[string]any{
				"latitude":  10.0,
				"longitude": 20.0,
			},
		})
		require.Error(t, err)
	})

	t.Run("tweet ok", func(t *testing.T) {
		got, err := normalizeEchoExtension(&model.EchoExtension{
			Type: model.Extension_TWEET,
			Payload: map[string]any{
				"url":      "https://x.com/u/status/1",
				"username": "u",
				"statusId": "1",
			},
		})
		require.NoError(t, err)
		assert.Equal(t, "u", got.Payload["username"])
		assert.Equal(t, "1", got.Payload["statusId"])
	})

	t.Run("tweet missing username", func(t *testing.T) {
		_, err := normalizeEchoExtension(&model.EchoExtension{
			Type: model.Extension_TWEET,
			Payload: map[string]any{
				"url":      "https://x.com/u/status/1",
				"statusId": "1",
			},
		})
		require.Error(t, err)
	})
}

// TestGetPayloadFloat 覆盖 JSON 数字落地的各种类型容错（float/int/json.Number/string）与失败回退。
func TestGetPayloadFloat(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		wantOK  bool
		want    float64
	}{
		{"float64", map[string]any{"v": float64(1.5)}, true, 1.5},
		{"float32", map[string]any{"v": float32(2.5)}, true, 2.5},
		{"int", map[string]any{"v": 3}, true, 3},
		{"int64", map[string]any{"v": int64(4)}, true, 4},
		{"json.Number", map[string]any{"v": json.Number("5.5")}, true, 5.5},
		{"string number", map[string]any{"v": " 6.25 "}, true, 6.25},
		{"invalid string", map[string]any{"v": "not-a-number"}, false, 0},
		{"bad json.Number", map[string]any{"v": json.Number("nan-ish")}, false, 0},
		{"missing key", map[string]any{}, false, 0},
		{"nil value", map[string]any{"v": nil}, false, 0},
		{"unsupported type", map[string]any{"v": true}, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := getPayloadFloat(tc.payload, "v")
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.InDelta(t, tc.want, got, 1e-9)
			}
		})
	}
}

// TestGetPayloadString_NonString 补齐 getPayloadString 的非字符串/缺失/nil 回退分支。
func TestGetPayloadString_NonString(t *testing.T) {
	assert.Equal(t, "", getPayloadString(map[string]any{"v": 123}, "v"))
	assert.Equal(t, "", getPayloadString(map[string]any{"v": nil}, "v"))
	assert.Equal(t, "", getPayloadString(map[string]any{}, "missing"))
	assert.Equal(t, "ok", getPayloadString(map[string]any{"v": "ok"}, "v"))
}

// TestCollectEchoFileIDs 覆盖文件 ID 收集的各分支：nil、空、FileID 优先、回退 File.ID、两者皆空跳过。
func TestCollectEchoFileIDs(t *testing.T) {
	t.Run("nil echo", func(t *testing.T) {
		assert.Nil(t, collectEchoFileIDs(nil))
	})

	t.Run("no files", func(t *testing.T) {
		assert.Nil(t, collectEchoFileIDs(&model.Echo{}))
	})

	t.Run("prefers FileID, falls back to File.ID, skips empty", func(t *testing.T) {
		echo := &model.Echo{
			EchoFiles: []fileModel.EchoFile{
				{FileID: "direct-id"},
				{FileID: "  ", File: fileModel.File{ID: "nested-id"}}, // FileID 空白 -> 用 File.ID
				{FileID: "   ", File: fileModel.File{ID: "  "}},       // 两者皆空 -> 跳过
			},
		}
		assert.Equal(t, []string{"direct-id", "nested-id"}, collectEchoFileIDs(echo))
	})
}
