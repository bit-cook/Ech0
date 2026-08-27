// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package webhook

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	webhookModel "github.com/lin-snow/ech0/internal/model/webhook"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fastBackoff 用极小的 backoff，使被测代码自身的重试 sleep 可忽略不计；
// 断言依赖命中计数而非计时。
const fastBackoff = time.Nanosecond

// recordedRequest 记录服务端实际收到的一次请求快照。
type recordedRequest struct {
	headers http.Header
	body    []byte
}

// requestRecorder 在 handler 中线程安全地记录每次收到的请求。
// sendWithRetry 内部是串行重试，但 httptest 每个请求在独立 goroutine 处理，
// 用互斥锁保证 -race 下读写安全，断言统一放到主测试 goroutine。
type requestRecorder struct {
	mu       sync.Mutex
	requests []recordedRequest
}

func (r *requestRecorder) record(req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, recordedRequest{
		headers: req.Header.Clone(),
		body:    body,
	})
}

func (r *requestRecorder) snapshot() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedRequest, len(r.requests))
	copy(out, r.requests)
	return out
}

func (r *requestRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

// TestSendWithRetry_Success2xx 校验：任意 2xx 状态码立即成功、不重试（仅命中一次）。
func TestSendWithRetry_Success2xx(t *testing.T) {
	cases := []struct {
		name   string
		status int
	}{
		{"200 OK", http.StatusOK},
		{"201 Created", http.StatusCreated},
		{"202 Accepted", http.StatusAccepted},
		{"204 NoContent", http.StatusNoContent},
		{"299 edge", 299},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &requestRecorder{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				rec.record(req)
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			wh := &webhookModel.Webhook{URL: srv.URL, Secret: "s"}
			err := sendWithRetry(srv.Client(), wh, newObs(t), 3, fastBackoff)

			require.NoError(t, err, "2xx 必须视为成功")
			assert.Equal(t, 1, rec.count(), "成功时不应重试")
		})
	}
}

// TestSendWithRetry_ExhaustsOn5xx 校验：持续 5xx 时重试 maxRetries 次后返回最后一次错误。
func TestSendWithRetry_ExhaustsOn5xx(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		maxRetries int
	}{
		{"500 retries 3", http.StatusInternalServerError, 3},
		{"503 retries 2", http.StatusServiceUnavailable, 2},
		{"502 retries 5", http.StatusBadGateway, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &requestRecorder{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				rec.record(req)
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			wh := &webhookModel.Webhook{URL: srv.URL, Secret: "s"}
			err := sendWithRetry(srv.Client(), wh, newObs(t), tc.maxRetries, fastBackoff)

			require.Error(t, err, "持续 5xx 必须最终报错")
			assert.Contains(t, err.Error(), "unexpected status code", "错误应描述非 2xx 状态码")
			assert.Equal(t, tc.maxRetries, rec.count(), "应恰好重试 maxRetries 次")
		})
	}
}

// TestSendWithRetry_RecoversAfterTransient 校验：前几次 5xx、随后 2xx 时最终成功，命中次数=失败次数+1。
func TestSendWithRetry_RecoversAfterTransient(t *testing.T) {
	const failBefore = 2 // 前 2 次失败，第 3 次成功
	var hits atomic.Int32
	rec := &requestRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rec.record(req)
		if hits.Add(1) <= failBefore {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := &webhookModel.Webhook{URL: srv.URL, Secret: "s"}
	err := sendWithRetry(srv.Client(), wh, newObs(t), 5, fastBackoff)

	require.NoError(t, err, "瞬时失败后恢复应最终成功")
	assert.Equal(t, failBefore+1, rec.count(), "应在第一次成功后停止重试")
}

// TestSendWithRetry_SignatureEndToEnd 校验：带密钥时，服务端收到的签名头端到端正确，
// 且 X-Ech0-Event / Content-Type 等契约头存在。
func TestSendWithRetry_SignatureEndToEnd(t *testing.T) {
	const secret = "end-to-end-secret"
	rec := &requestRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rec.record(req)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	obs := newObs(t)
	wh := &webhookModel.Webhook{URL: srv.URL, Secret: secret}
	err := sendWithRetry(srv.Client(), wh, obs, 3, fastBackoff)
	require.NoError(t, err)

	reqs := rec.snapshot()
	require.Len(t, reqs, 1)
	got := reqs[0]

	sig := got.headers.Get("X-Ech0-Signature")
	require.NotEmpty(t, sig, "带密钥时服务端必须收到签名头")
	require.True(t, strings.HasPrefix(sig, "sha256="), "签名头必须带 sha256= 前缀")

	// 签名必须覆盖服务端实际收到的 body。
	want := "sha256=" + buildWebhookSignature(secret, got.body)
	assert.Equal(t, want, sig, "服务端收到的签名必须等于对收到 body 的 HMAC")

	// 契约头端到端存在。
	assert.Equal(t, obs.Topic, got.headers.Get("X-Ech0-Event"))
	assert.Equal(t, "application/json", got.headers.Get("Content-Type"))
	assert.Equal(t, "Ech0-Webhook-Client", got.headers.Get("User-Agent"))
}

// TestSendWithRetry_NoSecretNoSignature 校验：无密钥时服务端不应收到签名头。
func TestSendWithRetry_NoSecretNoSignature(t *testing.T) {
	rec := &requestRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rec.record(req)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := &webhookModel.Webhook{URL: srv.URL, Secret: ""}
	require.NoError(t, sendWithRetry(srv.Client(), wh, newObs(t), 2, fastBackoff))

	reqs := rec.snapshot()
	require.Len(t, reqs, 1)
	assert.Empty(t, reqs[0].headers.Get("X-Ech0-Signature"), "无密钥不应携带签名头")
}

// TestSendWithRetry_BodyDeliveredEachAttempt 校验：每次重试都向服务端发送完整且一致的 body
// （buildRequest 每次重建请求并设置 GetBody，body 可被重复读取）。
func TestSendWithRetry_BodyDeliveredEachAttempt(t *testing.T) {
	const secret = "k"
	var hits atomic.Int32
	rec := &requestRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rec.record(req)
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError) // 第一次失败触发重试
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	obs := newObs(t)
	wh := &webhookModel.Webhook{URL: srv.URL, Secret: secret}
	require.NoError(t, sendWithRetry(srv.Client(), wh, obs, 3, fastBackoff))

	reqs := rec.snapshot()
	require.Len(t, reqs, 2, "应发生一次重试，共两次请求")

	for i, r := range reqs {
		require.NotEmpty(t, r.body, "第 %d 次请求 body 不应为空", i)
		// 每次收到的 body 都应携带正确的事件载荷，并能据此重算出正确签名。
		assert.Contains(t, string(r.body), `"topic":"`+obs.Topic+`"`, "第 %d 次 body 应含 topic", i)
		want := "sha256=" + buildWebhookSignature(secret, r.body)
		assert.Equal(t, want, r.headers.Get("X-Ech0-Signature"), "第 %d 次签名应与该次 body 一致", i)
	}
	// 两次 body 必须完全一致（重试不应损坏/截断 body）。
	assert.Equal(t, reqs[0].body, reqs[1].body, "重试发送的 body 必须与首次一致")
}

