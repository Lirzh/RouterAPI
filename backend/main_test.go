package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// 监听侧超时是这次改动的核心决策，尤其"WriteTimeout 必须为 0"——
// 有人为了"更严谨"给它加上有限值，就会把每一个流式请求在半路切断。
func TestHTTPServerTimeouts(t *testing.T) {
	srv := newHTTPServer(http.NotFoundHandler(), 8080)

	if srv.Addr != ":8080" {
		t.Errorf("Addr = %q, want :8080", srv.Addr)
	}
	if srv.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout 必须为正：它是 Slowloris 的主要防线")
	}
	if srv.ReadTimeout <= 0 {
		t.Error("ReadTimeout 必须为正：慢速滴流的 body 不能无限占住连接")
	}
	if srv.IdleTimeout <= 0 {
		t.Error("IdleTimeout 必须为正：keep-alive 空闲连接要能回收")
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want 0（SSE 是分钟级响应，有限值会切断流式）", srv.WriteTimeout)
	}
	// 读预算还得容得下"最大请求体 + 慢链路"，否则配了大 body 上限也传不上来
	if srv.ReadTimeout < 5*time.Minute {
		t.Errorf("ReadTimeout = %v, 太短会让大 body 上传失败", srv.ReadTimeout)
	}
}

// 优雅停机要同时满足三件事：在途请求写完、runServer 正常返回、端口随后不再接客。
// 只断第一条的话，"其实根本没停"也能通过。
func TestRunServerDrainsInFlight(t *testing.T) {
	const body = "the-whole-answer"
	var (
		entered = make(chan struct{}, 1)
		release = make(chan struct{})
	)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{} // 模拟一次还在等上游的补全
		<-release
		_, _ = io.WriteString(w, body)
	})
	addr, srv, shutdown := loopbackServer(handler)
	done := make(chan error, 1)
	go func() { done <- runServer(srv, shutdown, 10*time.Second) }()
	waitForListener(t, addr)

	respCh := make(chan string, 1)
	go func() {
		resp, err := http.Get(addr)
		if err != nil {
			respCh <- "err:" + err.Error()
			return
		}
		defer resp.Body.Close()
		all, _ := io.ReadAll(resp.Body)
		respCh <- string(all)
	}()
	// 必须确认真的进了 handler 再停机，否则测的是"请求还没发出去"
	awaitSignal(t, entered, "请求没有进入 handler")

	close(shutdown)
	close(release)

	if got := <-respCh; got != body {
		t.Errorf("在途请求被截断: got %q want %q", got, body)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runServer 返回错误: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runServer 没有返回")
	}
	if _, err := http.Get(addr); err == nil {
		t.Error("停机完成后端口仍在接客")
	}
}

// 排空预算不是"永远等"：一个赖着不写完的请求不能把滚动发布卡死。
func TestRunServerHonorsDrainBudget(t *testing.T) {
	entered := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second): // 远超排空预算
		}
		_, _ = io.WriteString(w, "late")
	})
	addr, srv, shutdown := loopbackServer(handler)
	done := make(chan error, 1)
	go func() { done <- runServer(srv, shutdown, 300*time.Millisecond) }()
	waitForListener(t, addr)

	respCh := make(chan error, 1)
	go func() {
		resp, err := http.Get(addr)
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		respCh <- err
	}()
	awaitSignal(t, entered, "请求没有进入 handler")
	close(shutdown)

	start := time.Now()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runServer 返回错误: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("排空预算没起作用，runServer 还在等那个赖着的请求")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("预算 300ms，却拖了 %v 才退出", elapsed)
	}
	if err := <-respCh; err == nil {
		t.Error("被强关的在途请求应以读错误结束")
	}
}

// ListenAndServe 自己就失败了（端口被占）时必须立刻把错误抛出去，
// 而不是坐等一个永远不会来的停机信号。
func TestRunServerReportsBindError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	shutdown := make(chan struct{})
	srv := &http.Server{Addr: ln.Addr().String(), Handler: http.NotFoundHandler()}
	result := make(chan error, 1)
	go func() { result <- runServer(srv, shutdown, time.Second) }()

	select {
	case got := <-result:
		close(shutdown)
		if got == nil || errors.Is(got, http.ErrServerClosed) {
			t.Fatalf("端口被占应返回真实错误, got %v", got)
		}
	case <-time.After(5 * time.Second):
		close(shutdown)
		t.Fatal("绑定失败时 runServer 没有立即返回（会表现成\"服务起了但没起来\"）")
	}
}

// loopbackServer 只绑 127.0.0.1：newHTTPServer 的 ":port" 会绑全网卡，
// 测试没必要惊动防火墙，也没必要赌某个端口能否被独占。
func loopbackServer(handler http.Handler) (addr string, srv *http.Server, shutdown chan struct{}) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		panic(err)
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port),
		&http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", port), Handler: handler},
		make(chan struct{})
}

// waitForListener 轮询到端口确实开始 accept，替代靠 sleep 猜"服务起来了"。
func waitForListener(t *testing.T, addr string) {
	t.Helper()
	host := addr[len("http://"):]
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", host)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("服务未在 %s 上开始监听", addr)
}

// awaitSignal 等一个"事件真的发生了"的信号，超时就失败——
// 停机测试里如果请求根本没进 handler，后面所有断言都是在测别的东西。
func awaitSignal(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal(msg)
	}
}
