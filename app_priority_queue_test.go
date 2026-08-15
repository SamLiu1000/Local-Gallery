package main

import (
	"testing"
	"time"
)

// TestPriorityQueueHighBeforeQueuedLow 验证核心不变量：
// 当 LOW 队列有任务排队时，新入队的 HIGH 任务必须优先于排队的 LOW 被执行。
//
// 手法：先用「入队后阻塞在 gate 上」的 LOW 任务占满全部 worker，
// 剩下的 LOW 在 prioLowCh 里排队；此时把 HIGH 写入带缓冲通道（确定已入队），
// 再释放一个 worker——该 worker 应立刻取 HIGH（而非排队的 LOW）。
func TestPriorityQueueHighBeforeQueuedLow(t *testing.T) {
	startPriorityWorkers()

	// enqueueHighWait 端到端：空闲 worker 下应能入队并等执行完成
	roundTrip := make(chan struct{})
	go func() {
		enqueueHighWait(func() { close(roundTrip) })
	}()
	select {
	case <-roundTrip:
	case <-time.After(3 * time.Second):
		t.Fatal("enqueueHighWait 未返回（done 通道未关闭）")
	}

	const lowCount = prioWorkerCount + 10 // 64 个占满 worker + 10 个排队
	gates := make([]chan struct{}, 0, lowCount)
	started := make(chan struct{}, lowCount)

	// 入队 lowCount 个 LOW，任务一执行就发 started 信号然后阻塞在 gate 上
	for i := 0; i < lowCount; i++ {
		gate := make(chan struct{})
		gates = append(gates, gate)
		go enqueueLowWait(func() {
			started <- struct{}{}
			<-gate
		})
	}
	defer func() {
		for _, g := range gates {
			select { // 防止重复 close 触发 panic
			case <-g:
			default:
				close(g)
			}
		}
	}()

	// 等全部 worker 都被 LOW 占满（其余 LOW 处于排队状态）
	timeout := time.After(5 * time.Second)
	for i := 0; i < prioWorkerCount; i++ {
		select {
		case <-started:
		case <-timeout:
			t.Fatal("LOW 任务未能占满队列 worker")
		}
	}

	// ★ HIGH 直接写入带缓冲通道（同步，确定此刻已入队）
	highDone := make(chan struct{})
	prioHighCh <- prioTask{fn: func() { close(highDone) }}

	// 释放一个 worker：它必须取 HIGH（而不是排队的 LOW）
	close(gates[0])
	select {
	case <-highDone:
		// ✓ HIGH 优先于排队的 LOW 被执行
	case <-time.After(3 * time.Second):
		t.Fatal("HIGH 任务被排队的 LOW 卡住了：优先级调度未生效")
	}
}
