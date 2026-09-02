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

type wsClient struct {
	conn net.Conn
	send chan []byte
}

type wsHub struct {
	mu      sync.Mutex
	clients map[*wsClient]struct{}
	game    *Game
}

func newWsHub(game *Game) *wsHub {
	return &wsHub{clients: map[*wsClient]struct{}{}, game: game}
}

// clientMessage 是客户端上行消息（type 缺省视为 input）。
type clientMessage struct {
	Type   string     `json:"type"` // "", "input", "shoot", "reset"
	Move   [2]float32 `json:"move"`
	Jump   bool       `json:"jump"`
	Origin [3]float32 `json:"origin"`
	Dir    [3]float32 `json:"dir"`
}

// runTicker 以 20 Hz 固定节奏推进模拟，并广播状态给所有客户端。
// 服务器权威：模拟快慢与客户端数量、客户端帧率无关。
func (h *wsHub) runTicker() {
	ticker := time.NewTicker(time.Second / 20)
	defer ticker.Stop()
	for range ticker.C {
		h.game.Step()
		h.broadcast(encodeState(h.game.Snapshot()))
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

	c := &wsClient{conn: conn, send: make(chan []byte, wsSendCap)}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	go c.writeLoop()
	go c.readLoop(h)

	// 连接后立即推送一帧当前状态，客户端无需再拉取初始快照。
	c.send <- encodeState(h.game.Snapshot())
}

// broadcast 向所有客户端推送。发送不出去的慢客户端直接断开重连，
// 不能拖慢 20 Hz 的模拟 tick。
func (h *wsHub) broadcast(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- msg:
		default:
			delete(h.clients, c)
			c.conn.Close()
			close(c.send)
		}
	}
}

// remove 需要与 broadcast 持同一把锁，避免向已关闭的 channel 写入。
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

func (c *wsClient) writeLoop() {
	for data := range c.send {
		_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteSec))
		if err := writeWSFrame(c.conn, opText, data); err != nil {
			c.conn.Close()
			return
		}
	}
}

// readLoop 解析客户端帧并分发消息；出错或对端关闭即退出。
func (c *wsClient) readLoop(h *wsHub) {
	defer h.remove(c)
	br := bufio.NewReader(c.conn)
	for {
		op, payload, err := readWSFrame(br)
		if err != nil {
			return
		}
		switch op {
		case opText:
			h.handleMessage(payload)
		case opClose: // 回一个 close 帧后关闭
			_ = writeWSFrame(c.conn, opClose, []byte{})
			return
		case opPing:
			if writeWSFrame(c.conn, opPong, payload) != nil {
				return
			}
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
	opcode = hdr[0] & 0x0F
	masked := hdr[1]&0x80 != 0
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
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(br, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(br, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i&3]
		}
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
