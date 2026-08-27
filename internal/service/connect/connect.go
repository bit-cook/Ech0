// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lin-snow/ech0/internal/kvstore"
	commonModel "github.com/lin-snow/ech0/internal/model/common"
	model "github.com/lin-snow/ech0/internal/model/connect"
	coreSetting "github.com/lin-snow/ech0/internal/setting"
	"github.com/lin-snow/ech0/internal/transaction"
	"github.com/lin-snow/ech0/internal/util/egress"
	urlUtil "github.com/lin-snow/ech0/internal/util/url"
	versionPkg "github.com/lin-snow/ech0/internal/version"
	logUtil "github.com/lin-snow/ech0/pkg/log"
	"github.com/lin-snow/ech0/pkg/viewer"
	"golang.org/x/sync/singleflight"
)

const (
	connectsInfoCacheTTL        = 30 * time.Minute
	connectFanoutMaxConcurrency = 8
	connectsInfoSingleflightKey = "connects_info"
	siteMetricsTimezone         = "UTC"
	healthProbeTimeout          = 5 * time.Second
	healthOverallTimeout        = 30 * time.Second
	// connectRetryBaseDelay 是 fetchConnectsInfo 拉取失败后指数退避的基准延迟（1s, 2s, ...）。
	// 抽成可注入字段（见 retryBaseDelay / WithRetryBaseDelay）后，测试可设为 0 让重试路径瞬时跑完。
	connectRetryBaseDelay = 1 * time.Second
)

type ConnectService struct {
	transactor        transaction.Transactor
	connectRepository Repository
	echoRepository    EchoRepository
	commonService     CommonService
	durableKV         kvstore.Store

	connectsInfoCacheMu      sync.RWMutex
	connectsInfoCache        []model.Connect
	connectsInfoCacheExpires time.Time
	connectsInfoCacheValid   bool
	connectsInfoFetcher      singleflight.Group

	// peerFetcher 拉取单个对端实例的连接信息；默认 fetchPeerConnectInfo（egress.Fetch 带
	// SSRF Guard，会拒绝回环/私网地址）。因 Guard 会拦掉 httptest 的 127.0.0.1，测试无法用真
	// HTTP 覆盖编排逻辑，故抽成可注入函数：测试注入返回 canned Connect 的替身，即可覆盖
	// fetchConnectsInfo 的并发扇出/重试/去重与健康聚合，而不触发真实网络。
	peerFetcher func(peerConnectURL string, requestTimeout time.Duration) (model.Connect, error)

	// retryBaseDelay 是 fetchConnectsInfo 重试退避的基准延迟；默认 connectRetryBaseDelay(1s)。
	// 测试用 WithRetryBaseDelay(0) 设为 0，使失败/重试路径不再 sleep 真实墙钟时间。
	retryBaseDelay time.Duration
}

func NewConnectService(
	tx transaction.Transactor,
	connectRepository Repository,
	echoRepository EchoRepository,
	commonService CommonService,
	durableKV kvstore.Store,
) *ConnectService {
	return &ConnectService{
		transactor:        tx,
		connectRepository: connectRepository,
		echoRepository:    echoRepository,
		commonService:     commonService,
		durableKV:         durableKV,
		peerFetcher:       fetchPeerConnectInfo,
		retryBaseDelay:    connectRetryBaseDelay,
	}
}

// WithPeerFetcher 替换对端拉取实现（默认 fetchPeerConnectInfo）并返回自身，主要供测试注入替身：
//
//	svc := service.NewConnectService(...).WithPeerFetcher(fakeFetch)
func (connectService *ConnectService) WithPeerFetcher(
	f func(peerConnectURL string, requestTimeout time.Duration) (model.Connect, error),
) *ConnectService {
	connectService.peerFetcher = f
	return connectService
}

// WithRetryBaseDelay 覆盖重试退避基准延迟并返回自身，主要供测试设为 0 消除墙钟等待：
//
//	svc := service.NewConnectService(...).WithRetryBaseDelay(0)
func (connectService *ConnectService) WithRetryBaseDelay(d time.Duration) *ConnectService {
	connectService.retryBaseDelay = d
	return connectService
}

