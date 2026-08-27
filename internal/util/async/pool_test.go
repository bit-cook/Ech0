// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package async

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWorkerPool_SubmitWaitRunsAllJobs 校验：提交的全部任务都会被执行，Wait 在全部完成后返回。
func TestWorkerPool_SubmitWaitRunsAllJobs(t *testing.T) {
	cases := []struct {
		name        string
		workerCount int
		queueSize   int
		jobs        int
	}{
		{"single worker", 1, 1, 50},
		{"multi worker", 4, 8, 200},
		{"more workers than jobs", 8, 16, 3},
		{"unbuffered queue", 2, 0, 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := NewWorkerPool(tc.workerCount, tc.queueSize)
			defer pool.Stop()

			var done atomic.Int32
			for range tc.jobs {
				pool.Submit(func() error {
					done.Add(1)
					return nil
				})
			}
			pool.Wait()

			assert.Equal(t, int32(tc.jobs), done.Load(), "全部任务都应被执行")
		})
	}
}

// TestWorkerPool_JobErrorsDoNotBlockWait 校验：任务返回 error（被记录）不影响其它任务执行与 Wait 完成。
func TestWorkerPool_JobErrorsDoNotBlockWait(t *testing.T) {
	pool := NewWorkerPool(3, 4)
	defer pool.Stop()

	const total = 60
	var executed atomic.Int32
	for i := range total {
		failing := i%2 == 0
		pool.Submit(func() error {
			executed.Add(1)
			if failing {
				return errors.New("boom")
			}
			return nil
		})
	}
	pool.Wait()

	assert.Equal(t, int32(total), executed.Load(), "返回错误的任务也应计入执行并不阻塞 Wait")
}

// TestWorkerPool_SubmitAfterStopIsNoop 校验：Stop 之后 Submit 直接丢弃（no-op），不执行、不 panic、Wait 立即返回。
func TestWorkerPool_SubmitAfterStopIsNoop(t *testing.T) {
	pool := NewWorkerPool(2, 4)

	var before atomic.Int32
	for range 10 {
		pool.Submit(func() error {
			before.Add(1)
			return nil
		})
	}
	pool.Stop() // 内部会 Wait 直到前述任务全部完成
	require.Equal(t, int32(10), before.Load(), "Stop 前提交的任务应全部完成")

	var after atomic.Int32
	require.NotPanics(t, func() {
		for range 10 {
			pool.Submit(func() error {
				after.Add(1)
				return nil
			})
		}
	}, "Stop 之后 Submit 不应 panic")

	pool.Wait() // 不应阻塞
	assert.Zero(t, after.Load(), "Stop 之后提交的任务应被丢弃，不被执行")
}

// TestWorkerPool_StopIsIdempotent 校验：多次 Stop 不会因重复 close channel 而 panic（stopOnce 保护）。
func TestWorkerPool_StopIsIdempotent(t *testing.T) {
	pool := NewWorkerPool(2, 2)

	var done atomic.Int32
	for range 5 {
		pool.Submit(func() error {
			done.Add(1)
			return nil
		})
	}

	require.NotPanics(t, func() {
		pool.Stop()
		pool.Stop()
		pool.Stop()
	}, "重复 Stop 不应 panic")
	assert.Equal(t, int32(5), done.Load())
}

// TestWorkerPool_StopWaitsForInFlightJobs 校验：Stop 会阻塞直到已入队任务全部执行完毕（内部 Wait）。
// 用任务自身的同步信号（channel）协调，不依赖计时。
func TestWorkerPool_StopWaitsForInFlightJobs(t *testing.T) {
	pool := NewWorkerPool(1, 8)

	release := make(chan struct{})
	started := make(chan struct{})
	var finished atomic.Int32

	// 第一个任务阻塞直到收到 release，占住唯一 worker。
	pool.Submit(func() error {
		close(started)
		<-release
		finished.Add(1)
		return nil
	})
	<-started // 确认任务已被 worker 取走并开始执行

	// 再排入若干任务，它们会在 release 之后由同一个 worker 依次执行。
	const queued = 5
	for range queued {
		pool.Submit(func() error {
			finished.Add(1)
			return nil
		})
	}

	// 在后台调用 Stop，并在其返回后发信号；释放阻塞任务前 Stop 不应返回。
	stopReturned := make(chan struct{})
	go func() {
		pool.Stop()
		close(stopReturned)
	}()

	// 此刻 Stop 仍应被阻塞（still-running 任务未完成）。
	select {
	case <-stopReturned:
		t.Fatal("Stop 不应在仍有在途任务时返回")
	default:
	}

	close(release) // 放行，全部任务得以完成
	<-stopReturned // Stop 现在应当返回

	assert.Equal(t, int32(queued+1), finished.Load(), "Stop 返回时所有入队任务都应已执行")
}

// TestWorkerPool_ConcurrentSubmitAndStop 是关停竞争的回归测试：大量 goroutine 并发 Submit
// 的同时另一线程 Stop。修复前 Submit 在 RUnlock 之后才向 channel 发送，与 Stop 的
// close(jobs) 形成 send-after-close 窗口（旧实现靠 recover 兜 panic）；修复后 Submit 全程持读锁
// 直到发送完成，而 close 仅在写锁内发生 —— 二者互斥，结构上不可能向已关闭 channel 发送。
//
// 在 `-race -count=N` 下反复运行应当：不 panic、无数据竞争、且 Stop/Wait 不阻塞（wg 计数始终平衡）。
func TestWorkerPool_ConcurrentSubmitAndStop(t *testing.T) {
	pool := NewWorkerPool(4, 8)

	const submitters = 16
	const perSubmitter = 50

	var executed atomic.Int32
	var submittersWg sync.WaitGroup
	submittersWg.Add(submitters)
	for range submitters {
		go func() {
			defer submittersWg.Done()
			for range perSubmitter {
				pool.Submit(func() error {
					executed.Add(1)
					return nil
				})
			}
		}()
	}

	// 在提交途中并发关停（Stop 内部会 close + Wait）。
	pool.Stop()

	// 所有 Submit 调用都应正常返回（不因 send-on-closed 崩溃），Wait 不应阻塞。
	require.NotPanics(t, func() {
		submittersWg.Wait()
		pool.Wait()
	}, "并发 Submit/Stop 不应 panic 或死锁")

	// 关停后被丢弃的任务数不确定，但执行数必须落在 [0, 全部] 内。
	got := executed.Load()
	assert.GreaterOrEqual(t, got, int32(0))
	assert.LessOrEqual(t, got, int32(submitters*perSubmitter), "执行数不应超过提交总数")
}
