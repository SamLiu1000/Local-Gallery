package main

// ============================================================
// 双优先级任务队列
//
// 目标：大图读取（查看器/GetImageFile）与缩略图生成分开调度——
// HIGH（大图读取）永远先于 LOW（缩略图生成）被执行，大图请求
// 不会被排队的缩略图任务拖住。
//
// 并发语义：LOW 任务内部自行 acquire thumbSem（并发设置不变），
// 队列的 worker 数量只是执行载体、不是并发上限，因此无需随
// thumbConcurrency 调整。
//
// 纯标准库实现，无 build tag（主构建与 bindings 构建共用）。
// ============================================================

import "sync"

// ★ activeScanOps：当前进行中的扫描/增量刷新数量（原子）。
//   声明在这个无 build-tag 的文件里，主构建与 bindings 构建都能引用
//   （app_thumbnail.go 带 !bindings tag，bindings 构建看不到它）。
//   triggerAutoPreGen 据此判断"导入仍在进行"，期间不做后台预生成，
//   避免与扫描写库、FTS 索引、尺寸回填抢磁盘把前台缩略图生成拖慢数十倍。
var activeScanOps int32

// ★ onDemandActive：正在生成的前台 on-demand 缩略图数量（原子）。
//   同样声明在无 build-tag 文件里供两种构建共用；预生成/索引据此让出 CPU。
var onDemandActive int32

// prioTask 一个待执行任务。done 非空时执行完毕后关闭，供等待方唤醒。
type prioTask struct {
	fn   func()
	done chan struct{}
}

var (
	prioHighCh = make(chan prioTask, 128)  // 大图读取
	prioLowCh  = make(chan prioTask, 4096) // 缩略图生成

	prioWorkersOnce sync.Once
)

// prioWorkerCount 队列 worker 数量。只负责从通道取任务执行，
// LOW 的并发上限由任务内部的 thumbSem 决定，所以这里取一个
// 足够大的固定值即可，不与并发设置耦合。
const prioWorkerCount = 64

// startPriorityWorkers 惰性启动队列 worker（首次入队时）。
func startPriorityWorkers() {
	prioWorkersOnce.Do(func() {
		for i := 0; i < prioWorkerCount; i++ {
			go priorityWorker()
		}
	})
}

// priorityWorker 严格优先调度：先非阻塞取 HIGH，取不到才在
// HIGH/LOW 之间 select —— 只要 HIGH 有任务，就绝不会去取 LOW。
func priorityWorker() {
	for {
		select {
		case t := <-prioHighCh:
			runPrioTask(t)
			continue
		default:
		}
		select {
		case t := <-prioHighCh:
			runPrioTask(t)
		case t := <-prioLowCh:
			runPrioTask(t)
		}
	}
}

func runPrioTask(t prioTask) {
	t.fn()
	if t.done != nil {
		close(t.done)
	}
}

// enqueueHighWait 入 HIGH 队列（大图读取），阻塞到任务执行完毕。
func enqueueHighWait(fn func()) {
	startPriorityWorkers()
	done := make(chan struct{})
	prioHighCh <- prioTask{fn: fn, done: done}
	<-done
}

// enqueueLowWait 入 LOW 队列（缩略图生成），阻塞到任务执行完毕。
// 注意：LOW 任务内部应自行 acquire thumbSem，保持并发设置不变。
func enqueueLowWait(fn func()) {
	startPriorityWorkers()
	done := make(chan struct{})
	prioLowCh <- prioTask{fn: fn, done: done}
	<-done
}
