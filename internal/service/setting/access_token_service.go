// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	authModel "github.com/lin-snow/ech0/internal/model/auth"
	commonModel "github.com/lin-snow/ech0/internal/model/common"
	model "github.com/lin-snow/ech0/internal/model/setting"
	userModel "github.com/lin-snow/ech0/internal/model/user"
	jwtUtil "github.com/lin-snow/ech0/internal/util/jwt"
	uuidUtil "github.com/lin-snow/ech0/internal/util/uuid"
	"github.com/lin-snow/ech0/pkg/viewer"
)

// ListAccessTokens 列出访问令牌
func (settingService *SettingService) ListAccessTokens(
	ctx context.Context,
) ([]model.AccessTokenSetting, error) {
	// 鉴权
	userid := viewer.MustFromContext(ctx).UserID()
	user, err := settingService.commonService.CommonGetUserByUserId(ctx, userid)
	if err != nil {
		return nil, err
	}
	if !user.IsAdmin {
		return nil, errors.New(commonModel.NO_PERMISSION_DENIED)
	}

	tokens, err := settingService.settingRepository.ListAccessTokens(ctx, user.ID)
	if err != nil {
		return []model.AccessTokenSetting{}, nil
	}

	// 处理tokens,过滤并删除过期的token
	var validTokens []model.AccessTokenSetting
	currentTime := time.Now().UTC().Unix()

	for _, token := range tokens {
		if token.Expiry == nil || *token.Expiry > currentTime {
			// nil 表示永不过期，或者还没过期
			validTokens = append(validTokens, token)
		} else {
			// 删除过期 token
			_ = settingService.transactor.Run(ctx, func(txCtx context.Context) error {
				return settingService.settingRepository.DeleteAccessTokenByID(txCtx, token.ID)
			})
		}
	}

	return validTokens, nil
}

// CreateAccessToken 创建访问令牌
func (settingService *SettingService) CreateAccessToken(
	ctx context.Context,
	newToken *model.AccessTokenSettingDto,
) (string, error) {
	// 鉴权
	userid := viewer.MustFromContext(ctx).UserID()
	user, err := settingService.commonService.CommonGetUserByUserId(ctx, userid)
	if err != nil {
		return "", err
	}
	if !user.IsAdmin {
		return "", errors.New(commonModel.NO_PERMISSION_DENIED)
	}
	if err := validateAccessTokenRequest(user, newToken); err != nil {
		return "", err
	}

	name := newToken.Name
	expiry := newToken.Expiry
	audience := newToken.Audience
	scopes := normalizeScopes(newToken.Scopes)
	scopeJSON, err := json.Marshal(scopes)
	if err != nil {
		return "", err
	}
	jti := uuidUtil.NewV7()
	var expiryDuration time.Duration

	switch expiry {
	case model.EIGHT_HOUR_EXPIRY:
		expiryDuration = 8 * time.Hour
	case model.ONE_MONTH_EXPIRY:
		expiryDuration = 30 * 24 * time.Hour
	case model.NEVER_EXPIRY:
		expiryDuration = 0
	default:
		expiryDuration = 8 * time.Hour
	}

	// 生成jwt令牌
	claims := jwtUtil.CreateAccessClaimsWithExpiry(user, int64(expiryDuration), scopes, audience, jti)
	tokenString, err := jwtUtil.GenerateToken(claims)
	if err != nil {
		return "", err
	}

	// 处理数据库存储的 expiry
	var expiryPtr *int64
	if expiry == model.NEVER_EXPIRY {
		expiryPtr = nil // 永不过期，用 NULL
	} else {
		t := time.Now().UTC().Add(expiryDuration).Unix()
		expiryPtr = &t
	}

	// 保存到数据库
	accessToken := &model.AccessTokenSetting{
		UserID:    user.ID,
		Token:     tokenString,
		Name:      name,
		TokenType: authModel.TokenTypeAccess,
		Scopes:    string(scopeJSON),
		Audience:  audience,
		JTI:       jti,
		Expiry:    expiryPtr,
	}

	if err := settingService.transactor.Run(ctx, func(txCtx context.Context) error {
		return settingService.settingRepository.CreateAccessToken(txCtx, accessToken)
	}); err != nil {
		return "", err
	}

	return tokenString, nil
}

