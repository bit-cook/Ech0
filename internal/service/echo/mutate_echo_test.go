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
	fileModel "github.com/lin-snow/ech0/internal/model/file"
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

// TestDeleteEchoById_Guards 覆盖删除前的快速失败：非管理员拒绝、用户解析错误。
func TestDeleteEchoById_Guards(t *testing.T) {
	t.Run("non-admin is denied", func(t *testing.T) {
		repo := echomock.NewMockRepository(t)
		common := commonmock.NewMockService(t)
		common.EXPECT().
			CommonGetUserByUserId(mock.Anything, userID).
			Return(helpers.NewUser(), nil).
			Once()

		svc := echoService.NewEchoService(nil, common, nil, repo, nilBus)
		require.EqualError(t, svc.DeleteEchoById(helpers.CtxAsUser(userID), echoID), commonModel.NO_PERMISSION_DENIED)
	})

	t.Run("user lookup error propagates", func(t *testing.T) {
		repo := echomock.NewMockRepository(t)
		common := commonmock.NewMockService(t)
		boom := errors.New("lookup failed")
		common.EXPECT().
			CommonGetUserByUserId(mock.Anything, adminID).
			Return(helpers.NewUser(helpers.AsAdmin), boom).
			Once()

		svc := echoService.NewEchoService(nil, common, nil, repo, nilBus)
		require.ErrorIs(t, svc.DeleteEchoById(helpers.CtxAsUser(adminID), echoID), boom)
	})
}

// TestDeleteEchoById_NotFound 确认事务内回查 echo 为 nil 时返回 ECHO_NOT_FOUND，
// 不触达缓存失效 / 事件。
func TestDeleteEchoById_NotFound(t *testing.T) {
	repo := echomock.NewMockRepository(t)
	common := commonmock.NewMockService(t)
	tx := txmock.NewMockTransactor(t)
	common.EXPECT().
		CommonGetUserByUserId(mock.Anything, adminID).
		Return(helpers.NewUser(helpers.AsAdmin), nil).
		Once()
	tx.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(runTx).Once()
	repo.EXPECT().GetEchosById(mock.Anything, echoID).Return(nil, nil).Once()

	svc := echoService.NewEchoService(tx, common, nil, repo, nilBus)
	require.EqualError(t, svc.DeleteEchoById(helpers.CtxAsUser(adminID), echoID), commonModel.ECHO_NOT_FOUND)
}

// TestDeleteEchoById_GetEchoError 确认事务内回查出错时上抛该错误。
func TestDeleteEchoById_GetEchoError(t *testing.T) {
	repo := echomock.NewMockRepository(t)
	common := commonmock.NewMockService(t)
	tx := txmock.NewMockTransactor(t)
	boom := errors.New("fetch failed")
	common.EXPECT().
		CommonGetUserByUserId(mock.Anything, adminID).
		Return(helpers.NewUser(helpers.AsAdmin), nil).
		Once()
	tx.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(runTx).Once()
	repo.EXPECT().GetEchosById(mock.Anything, echoID).Return(nil, boom).Once()

	svc := echoService.NewEchoService(tx, common, nil, repo, nilBus)
	require.ErrorIs(t, svc.DeleteEchoById(helpers.CtxAsUser(adminID), echoID), boom)
}

