package main

// 入口：组装 Jolt 物理桥 + ECS 模拟 + WebSocket hub，启动 20 Hz 模拟 tick。
// 收到 Ctrl+C / SIGTERM 时优雅退出：先停 HTTP 服务，再停 tick 循环、释放物理世界。

import (
	"context"
	"joltgo/sim"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"
)

func main() {
	simulation := sim.New(newJoltPhysics())
	simulation.Init()

	hub := newWsHub(simulation)
	mux := http.NewServeMux()
	mux.HandleFunc("/", hub.handle)

	hub.startTicker()

	addr := ":8080"
	srv := &http.Server{Addr: addr, Handler: mux}
	log.Printf("Jolt Physics FPS demo listening on ws://localhost%s/", addr)

	// 信号 → 优雅关闭：http.Shutdown 会停止接受新连接并等现有连接处理完。
	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, os.Interrupt)
		<-quit
		log.Print("shutting down…")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("http shutdown: %v", err)
		}
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}

	// HTTP 服务已停止：停 20 Hz tick 循环，然后释放物理世界资源。
	hub.stopTicker()
	simulation.Shutdown()
	log.Print("stopped")
}
