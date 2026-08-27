// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package util

import (
	"errors"

	model "github.com/lin-snow/ech0/internal/model/common"
	logUtil "github.com/lin-snow/ech0/pkg/log"
)

// HandleError 处理错误信息，记录日志并返回错误消息（级别与可见性维持现状：Error）。
func HandleError(se *model.ServerError) string {
	if se.Err != nil {
		if se.Msg == "" {
			se.Msg = se.Err.Error()
		}
		logUtil.GetLogger().Error(se.Msg, logUtil.Err(se.Err))
	}

	return se.Msg
}

// ExtractBizErrorCode 从 error 链路中提取业务错误码。
func ExtractBizErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if bizErr, ok := errors.AsType[*model.BizError](err); ok {
		return bizErr.Code
	}
	return ""
}

// HandlePanicError 处理 panic 错误，记录日志（含调用栈）并触发 panic。
func HandlePanicError(se *model.ServerError) {
	if se.Err != nil {
		if se.Msg == "" {
			se.Msg = se.Err.Error()
		}
		logUtil.Panic(se.Msg, logUtil.Err(se.Err)) // 记录 panic 级别日志（含栈）后 panic
	}

	panic(se.Msg)
}