// AddConnect 添加连接
func (connectService *ConnectService) AddConnect(ctx context.Context, connected model.Connected) error {
	userid := viewer.MustFromContext(ctx).UserID()
	if err := connectService.transactor.Run(ctx, func(txCtx context.Context) error {
		user, err := connectService.commonService.CommonGetUserByUserId(txCtx, userid)
		if err != nil {
			return err
		}

		if !user.IsAdmin {
			return errors.New(commonModel.NO_PERMISSION_DENIED)
		}

		// 检查连接地址是否为空
		if connected.ConnectURL == "" {
			return errors.New(commonModel.INVALID_CONNECTION_URL)
		}

		// 去除连接地址前后的空格和斜杠
		connected.ConnectURL = urlUtil.TrimURL(connected.ConnectURL)

		// SSRF 防护：拒绝指向私网/回环/云元数据等地址的对端 URL。
		// 运行时 fetchPeerConnectInfo 也会再次校验，这里在入库前就拦截，避免恶意记录污染存储。
		if err := egress.Validate(connected.ConnectURL + "/api/connect"); err != nil {
			return errors.New(commonModel.INVALID_CONNECTION_URL)
		}

		// 检查连接地址是否已存在
		connectedList, err := connectService.connectRepository.GetAllConnects(txCtx)
		if err != nil {
			return err
		}

		// 检查连接地址是否已存在
		for _, conn := range connectedList {
			if conn.ConnectURL == connected.ConnectURL {
				return errors.New(commonModel.CONNECT_HAS_EXISTS)
			}
		}

		// 添加连接地址
		if err := connectService.connectRepository.CreateConnect(txCtx, &connected); err != nil {
			return err
		}

		return nil
	}); err != nil {
		return err
	}

	connectService.invalidateConnectsInfoCache()
	return nil
}

// DeleteConnect 删除连接
func (connectService *ConnectService) DeleteConnect(ctx context.Context, id string) error {
	userid := viewer.MustFromContext(ctx).UserID()
	if err := connectService.transactor.Run(ctx, func(txCtx context.Context) error {
		user, err := connectService.commonService.CommonGetUserByUserId(txCtx, userid)
		if err != nil {
			return err
		}

		if !user.IsAdmin {
			return errors.New(commonModel.NO_PERMISSION_DENIED)
		}

		// 删除连接地址
		if err := connectService.connectRepository.DeleteConnect(txCtx, id); err != nil {
			return err
		}

		return nil
	}); err != nil {
		return err
	}

	connectService.invalidateConnectsInfoCache()
	return nil
}

// GetConnect 提供当前实例的连接信息
func (connectService *ConnectService) GetConnect() (model.Connect, error) {
	var connect model.Connect

	// 获取系统设置
	setting, err := coreSetting.Get(context.Background(), connectService.durableKV, coreSetting.System)
	if err != nil {
		return connect, err
	}

	// 获取 owner 信息
	owner, err := connectService.commonService.GetOwner()
	if err != nil {
		return connect, err
	}

	// 站点级统计统一按 UTC 日界，避免依赖部署环境 TZ。
	todayEchos := connectService.echoRepository.GetTodayEchos(true, siteMetricsTimezone)
	// 统计总发布数量
	_, totalEchos := connectService.echoRepository.GetEchosByPage(1, 1, "", true)

	// 设置 Connect 信息
	connect.ServerName = setting.ServerName
	connect.ServerURL = setting.ServerURL
	connect.TotalEchos = int(totalEchos)
	connect.TodayEchos = len(todayEchos)
	connect.SysUsername = owner.Username
	connect.Version = versionPkg.Version

	trimmedServerURL := strings.TrimRight(setting.ServerURL, "/")
	logoPath := strings.TrimSpace(setting.ServerLogo)

	if logoPath == "" || logoPath == "Ech0.svg" || logoPath == "/Ech0.svg" {
		connect.Logo = fmt.Sprintf("%s/Ech0.svg", trimmedServerURL)
	} else if strings.HasPrefix(logoPath, "http://") || strings.HasPrefix(logoPath, "https://") {
		connect.Logo = logoPath
	} else if strings.HasPrefix(logoPath, "/") {
		connect.Logo = fmt.Sprintf("%s%s", trimmedServerURL, logoPath)
	} else {
		connect.Logo = fmt.Sprintf("%s/%s", trimmedServerURL, logoPath)
	}

	return connect, nil
}

// GetConnectsInfo 获取实例获取到的其它实例的连接信息
func (connectService *ConnectService) GetConnectsInfo() ([]model.Connect, error) {
	if cached, ok := connectService.getCachedConnectsInfo(); ok {
		return cached, nil
	}

	result, err, _ := connectService.connectsInfoFetcher.Do(connectsInfoSingleflightKey, func() (any, error) {
		// double-check，避免在等待 singleflight 期间其它请求已回填缓存
		if cached, ok := connectService.getCachedConnectsInfo(); ok {
			return cached, nil
		}

		connectList, fetchErr := connectService.fetchConnectsInfo()
		if fetchErr != nil {
			return nil, fetchErr
		}

		connectService.setCachedConnectsInfo(connectList)
		return cloneConnects(connectList), nil
	})
	if err != nil {
		return nil, err
	}

	connects, ok := result.([]model.Connect)
	if !ok {
		return nil, fmt.Errorf("invalid cache result type")
	}

	return cloneConnects(connects), nil
}