func validateAccessTokenRequest(user userModel.User, dto *model.AccessTokenSettingDto) error {
	if dto == nil {
		return errors.New(commonModel.INVALID_PARAMS_BODY)
	}
	if strings.TrimSpace(dto.Name) == "" {
		return errors.New(commonModel.INVALID_PARAMS_BODY)
	}
	if !authModel.IsValidAudience(dto.Audience) {
		return errors.New(commonModel.INVALID_PARAMS_BODY)
	}
	if len(dto.Scopes) == 0 {
		return errors.New(commonModel.INVALID_PARAMS_BODY)
	}
	for _, scope := range dto.Scopes {
		if !authModel.IsValidScope(scope) {
			return errors.New(commonModel.INVALID_PARAMS_BODY)
		}
	}
	if authModel.HasAdminScope(dto.Scopes) && !user.IsAdmin {
		return errors.New(commonModel.NO_PERMISSION_DENIED)
	}
	return nil
}

func normalizeScopes(scopes []string) []string {
	seen := make(map[string]struct{}, len(scopes))
	result := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		result = append(result, scope)
	}
	return result
}

// DeleteAccessToken 删除访问令牌。
//
// 删除前先把 JTI 写入黑名单，让鉴权中间件立刻拒绝该 token；否则光删 DB 行
// 不会让已签发的 JWT 失效，泄漏 token 仍可继续访问到自然过期 (GHSA-fpw6-hrg5-q5x5)。
//
// 注意：黑名单当前是 in-memory ristretto，重启会丢；不过对于会自然过期的 token，
// 自然过期接管；对于 NEVER_EXPIRY (现已统一为 100 年) 这是已知限制，需要持久化或
// 中间件回查 DB 才能彻底关闭，超出本次修复范围。
func (settingService *SettingService) DeleteAccessToken(ctx context.Context, id string) error {
	// 鉴权
	userid := viewer.MustFromContext(ctx).UserID()
	user, err := settingService.commonService.CommonGetUserByUserId(ctx, userid)
	if err != nil {
		return err
	}
	if !user.IsAdmin {
		return errors.New(commonModel.NO_PERMISSION_DENIED)
	}

	// 取出 JTI 与剩余过期时间用于黑名单写入。读失败不阻断删除（兼容历史损坏行）。
	if settingService.tokenRevoker != nil {
		token, getErr := settingService.settingRepository.GetAccessTokenByID(ctx, id)
		if getErr == nil && token.JTI != "" {
			ttl := remainingTTLForRevoke(token.Expiry)
			settingService.tokenRevoker.RevokeToken(token.JTI, ttl)
		}
	}

	return settingService.transactor.Run(ctx, func(txCtx context.Context) error {
		return settingService.settingRepository.DeleteAccessTokenByID(txCtx, id)
	})
}

// remainingTTLForRevoke 计算 JTI 黑名单条目应该保留多久。
//   - Expiry == nil  : NEVER_EXPIRY，按 100 年存留（与 JWT 层 fallback 对齐）。
//   - 已过期         : 给个非零最小值，仍写一笔以防黑名单被外部短期回放。
//   - 未过期         : 直到 token 自然过期。
func remainingTTLForRevoke(expiryUnix *int64) time.Duration {
	const neverFallback = 100 * 365 * 24 * time.Hour
	if expiryUnix == nil {
		return neverFallback
	}
	remaining := time.Until(time.Unix(*expiryUnix, 0).UTC())
	if remaining <= 0 {
		return time.Hour
	}
	return remaining
}
