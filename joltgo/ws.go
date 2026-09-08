package main

// 极简 WebSocket 服务端（仅标准库），游戏协议的唯一通道。
//
//   - 服务端 → 客户端：模拟 tick 后推送状态快照（文本帧，无掩码）
//   - 客户端 → 服务端：input / shoot / reset（文本帧，带掩码，单帧不拆分）
//
// 因此不引入第三方库，直接实现 RFC 6455 的握手和帧编解码。
// 约束：不支持分片帧（双方消息都很小，不会分片）；无 TLS（本地 demo）。

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"joltgo/sim"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	wsGUID     = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	opText     = 0x1
	opClose    = 0x8
	opPing     = 0x9
	opPong     = 0xA
	wsSendCap  = 16
	wsWriteSec = 5 * time.Second
	maxFrame   = 1 << 20 // 1 MiB，防止异常大帧
)

// outFrame 是下行通道里的一帧：op 是 WebSocket opcode（文本/关闭/ping/pong）。
// 所有下行帧（含 readLoop 的 close/pong 回复）都经 c.send 由唯一的 writeLoop
// 写出，保证同一连接上不会出现两个并发写者、帧序可控。
type outFrame struct {
	op   byte
	data []byte
}

type wsClient struct {
	conn net.Conn
	send chan outFrame
}

type wsHub struct {
	mu      sync.Mutex
	clients map[*wsClient]struct{}
	game    *sim.Simulation
	stop    chan struct{}
	wg      sync.WaitGroup
}

func newWsHub(game *sim.Simulation) *wsHub {
	return &wsHub{clients: map[*wsClient]struct{}{}, game: game, stop: make(chan struct{})}
}

// clientMessage 是客户端上行消息（type 缺省视为 input）。
type clientMessage struct {
	Type   string     `json:"type"` // "", "input", "shoot", "reset"
	Move   [2]float32 `json:"move"`
	Jump   bool       `json:"jump"`
	Origin [3]float32 `json:"origin"`
	Dir    [3]float32 `json:"dir"`
}

// startTicker 启动 20 Hz 模拟 tick goroutine；stopTicker 停止它并等待退出。
// 服务器权威：模拟快慢与客户端数量、客户端帧率无关。
func (h *wsHub) startTicker() {
	h.wg.Add(1)
	go h.runTicker()
}

func (h *wsHub) stopTicker() {
	close(h.stop)
	h.wg.Wait()
}

func (h *wsHub) runTicker() {
	defer h.wg.Done()
	ticker := time.NewTicker(time.Second / 20)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			h.game.Step()
			h.broadcast(encodeState(h.game.Snapshot()))
		case <-h.stop:
			return
		}
	}
}

// handle 完成 WebSocket 握手，随后启动读写两个 goroutine。
func (h *wsHub) handle(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
		!strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return
	}
	// 只接受 RFC 6455 第 13 版握手（浏览器 / Godot WebSocketPeer 都发 13）。
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		http.Error(w, "unsupported websocket version", http.StatusBadRequest)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	sum := sha1.Sum([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	_, err = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n")
	if err == nil {
		err = rw.Flush()
	}
	if err != nil {
		conn.Close()
		return
	}

	c := &wsClient{conn: conn, send: make(chan outFrame, wsSendCap)}

	// 连接后立即推送一帧当前状态，客户端无需再拉取初始快照。
	// 快照在注册前编码（encodeState→Snapshot 自带 sim 锁），随后在 h.mu 内投递：
	// remove()/broadcast 只在持锁时 close(c.send)，因此注册与投递之间不会发生
	// send-on-closed-channel 竞争；若缓冲已满则放弃首帧（等下个 tick 广播）。
	initial := outFrame{op: opText, data: encodeState(h.game.Snapshot())}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	select {
	case c.send <- initial:
	default:
	}
	h.mu.Unlock()
	go c.writeLoop(h)
	go c.readLoop(h, rw.Reader) // 复用 Hijack 的缓冲 Reader：紧随手握手的首帧不会丢
}

// broadcast 向所有客户端推送。发送不出去的慢客户端直接断开重连，
// 不能拖慢 20 Hz 的模拟 tick。
func (h *wsHub) broadcast(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- outFrame{op: opText, data: msg}:
		default:
			delete(h.clients, c)
			c.conn.Close()
			close(c.send)
		}
	}
}

// enqueue 向单个客户端的发送通道投递一帧（close/pong 等控制帧用）。
// 在 h.mu 下检查客户端仍存活并投递：客户端一旦被 remove()/broadcast 剔除，
// 通道即被关闭，绝不能对其发送；发送不出去（缓冲满）则直接放弃。
func (h *wsHub) enqueue(c *wsClient, m outFrame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; !ok {
		return
	}
	select {
	case c.send <- m:
	default:
	}
}