// fetchPeerConnectInfo 请求对端 GET /api/connect，成功时返回解析后的 Connect（与 GetConnectsInfo 探测逻辑一致）。
// 使用 egress.Fetch（带 Guard）进行 SSRF 防护：拒绝指向私网/回环/云元数据等地址的对端 URL，
// 并通过安全拨号器防御 DNS rebinding。
func fetchPeerConnectInfo(peerConnectURL string, requestTimeout time.Duration) (model.Connect, error) {
	url := urlUtil.TrimURL(peerConnectURL) + "/api/connect"
	resp, err := egress.Fetch(url, "GET", egress.Header{
		Header:  "Ech0_URL",
		Content: peerConnectURL,
	}, requestTimeout)
	if err != nil {
		return model.Connect{}, err
	}

	var connectInfo commonModel.Result[model.Connect]
	if err := json.Unmarshal(resp, &connectInfo); err != nil {
		return model.Connect{}, fmt.Errorf("JSON解析失败: %w", err)
	}
	if connectInfo.Code != 1 {
		return model.Connect{}, fmt.Errorf("响应码无效: %d, 消息: %s", connectInfo.Code, connectInfo.Message)
	}
	if connectInfo.Data.ServerURL == "" {
		return model.Connect{}, fmt.Errorf("服务器URL为空")
	}
	return connectInfo.Data, nil
}

func (connectService *ConnectService) fetchConnectsInfo() ([]model.Connect, error) {
	// 总超时时间：给予足够的缓冲，避免单个慢连接导致整体超时
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// 获取所有连接地址
	connects, err := connectService.connectRepository.GetAllConnects(context.Background())
	if err != nil {
		return nil, err
	}

	if len(connects) == 0 {
		return []model.Connect{}, nil
	}

	var connectList []model.Connect
	connectList = make([]model.Connect, 0, len(connects))

	var wg sync.WaitGroup
	connectChan := make(chan model.Connect, len(connects))
	semaphore := make(chan struct{}, connectFanoutMaxConcurrency)

	seenURLs := make(map[string]struct{})
	var seenMutex sync.Mutex

	// 重试配置：平衡速度和可靠性
	const maxRetries = 3
	baseDelay := connectService.retryBaseDelay // 可注入（默认 1s；测试可设为 0）
	const requestTimeout = 3 * time.Second     // 单个请求超时时间（降低到3秒，加快失败检测）

	for _, conn := range connects {
		wg.Add(1)
		go func(conn model.Connected) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-semaphore }()

			var lastErr error
			for attempt := range maxRetries {
				select {
				case <-ctx.Done():
					logUtil.GetLogger().
						Info(
							"fetch connection info cancelled",
							slog.String("module", "connect"),
							slog.String("connect_url", conn.ConnectURL),
							logUtil.Err(ctx.Err()),
						)
					return // 总体超时直接退出
				default:
				}

				// 计算当前重试的延迟时间（指数退避）；delay<=0 时跳过等待（测试注入 0）。
				if attempt > 0 {
					delay := baseDelay * time.Duration(1<<(attempt-1)) // 1s, 2s, 4s...
					if delay > 0 {
						select {
						case <-time.After(delay):
						case <-ctx.Done():
							return
						}
					}
				}

				data, err := connectService.peerFetcher(conn.ConnectURL, requestTimeout)
				if err != nil {
					lastErr = err
					logUtil.GetLogger().Error("fetch connection info failed",
						slog.String("module", "connect"),
						slog.String("connect_url", conn.ConnectURL),
						slog.Int("attempt", attempt+1),
						logUtil.Err(lastErr),
					)
					if attempt == maxRetries-1 {
						logUtil.GetLogger().Error("fetch connection info exhausted retries",
							slog.String("module", "connect"),
							slog.String("connect_url", conn.ConnectURL),
							slog.Int("retries", maxRetries),
							logUtil.Err(lastErr),
						)
					}
					continue
				}

				// 成功获取有效数据，检查重复并发送
				seenMutex.Lock()
				if _, exists := seenURLs[data.ServerURL]; exists {
					seenMutex.Unlock()
					logUtil.GetLogger().Info("connection info duplicated",
						slog.String("module", "connect"),
						slog.String("connect_url", conn.ConnectURL),
						slog.String("server_url", data.ServerURL),
					)
					return // 重复数据，直接返回
				}
				seenURLs[data.ServerURL] = struct{}{}
				seenMutex.Unlock()

				logUtil.GetLogger().Info("fetch connection info succeeded",
					slog.String("module", "connect"),
					slog.String("connect_url", conn.ConnectURL),
					slog.String("server_name", data.ServerName),
				)
				connectChan <- data
				return // 成功处理，退出重试循环
			}
		}(conn)
	}

	// 使用带缓冲的通道来避免goroutine泄漏
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(connectChan)
		close(done)
	}()

	// 在单独的 goroutine 中收集结果，使用 mutex 保护并发写入
	var mu sync.Mutex
	collectDone := make(chan struct{})
	go func() {
		for connect := range connectChan {
			if connect.ServerURL != "" {
				mu.Lock()
				connectList = append(connectList, connect)
				mu.Unlock()
			}
		}
		close(collectDone)
	}()

	// 等待完成或超时
	select {
	case <-done:
		// 所有 goroutine 完成，等待收集完成
		<-collectDone
		mu.Lock()
		count := len(connectList)
		mu.Unlock()
		logUtil.GetLogger().Info("collect connection info completed", slog.String("module", "connect"), slog.Int("valid_count", count))
	case <-ctx.Done():
		// 超时，等待收集器完成或超时
		logUtil.GetLogger().Info("collect connection info timeout, waiting collector", slog.String("module", "connect"))
		select {
		case <-collectDone:
			// 收集器已完成
			logUtil.GetLogger().Info("collector completed", slog.String("module", "connect"))
		case <-time.After(200 * time.Millisecond):
			// 给收集器额外的时间处理缓冲区中的数据
			logUtil.GetLogger().Info("collector timeout", slog.String("module", "connect"))
		}
		mu.Lock()
		count := len(connectList)
		mu.Unlock()
		logUtil.GetLogger().Info("collect connection info timeout completed", slog.String("module", "connect"), slog.Int("valid_count", count))
	}

	// 安全地返回结果
	mu.Lock()
	defer mu.Unlock()
	return connectList, nil
}