// countingErrTransport 是一个始终返回连接错误的 RoundTripper，用于在不依赖真实网络的前提下
// 测试 client.Do 返回错误时的重试行为。
type countingErrTransport struct {
	calls *int32
}

func (c countingErrTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	atomic.AddInt32(c.calls, 1)
	return nil, errors.New("simulated connection failure")
}

// TestSendWithRetry_TransportErrorRetried 校验：client.Do 返回传输错误时也按 maxRetries 重试并最终报错。
func TestSendWithRetry_TransportErrorRetried(t *testing.T) {
	var calls int32
	client := &http.Client{Transport: countingErrTransport{calls: &calls}}
	wh := &webhookModel.Webhook{URL: "https://example.com/hook", Secret: "s"}

	const maxRetries = 4
	err := sendWithRetry(client, wh, newObs(t), maxRetries, fastBackoff)

	require.Error(t, err, "传输错误应最终冒泡")
	assert.Contains(t, err.Error(), "simulated connection failure")
	assert.Equal(t, int32(maxRetries), atomic.LoadInt32(&calls), "传输错误也应重试 maxRetries 次")
}

// TestSendWithRetry_BuildRequestError 校验：buildRequest 失败（非法 URL）时直接返回错误。
func TestSendWithRetry_BuildRequestError(t *testing.T) {
	var calls int32
	client := &http.Client{Transport: countingErrTransport{calls: &calls}}
	wh := &webhookModel.Webhook{URL: "://bad-url", Secret: "s"}

	err := sendWithRetry(client, wh, newObs(t), 3, fastBackoff)

	require.Error(t, err, "非法 URL 必须导致 buildRequest 报错")
	assert.Zero(t, atomic.LoadInt32(&calls), "buildRequest 失败时不应触达 transport")
}

// TestSendWithRetry_BackoffScheduleIsExponential 端到端校验重试节奏：真实的 HTTP 往返
// （httptest.NewTestServer 走内存网络）跑在 testing/synctest 的 bubble 里，假时钟让
// 秒级 backoff 既可精确断言又瞬时完成——其余用例靠 fastBackoff 规避等待，只能断言命中次数。
func TestSendWithRetry_BackoffScheduleIsExponential(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const initialBackoff = time.Second
		const maxRetries = 3

		start := time.Now()
		var mu sync.Mutex
		var offsets []time.Duration
		srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			offsets = append(offsets, time.Since(start))
			mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
		}))

		client := srv.Client() // 内存网络下，srv.URL 由首次 Client() 调用填充。
		wh := &webhookModel.Webhook{URL: srv.URL, Secret: "s"}
		err := sendWithRetry(client, wh, newObs(t), maxRetries, initialBackoff)
		require.Error(t, err, "持续 5xx 必须最终报错")

		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t,
			[]time.Duration{0, initialBackoff, 3 * initialBackoff},
			offsets,
			"两次重试之间的等待必须逐次翻倍，且最后一次尝试后不再等待",
		)
		assert.Equal(t, 3*initialBackoff, time.Since(start), "最后一次尝试之后不应再 sleep")
	})
}