// remove 需要与 broadcast / enqueue 持同一把锁，避免向已关闭的 channel 写入。
func (h *wsHub) remove(c *wsClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; !ok {
		return
	}
	delete(h.clients, c)
	c.conn.Close()
	close(c.send)
}

// writeLoop 是每连接唯一的写者：所有下行（文本/关闭/ping 回复）都从 c.send
// 取出后整帧写出，杜绝并发写者导致帧序乱序；写出错时主动注销自己。
func (c *wsClient) writeLoop(h *wsHub) {
	for m := range c.send {
		_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteSec))
		if err := writeWSFrame(c.conn, m.op, m.data); err != nil {
			h.remove(c)
			return
		}
	}
}

// readLoop 解析客户端帧并分发消息；出错或对端关闭即退出。
// br 复用握手 Hijack 时带回的缓冲 Reader（可能已缓存紧随手握手的首帧）。
func (c *wsClient) readLoop(h *wsHub, br *bufio.Reader) {
	defer h.remove(c)
	for {
		op, payload, err := readWSFrame(br)
		if err != nil {
			return
		}
		switch op {
		case opText:
			h.handleMessage(payload)
		case opClose: // 回一个 close 帧后关闭（经 send 通道由 writeLoop 单写者发出）
			h.enqueue(c, outFrame{op: opClose, data: nil})
			return
		case opPing:
			h.enqueue(c, outFrame{op: opPong, data: payload})
		case opPong:
			// 客户端 pong：忽略（本地 demo 未做服务端心跳统计）
		}
	}
}

// handleMessage 处理客户端上行消息；产生状态变化的操作立即补推一帧，
// 客户端不用等到下一个 tick。
func (h *wsHub) handleMessage(data []byte) {
	var msg clientMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("ws: bad message: %v", err)
		return
	}
	switch msg.Type {
	case "", "input":
		h.game.ApplyInput(msg.Move, msg.Jump)
	case "shoot":
		h.game.Shoot(msg.Origin, msg.Dir)
		h.broadcast(encodeState(h.game.Snapshot()))
	case "reset":
		h.game.Reset()
		h.broadcast(encodeState(h.game.Snapshot()))
	}
}

// writeWSFrame 把整帧拼成一次 conn.Write，避免多个小写之间被并发写穿插。
func writeWSFrame(conn net.Conn, opcode byte, payload []byte) error {
	buf := make([]byte, 0, 14+len(payload))
	buf = append(buf, 0x80|opcode)
	switch n := len(payload); {
	case n < 126:
		buf = append(buf, byte(n))
	case n <= 0xFFFF:
		buf = append(buf, 126, byte(n>>8), byte(n))
	default:
		buf = append(buf, 127,
			byte(n>>56), byte(n>>48), byte(n>>40), byte(n>>32),
			byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	buf = append(buf, payload...)
	_, err := conn.Write(buf)
	return err
}

func readWSFrame(br *bufio.Reader) (opcode byte, payload []byte, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(br, hdr[:]); err != nil {
		return 0, nil, err
	}
	if hdr[0]&0x80 == 0 {
		return 0, nil, fmt.Errorf("fragmented frames unsupported")
	}
	if hdr[0]&0x70 != 0 {
		return 0, nil, fmt.Errorf("reserved bits set in frame header")
	}
	opcode = hdr[0] & 0x0F
	switch opcode {
	case opText, opClose, opPing, opPong:
	default:
		return 0, nil, fmt.Errorf("unsupported opcode 0x%x", opcode)
	}
	masked := hdr[1]&0x80 != 0
	// RFC 6455 §5.1：客户端 → 服务器帧必须掩码，未掩码即协议错误。
	if !masked {
		return 0, nil, fmt.Errorf("client frames must be masked")
	}
	n := uint64(hdr[1] & 0x7F)
	switch n {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(br, ext[:]); err != nil {
			return 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(br, ext[:]); err != nil {
			return 0, nil, err
		}
		n = binary.BigEndian.Uint64(ext[:])
	}
	if n > maxFrame {
		return 0, nil, fmt.Errorf("frame too large: %d", n)
	}
	// 控制帧（close/ping/pong）长度上限 125（FIN 已在上方强制）。
	if opcode >= opClose && n > 125 {
		return 0, nil, fmt.Errorf("control frame too large: %d", n)
	}
	var mask [4]byte
	if _, err = io.ReadFull(br, mask[:]); err != nil {
		return 0, nil, err
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(br, payload); err != nil {
		return 0, nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i&3]
	}
	return opcode, payload, nil
}

func encodeState(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("ws: encode state: %v", err)
		return []byte("{}")
	}
	return b
}