// TestDeleteEchoById_Success 覆盖完整删除路径：
//   - 本地文件：登记进待删存储集合 + 删除文件记录；
//   - 外部文件（external）：跳过存储删除（仅删记录）；
//   - 仅删本地的物理对象，失败被吞（不影响返回）；
//   - 缓存失效 + 发出 EchoDeleted。
func TestDeleteEchoById_Success(t *testing.T) {
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

	stored := helpers.NewEcho(func(e *echoModel.Echo) {
		e.ID = echoID
		e.EchoFiles = []fileModel.EchoFile{
			{File: fileModel.File{ID: "f-local", Key: "k-local", StorageType: "local"}},
			{File: fileModel.File{ID: "f-ext", Key: "k-ext", StorageType: "external"}},
		}
	})
	repo.EXPECT().GetEchosById(mock.Anything, echoID).Return(&stored, nil).Once()
	// 两个文件都有记录 ID -> 都删记录。
	file.EXPECT().DeleteFileRecord(mock.Anything, "f-local").Return(nil).Once()
	file.EXPECT().DeleteFileRecord(mock.Anything, "f-ext").Return(nil).Once()
	repo.EXPECT().DeleteEchoById(mock.Anything, echoID).Return(nil).Once()
	repo.EXPECT().InvalidateEchoCaches(echoID).Once()
	// 仅本地对象进入物理删除（external 被排除）；删除失败被忽略。
	file.EXPECT().DeleteStoredFile("local", "k-local").Return(errors.New("ignored")).Once()

	var got event.EchoDeleted
	var fired int
	unsub, err := bus.Subscribe(func(_ context.Context, e busen.Event[event.EchoDeleted]) error {
		got = e.Value
		fired++
		return nil
	})
	require.NoError(t, err)
	defer unsub()

	svc := echoService.NewEchoService(tx, common, file, repo, func() *busen.Bus { return bus })
	require.NoError(t, svc.DeleteEchoById(helpers.CtxAsUser(adminID), echoID))

	require.Equal(t, 1, fired)
	assert.Equal(t, echoID, got.Echo.ID)
	assert.True(t, got.User.IsAdmin)
}

// TestDeleteEchoById_DeleteFileRecordError 确认事务内删除文件记录失败时整体回滚并上抛，
// 不触达缓存失效 / 物理删除 / 事件。
func TestDeleteEchoById_DeleteFileRecordError(t *testing.T) {
	repo := echomock.NewMockRepository(t)
	common := commonmock.NewMockService(t)
	file := filemock.NewMockService(t)
	tx := txmock.NewMockTransactor(t)
	boom := errors.New("delete record failed")

	common.EXPECT().
		CommonGetUserByUserId(mock.Anything, adminID).
		Return(helpers.NewUser(helpers.AsAdmin), nil).
		Once()
	tx.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(runTx).Once()
	stored := helpers.NewEcho(func(e *echoModel.Echo) {
		e.ID = echoID
		e.EchoFiles = []fileModel.EchoFile{{File: fileModel.File{ID: "f-1", Key: "k-1", StorageType: "local"}}}
	})
	repo.EXPECT().GetEchosById(mock.Anything, echoID).Return(&stored, nil).Once()
	file.EXPECT().DeleteFileRecord(mock.Anything, "f-1").Return(boom).Once()

	svc := echoService.NewEchoService(tx, common, file, repo, nilBus)
	require.ErrorIs(t, svc.DeleteEchoById(helpers.CtxAsUser(adminID), echoID), boom)
}