func (connectService *ConnectService) invalidateConnectsInfoCache() {
	connectService.connectsInfoCacheMu.Lock()
	defer connectService.connectsInfoCacheMu.Unlock()
	connectService.connectsInfoCache = nil
	connectService.connectsInfoCacheExpires = time.Time{}
	connectService.connectsInfoCacheValid = false
}

func (connectService *ConnectService) getCachedConnectsInfo() ([]model.Connect, bool) {
	connectService.connectsInfoCacheMu.RLock()
	defer connectService.connectsInfoCacheMu.RUnlock()

	if !connectService.connectsInfoCacheValid {
		return nil, false
	}
	if time.Now().After(connectService.connectsInfoCacheExpires) {
		return nil, false
	}
	return cloneConnects(connectService.connectsInfoCache), true
}

func (connectService *ConnectService) setCachedConnectsInfo(connects []model.Connect) {
	connectService.connectsInfoCacheMu.Lock()
	defer connectService.connectsInfoCacheMu.Unlock()
	connectService.connectsInfoCache = cloneConnects(connects)
	connectService.connectsInfoCacheExpires = time.Now().Add(connectsInfoCacheTTL)
	connectService.connectsInfoCacheValid = true
}

func cloneConnects(connects []model.Connect) []model.Connect {
	if len(connects) == 0 {
		return []model.Connect{}
	}
	cloned := make([]model.Connect, len(connects))
	copy(cloned, connects)
	return cloned
}

// GetConnects 获取当前实例添加的所有连接
func (connectService *ConnectService) GetConnects() ([]model.Connected, error) {
	// 获取所有连接地址
	connects, err := connectService.connectRepository.GetAllConnects(context.Background())
	if err != nil {
		return nil, err
	}

	// 如果没有找到，返回空切片
	if len(connects) == 0 {
		return []model.Connected{}, nil
	}

	// 返回查询到的 connects
	return connects, nil
}

// GetConnectsHealth 探测每个已保存互联地址的可达性并返回远端实例版本（即时探测，不做缓存）
func (connectService *ConnectService) GetConnectsHealth() ([]model.ConnectedHealth, error) {
	connects, err := connectService.connectRepository.GetAllConnects(context.Background())
	if err != nil {
		return nil, err
	}
	if len(connects) == 0 {
		return []model.ConnectedHealth{}, nil
	}

	out := make([]model.ConnectedHealth, len(connects))
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), healthOverallTimeout)
	defer cancelProbe()

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, connectFanoutMaxConcurrency)

	for i := range connects {
		wg.Add(1)
		go func(i int, conn model.Connected) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
			case <-probeCtx.Done():
				out[i] = model.ConnectedHealth{
					ID: conn.ID, ConnectURL: conn.ConnectURL,
					Status: "offline", Version: "",
				}
				return
			}
			defer func() { <-semaphore }()

			h := model.ConnectedHealth{
				ID: conn.ID, ConnectURL: conn.ConnectURL,
				Status: "offline", Version: "",
			}

			data, err := connectService.peerFetcher(conn.ConnectURL, healthProbeTimeout)
			if err != nil {
				out[i] = h
				return
			}
			h.Status = "online"
			h.Version = data.Version
			out[i] = h
		}(i, connects[i])
	}

	wg.Wait()
	return out, nil
}
