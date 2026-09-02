package main

// 入口：创建游戏与 WebSocket hub，启动 20 Hz 模拟 tick。

import (
	"log"
	"net/http"
)

func main() {
	game := newGame()
	game.Init()

	hub := newWsHub(game)
	mux := http.NewServeMux()
	mux.HandleFunc("/", hub.handle)

	go hub.runTicker()

	addr := ":8080"
	log.Printf("Jolt Physics FPS demo listening on ws://localhost%s/", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