// TestUpdateEcho_Guards 覆盖更新前的快速失败分支。
func TestUpdateEcho_Guards(t *testing.T) {
	t.Run("non-admin is denied", func(t *testing.T) {
		repo := echomock.NewMockRepository(t)
		common := commonmock.NewMockService(t)
		common.EXPECT().
			CommonGetUserByUserId(mock.Anything, userID).
			Return(helpers.NewUser(), nil).
			Once()

		svc := echoService.NewEchoService(nil, common, nil, repo, nilBus)
		require.EqualError(t,
			svc.UpdateEcho(helpers.CtxAsUser(userID), &echoModel.Echo{ID: echoID, Content: "hi"}),
			commonModel.NO_PERMISSION_DENIED,
		)
	})

	t.Run("invalid extension rejected before transaction", func(t *testing.T) {
		repo := echomock.NewMockRepository(t)
		common := commonmock.NewMockService(t)
		common.EXPECT().
			CommonGetUserByUserId(mock.Anything, adminID).
			Return(helpers.NewUser(helpers.AsAdmin), nil).
			Once()

		svc := echoService.NewEchoService(nil, common, nil, repo, nilBus)
		err := svc.UpdateEcho(helpers.CtxAsUser(adminID), &echoModel.Echo{
			ID:        echoID,
			Content:   "hi",
			Extension: &echoModel.EchoExtension{Type: echoModel.Extension_MUSIC}, // Payload nil -> error
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "extension payload")
	})

	t.Run("empty echo rejected", func(t *testing.T) {
		repo := echomock.NewMockRepository(t)
		common := commonmock.NewMockService(t)
		common.EXPECT().
			CommonGetUserByUserId(mock.Anything, adminID).
			Return(helpers.NewUser(helpers.AsAdmin), nil).
			Once()

		svc := echoService.NewEchoService(nil, common, nil, repo, nilBus)
		require.EqualError(t,
			svc.UpdateEcho(helpers.CtxAsUser(adminID), &echoModel.Echo{ID: echoID, Content: "   "}),
			commonModel.ECHO_CAN_NOT_BE_EMPTY,
		)
	})
}

// TestUpdateEcho_Success 覆盖完整更新路径：非法布局归一化为 waterfall、回填 EchoFiles.EchoID、
// 事务内处理标签并更新、缓存失效、发出 EchoUpdated、确认临时文件。
func TestUpdateEcho_Success(t *testing.T) {
	repo := echomock.NewMockRepository(t)
	common := commonmock.NewMockService(t)
	file := filemock.NewMockService(t)
	tx := txmock.NewMockTransactor(t)
	bus := helpers.NewTestBus(t)

	common.EXPECT().
		CommonGetUserByUserId(mock.Anything, adminID).
		Return(helpers.NewUser(helpers.AsAdmin), nil).
		Once()
	// 单文件 echo 通过单类别校验（一次批量读类别）。
	file.EXPECT().
		GetFilesByIDs(mock.Anything, []string{"file-1"}).
		Return([]commonModel.FileDto{{ID: "file-1", Category: "image"}}, nil).
		Once()
	tx.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(runTx).Once()
	repo.EXPECT().GetTagsByNames(mock.Anything, mock.Anything).Return([]*echoModel.Tag{}, nil).Once()

	var updated echoModel.Echo
	repo.EXPECT().
		UpdateEcho(mock.Anything, mock.Anything).
		Run(func(_ context.Context, e *echoModel.Echo) { updated = *e }).
		Return(nil).
		Once()
	repo.EXPECT().InvalidateEchoCaches(echoID).Once()
	file.EXPECT().ConfirmTempFiles(mock.Anything, []string{"file-1"}).Return(nil).Once()

	var got event.EchoUpdated
	var fired int
	unsub, err := bus.Subscribe(func(_ context.Context, e busen.Event[event.EchoUpdated]) error {
		got = e.Value
		fired++
		return nil
	})
	require.NoError(t, err)
	defer unsub()

	in := &echoModel.Echo{
		ID:        echoID,
		Content:   "updated",
		Layout:    "bogus-layout",
		EchoFiles: []fileModel.EchoFile{{FileID: "file-1"}},
	}
	svc := echoService.NewEchoService(tx, common, file, repo, func() *busen.Bus { return bus })
	require.NoError(t, svc.UpdateEcho(helpers.CtxAsUser(adminID), in))

	assert.Equal(t, echoModel.LayoutWaterfall, updated.Layout)
	require.Len(t, updated.EchoFiles, 1)
	assert.Equal(t, echoID, updated.EchoFiles[0].EchoID) // EchoID 被回填
	require.Equal(t, 1, fired)
	assert.Equal(t, echoID, got.Echo.ID)
}

// TestUpdateEcho_TransactionError 确认事务失败时上抛错误且不触达缓存失效 / 事件。
func TestUpdateEcho_TransactionError(t *testing.T) {
	repo := echomock.NewMockRepository(t)
	common := commonmock.NewMockService(t)
	tx := txmock.NewMockTransactor(t)
	boom := errors.New("update failed")

	common.EXPECT().
		CommonGetUserByUserId(mock.Anything, adminID).
		Return(helpers.NewUser(helpers.AsAdmin), nil).
		Once()
	tx.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(runTx).Once()
	repo.EXPECT().GetTagsByNames(mock.Anything, mock.Anything).Return([]*echoModel.Tag{}, nil).Once()
	repo.EXPECT().UpdateEcho(mock.Anything, mock.Anything).Return(boom).Once()

	svc := echoService.NewEchoService(tx, common, nil, repo, nilBus)
	require.ErrorIs(t,
		svc.UpdateEcho(helpers.CtxAsUser(adminID), &echoModel.Echo{ID: echoID, Content: "x", Layout: echoModel.LayoutGrid}),
		boom,
	)
}
