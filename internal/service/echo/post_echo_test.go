// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lin-snow/ech0/internal/event"
	commonModel "github.com/lin-snow/ech0/internal/model/common"
	echoModel "github.com/lin-snow/ech0/internal/model/echo"
	echoService "github.com/lin-snow/ech0/internal/service/echo"
	"github.com/lin-snow/ech0/internal/test/helpers"
	commonmock "github.com/lin-snow/ech0/internal/test/mocks/commonmock"
	echomock "github.com/lin-snow/ech0/internal/test/mocks/echomock"
	filemock "github.com/lin-snow/ech0/internal/test/mocks/filemock"
	txmock "github.com/lin-snow/ech0/internal/test/mocks/txmock"
	"github.com/lin-snow/ech0/pkg/busen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestPostEcho_Guards 覆盖 PostEcho 在进入事务前的快速失败分支：
// 管理员守卫、用户查询错误、扩展归一化失败、空内容拒绝——均不应触达 transactor / repo 写入。
func TestPostEcho_Guards(t *testing.T) {
	t.Run("non-admin is denied", func(t *testing.T) {
		repo := echomock.NewMockRepository(t)
		common := commonmock.NewMockService(t)
		common.EXPECT().
			CommonGetUserByUserId(mock.Anything, userID).
			Return(helpers.NewUser(), nil).
			Once()

		svc := echoService.NewEchoService(nil, common, nil, repo, nilBus)
		err := svc.PostEcho(helpers.CtxAsUser(userID), &echoModel.Echo{Content: "hi"})

		require.EqualError(t, err, commonModel.NO_PERMISSION_DENIED)
	})

	t.Run("user lookup error propagates", func(t *testing.T) {
		repo := echomock.NewMockRepository(t)
		common := commonmock.NewMockService(t)
		boom := errors.New("user lookup failed")
		common.EXPECT().
			CommonGetUserByUserId(mock.Anything, adminID).
			Return(helpers.NewUser(helpers.AsAdmin), boom).
			Once()

		svc := echoService.NewEchoService(nil, common, nil, repo, nilBus)
		err := svc.PostEcho(helpers.CtxAsUser(adminID), &echoModel.Echo{Content: "hi"})

		require.ErrorIs(t, err, boom)
	})

	t.Run("invalid extension is rejected before transaction", func(t *testing.T) {
		repo := echomock.NewMockRepository(t)
		common := commonmock.NewMockService(t)
		common.EXPECT().
			CommonGetUserByUserId(mock.Anything, adminID).
			Return(helpers.NewUser(helpers.AsAdmin), nil).
			Once()

		svc := echoService.NewEchoService(nil, common, nil, repo, nilBus)
		// Type 非空但 Payload 为 nil -> normalizeEchoExtension 报错。
		err := svc.PostEcho(helpers.CtxAsUser(adminID), &echoModel.Echo{
			Content:   "hi",
			Extension: &echoModel.EchoExtension{Type: echoModel.Extension_MUSIC},
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "extension payload")
	})

	t.Run("empty echo is rejected", func(t *testing.T) {
		repo := echomock.NewMockRepository(t)
		common := commonmock.NewMockService(t)
		common.EXPECT().
			CommonGetUserByUserId(mock.Anything, adminID).
			Return(helpers.NewUser(helpers.AsAdmin), nil).
			Once()

		svc := echoService.NewEchoService(nil, common, nil, repo, nilBus)
		err := svc.PostEcho(helpers.CtxAsUser(adminID), &echoModel.Echo{Content: "   "})

		require.EqualError(t, err, commonModel.ECHO_CAN_NOT_BE_EMPTY)
	})
}

// TestPostEcho_Success 覆盖完整成功路径：布局归一化、用户名回填、事务内建 echo、
// 缓存失效、回查 savedEcho 并通过 live bus 发出 EchoCreated 事件、确认临时文件。
func TestPostEcho_Success(t *testing.T) {
	repo := echomock.NewMockRepository(t)
	common := commonmock.NewMockService(t)
	file := filemock.NewMockService(t)
	tx := txmock.NewMockTransactor(t)
	bus := helpers.NewTestBus(t)

	admin := helpers.NewUser(helpers.AsAdmin)
	admin.Username = "adminuser"
	common.EXPECT().
		CommonGetUserByUserId(mock.Anything, adminID).
		Return(admin, nil).
		Once()

	tx.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(runTx).Once()
	// ProcessEchoTags：无标签，names 为空，仅查询一次。
	repo.EXPECT().GetTagsByNames(mock.Anything, mock.Anything).Return([]*echoModel.Tag{}, nil).Once()

	var created echoModel.Echo
	repo.EXPECT().
		CreateEcho(mock.Anything, mock.Anything).
		Run(func(_ context.Context, e *echoModel.Echo) { created = *e }).
		Return(nil).
		Once()
	repo.EXPECT().InvalidateEchoCaches().Once()

	saved := helpers.NewEcho(func(e *echoModel.Echo) { e.ID = "saved-1"; e.Content = "hello" })
	repo.EXPECT().GetEchosById(mock.Anything, mock.Anything).Return(&saved, nil).Once()

	file.EXPECT().ConfirmTempFiles(mock.Anything, mock.Anything).Return(nil).Once()

	// 同步订阅 EchoCreated：busen 默认同步投递，Publish 返回前 handler 已执行，无需 sleep。
	var gotEvent event.EchoCreated
	var gotCount int
	unsub, err := bus.Subscribe(func(_ context.Context, e busen.Event[event.EchoCreated]) error {
		gotEvent = e.Value
		gotCount++
		return nil
	})
	require.NoError(t, err)
	defer unsub()

	svc := echoService.NewEchoService(tx, common, file, repo, func() *busen.Bus { return bus })
	in := &echoModel.Echo{Content: "hello", Layout: "weird-layout"}
	require.NoError(t, svc.PostEcho(helpers.CtxAsUser(adminID), in))

	// 非法布局归一化为 waterfall；用户名与作者 ID 回填。
	assert.Equal(t, echoModel.LayoutWaterfall, created.Layout)
	assert.Equal(t, "adminuser", created.Username)
	assert.Equal(t, adminID, created.UserID)

	// 事件以回查到的 savedEcho 与当前 user 发出。
	require.Equal(t, 1, gotCount)
	assert.Equal(t, "saved-1", gotEvent.Echo.ID)
	assert.True(t, gotEvent.User.IsAdmin)
}

// TestPostEcho_ValidLayoutPreserved 确认合法布局不被覆盖。
func TestPostEcho_ValidLayoutPreserved(t *testing.T) {
	repo := echomock.NewMockRepository(t)
	common := commonmock.NewMockService(t)
	file := filemock.NewMockService(t)
	tx := txmock.NewMockTransactor(t)
	bus := helpers.NewTestBus(t)

	common.EXPECT().
		CommonGetUserByUserId(mock.Anything, adminID).
		Return(helpers.NewUser(helpers.AsAdmin), nil).
		Once()
	tx.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(runTx).Once()
	repo.EXPECT().GetTagsByNames(mock.Anything, mock.Anything).Return([]*echoModel.Tag{}, nil).Once()

	var created echoModel.Echo
	repo.EXPECT().
		CreateEcho(mock.Anything, mock.Anything).
		Run(func(_ context.Context, e *echoModel.Echo) { created = *e }).
		Return(nil).
		Once()
	repo.EXPECT().InvalidateEchoCaches().Once()
	saved := helpers.NewEcho(func(e *echoModel.Echo) { e.ID = "saved-2" })
	repo.EXPECT().GetEchosById(mock.Anything, mock.Anything).Return(&saved, nil).Once()
	file.EXPECT().ConfirmTempFiles(mock.Anything, mock.Anything).Return(nil).Once()

	svc := echoService.NewEchoService(tx, common, file, repo, func() *busen.Bus { return bus })
	require.NoError(t, svc.PostEcho(helpers.CtxAsUser(adminID), &echoModel.Echo{
		Content: "hello",
		Layout:  echoModel.LayoutGrid,
	}))

	assert.Equal(t, echoModel.LayoutGrid, created.Layout)
}

// TestPostEcho_TransactionError 确认事务失败时直接返回错误，且不触达缓存失效 / 回查 / 事件。
func TestPostEcho_TransactionError(t *testing.T) {
	repo := echomock.NewMockRepository(t)
	common := commonmock.NewMockService(t)
	tx := txmock.NewMockTransactor(t)
	boom := errors.New("create echo failed")

	common.EXPECT().
		CommonGetUserByUserId(mock.Anything, adminID).
		Return(helpers.NewUser(helpers.AsAdmin), nil).
		Once()
	tx.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(runTx).Once()
	repo.EXPECT().GetTagsByNames(mock.Anything, mock.Anything).Return([]*echoModel.Tag{}, nil).Once()
	repo.EXPECT().CreateEcho(mock.Anything, mock.Anything).Return(boom).Once()

	svc := echoService.NewEchoService(tx, common, nil, repo, nilBus)
	err := svc.PostEcho(helpers.CtxAsUser(adminID), &echoModel.Echo{Content: "hello"})

	require.ErrorIs(t, err, boom)
}

// TestPostEcho_SavedEchoNilSkipsEvent 确认回查返回 nil 时不发事件，但仍确认临时文件并成功返回。
func TestPostEcho_SavedEchoNilSkipsEvent(t *testing.T) {
	repo := echomock.NewMockRepository(t)
	common := commonmock.NewMockService(t)
	file := filemock.NewMockService(t)
	tx := txmock.NewMockTransactor(t)
	bus := helpers.NewTestBus(t)

	common.EXPECT().
		CommonGetUserByUserId(mock.Anything, adminID).
		Return(helpers.NewUser(helpers.AsAdmin), nil).
		Once()
	tx.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(runTx).Once()
	repo.EXPECT().GetTagsByNames(mock.Anything, mock.Anything).Return([]*echoModel.Tag{}, nil).Once()
	repo.EXPECT().CreateEcho(mock.Anything, mock.Anything).Return(nil).Once()
	repo.EXPECT().InvalidateEchoCaches().Once()
	repo.EXPECT().GetEchosById(mock.Anything, mock.Anything).Return(nil, nil).Once()
	file.EXPECT().ConfirmTempFiles(mock.Anything, mock.Anything).Return(nil).Once()

	var fired int
	unsub, err := bus.Subscribe(func(_ context.Context, _ busen.Event[event.EchoCreated]) error {
		fired++
		return nil
	})
	require.NoError(t, err)
	defer unsub()

	svc := echoService.NewEchoService(tx, common, file, repo, func() *busen.Bus { return bus })
	require.NoError(t, svc.PostEcho(helpers.CtxAsUser(adminID), &echoModel.Echo{Content: "hello"}))

	assert.Equal(t, 0, fired)
}

// TestPostEcho_RefetchError 确认事务成功后回查出错时返回该错误。
func TestPostEcho_RefetchError(t *testing.T) {
	repo := echomock.NewMockRepository(t)
	common := commonmock.NewMockService(t)
	tx := txmock.NewMockTransactor(t)
	bus := helpers.NewTestBus(t)
	boom := errors.New("refetch failed")

	common.EXPECT().
		CommonGetUserByUserId(mock.Anything, adminID).
		Return(helpers.NewUser(helpers.AsAdmin), nil).
		Once()
	tx.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(runTx).Once()
	repo.EXPECT().GetTagsByNames(mock.Anything, mock.Anything).Return([]*echoModel.Tag{}, nil).Once()
	repo.EXPECT().CreateEcho(mock.Anything, mock.Anything).Return(nil).Once()
	repo.EXPECT().InvalidateEchoCaches().Once()
	repo.EXPECT().GetEchosById(mock.Anything, mock.Anything).Return(nil, boom).Once()

	svc := echoService.NewEchoService(tx, common, nil, repo, func() *busen.Bus { return bus })
	err := svc.PostEcho(helpers.CtxAsUser(adminID), &echoModel.Echo{Content: "hello"})

	require.ErrorIs(t, err, boom)
}

// TestPostEcho_LayoutNonePreserved 确认 "none" 布局（音频/视频 Echo 使用）不被归一化成 waterfall。
func TestPostEcho_LayoutNonePreserved(t *testing.T) {
	repo := echomock.NewMockRepository(t)
	common := commonmock.NewMockService(t)
	file := filemock.NewMockService(t)
	tx := txmock.NewMockTransactor(t)
	bus := helpers.NewTestBus(t)

	common.EXPECT().
		CommonGetUserByUserId(mock.Anything, adminID).
		Return(helpers.NewUser(helpers.AsAdmin), nil).
		Once()
	tx.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(runTx).Once()
	repo.EXPECT().GetTagsByNames(mock.Anything, mock.Anything).Return([]*echoModel.Tag{}, nil).Once()

	var created echoModel.Echo
	repo.EXPECT().
		CreateEcho(mock.Anything, mock.Anything).
		Run(func(_ context.Context, e *echoModel.Echo) { created = *e }).
		Return(nil).
		Once()
	repo.EXPECT().InvalidateEchoCaches().Once()
	saved := helpers.NewEcho(func(e *echoModel.Echo) { e.ID = "saved-none" })
	repo.EXPECT().GetEchosById(mock.Anything, mock.Anything).Return(&saved, nil).Once()
	file.EXPECT().ConfirmTempFiles(mock.Anything, mock.Anything).Return(nil).Once()

	svc := echoService.NewEchoService(tx, common, file, repo, func() *busen.Bus { return bus })
	require.NoError(t, svc.PostEcho(helpers.CtxAsUser(adminID), &echoModel.Echo{
		Content: "audio post",
		Layout:  echoModel.LayoutNone,
	}))

	assert.Equal(t, echoModel.LayoutNone, created.Layout)
}
