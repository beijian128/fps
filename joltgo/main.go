package main

// 入口：组装 Jolt 物理桥 + ECS 模拟 + WebSocket hub，启动 20 Hz 模拟 tick。

import (
	"joltgo/sim"
	"log"
	"net/http"
)

func main() {
	simulation := sim.New(newJoltPhysics())
	simulation.Init()

	hub := newWsHub(simulation)
	mux := http.NewServeMux()
	mux.HandleFunc("/", hub.handle)

	go hub.runTicker()

	addr := ":8080"
	log.Printf("Jolt Physics FPS demo listening on ws://localhost%s/", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
