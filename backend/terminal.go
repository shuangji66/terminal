package main

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// wsAcceptKey computes the Sec-WebSocket-Accept value for a handshake key.
func wsAccept(key string) string {
	h := sha1.New()
	io.WriteString(h, key+"258EAFA5-E914-47DA-95CA-C5AB0DC85B11")
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

const (
	// wsCloseTaken 是「会话被其他设备接管」的 WebSocket 关闭码（4000–4999 为应用自定
	// 区间）。前端据此把标签置为 detached：显示提示、不自动重连（否则两台设备会互抢）。
	wsCloseTaken = 4001
	// oscDetached 是顶号通知（OSC 控制帧，不写入 PTY）。与 close 帧双保险：
	// 帧先到 → 立即提示；帧被代理吞掉时还能靠 close 码判定。
	oscDetached = "\x1b]detached\x07"

	opText  = 0x1
	opBin   = 0x2
	opCont  = 0x0
	opClose = 0x8
	opPing  = 0x9
	opPong  = 0xA
)

// wsFrame builds a single unmasked server->client frame.
func wsFrame(op byte, payload []byte) []byte {
	n := len(payload)
	var hdr []byte
	if n < 126 {
		hdr = []byte{0x80 | op, byte(n)}
	} else if n < 65536 {
		hdr = []byte{0x80 | op, 126, byte(n >> 8), byte(n)}
	} else {
		hdr = []byte{0x80 | op, 127}
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		hdr = append(hdr, b[:]...)
	}
	return append(hdr, payload...)
}

// wsCloseFrame 构造服务端 close 帧：payload = 2 字节状态码 + UTF-8 reason。
func wsCloseFrame(code uint16, reason string) []byte {
	payload := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(payload, code)
	copy(payload[2:], reason)
	return wsFrame(opClose, payload)
}

// kickDetached 通知被顶掉的旧连接：先发 \x1b]detached\x07，再发 close(4001)，最后关闭。
// 写带 1s 超时：旧连接 TCP 缓冲可能已满，绝不能让这次写阻塞拖住新设备的挂载。
func kickDetached(c net.Conn) {
	_ = c.SetWriteDeadline(time.Now().Add(time.Second))
	_, _ = c.Write(wsFrame(opText, []byte(oscDetached)))
	_, _ = c.Write(wsCloseFrame(wsCloseTaken, "detached"))
	_ = c.Close()
}

// wsReadFrame reads a single client frame, unmasking the payload. It returns
// the opcode, the FIN bit and the unmasked payload. A partial reader is passed
// in so the buffered bytes from the handshake are not lost.
func wsReadFrame(r io.Reader) (byte, bool, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, false, nil, err
	}
	fin := h[0]&0x80 != 0
	op := h[0] & 0x0f
	masked := h[1]&0x80 != 0
	ln := uint64(h[1] & 0x7f)
	if ln == 126 {
		var e [2]byte
		if _, err := io.ReadFull(r, e[:]); err != nil {
			return 0, false, nil, err
		}
		ln = uint64(binary.BigEndian.Uint16(e[:]))
	} else if ln == 127 {
		var e [8]byte
		if _, err := io.ReadFull(r, e[:]); err != nil {
			return 0, false, nil, err
		}
		ln = binary.BigEndian.Uint64(e[:])
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, false, nil, err
		}
	}
	payload := make([]byte, ln)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, false, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return op, fin, payload, nil
}

// localeLang 决定会话的 LANG。中文输入依赖 UTF-8：若 LANG 为空或非 UTF-8，则回退
// 到内建于 glibc 的 C.UTF-8（Debian 11+ 可用）。
func localeLang(s string) string {
	u := strings.ToUpper(s)
	if s != "" && (strings.Contains(u, "UTF-8") || strings.Contains(u, "UTF8")) {
		return s
	}
	return "C.UTF-8"
}

// terminalHandler serves the terminal WebSocket on the admin unix socket.
type terminalHandler struct {
	mgr *SessionManager
}

// ServeHTTP hijacks the connection, upgrades to WebSocket, then either creates
// a new session (id empty → 先发 \x1b]id;<id>\x07 控制帧) or attaches to an
// existing one (回放历史 + \x1b]ready\x07 后进入实时流)。
// 浏览器断开只解挂载；会话继续运行并写入历史临时文件，刷新后可重新挂载。
func (t *terminalHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return
	}
	proto := r.Header.Get("Sec-WebSocket-Protocol")
	conn, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	protoHdr := ""
	if proto != "" {
		protoHdr = "\r\nSec-WebSocket-Protocol: " + proto
	}
	conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + wsAccept(key) + protoHdr + "\r\n\r\n"))
	_ = brw.Flush()

	id := r.URL.Query().Get("id")
	if id == "" {
		// 新建会话：解析“以哪个用户运行”（user=root | app:<APP NAME> | 空=NAS 用户 |
		// 数字=uid），目标 NAS 用户来自网关 unix sock 传递的 X-Trim-Userid 请求头。
		runAs, uerr := resolveRunUser(t.mgr.renv, r.URL.Query().Get("user"), r.Header.Get("X-Trim-Userid"))
		if uerr != nil {
			conn.Write(wsFrame(opText, []byte("\r\n\x1b[31m"+uerr.Error()+"\x1b[0m\r\n")))
			conn.Write(wsFrame(opClose, nil))
			conn.Close()
			return
		}
		s, cerr := t.mgr.create(runAs)
		if cerr != nil {
			conn.Write(wsFrame(opText, []byte("\r\n\x1b[31mcreate session failed: "+cerr.Error()+"\x1b[0m\r\n")))
			conn.Write(wsFrame(opClose, nil))
			conn.Close()
			return
		}
		conn.Write(wsFrame(opText, []byte("\x1b]id;"+s.id+"\x07")))
		if _, aerr := s.attach(conn); aerr != nil {
			conn.Close()
			return
		}
		id = s.id
	} else {
		s, ok := t.mgr.get(id)
		if !ok {
			conn.Write(wsFrame(opText, []byte("\r\n\x1b[31msession not found: "+id+"\x1b[0m\r\n")))
			conn.Write(wsFrame(opClose, nil))
			conn.Close()
			return
		}
		// 单挂载点：挂载成功即成为操作端，旧连接被顶掉并收到「已被接管」通知。
		prev, aerr := s.attach(conn)
		if aerr != nil {
			conn.Close()
			return
		}
		if prev != nil {
			kickDetached(prev)
		}
	}

	// 读取客户端帧：普通数据写入 PTY；resize/ping 为 OSC 控制消息不进 PTY。
	// 浏览器发送的普通消息都是单帧，但网关/代理可能分片（op=0 continuation），
	// 按 FIN 位累积完整消息后再处理，避免长粘贴被截断。
	var msgBuf []byte
	for {
		op, fin, payload, rerr := wsReadFrame(brw)
		if rerr != nil {
			break
		}
		switch op {
		case opText, opBin, opCont:
			if op != opCont {
				msgBuf = msgBuf[:0]
			}
			msgBuf = append(msgBuf, payload...)
			if !fin {
				continue
			}
			s := string(msgBuf)
			if strings.HasPrefix(s, "\x1b]resize;") && strings.HasSuffix(s, "\x07") {
				trimmed := strings.TrimSuffix(strings.TrimPrefix(s, "\x1b]resize;"), "\x07")
				parts := strings.Split(trimmed, ";")
				if len(parts) == 2 {
					cols, err1 := strconv.Atoi(parts[0])
					rows, err2 := strconv.Atoi(parts[1])
					if err1 == nil && err2 == nil && cols > 0 && rows > 0 {
						if sess, ok := t.mgr.get(id); ok {
							// 只有操作端的尺寸生效（被顶掉的旧设备会随后触发一次重排）
							if err := sess.resize(conn, uint16(cols), uint16(rows)); err != nil && !errors.Is(err, errNotOwner) {
								logger().Printf("[terminal] resize error: %v", err)
							}
						}
					}
				}
			} else if strings.HasPrefix(s, "\x1b]ping") && strings.HasSuffix(s, "\x07") {
				// 心跳：仅保持连接不被代理/NAT 空闲超时断开，不写入 PTY
			} else {
				if sess, ok := t.mgr.get(id); ok {
					// 非操作端的残留输入在服务端丢弃（见 Session.writeInput 的说明）
					if werr := sess.writeInput(conn, msgBuf); werr != nil && !errors.Is(werr, errNotOwner) {
						logger().Printf("[terminal] write input error: %v", werr)
					}
				}
			}
		case opPing:
			conn.Write(wsFrame(opPong, payload))
		case opClose:
			goto done
		}
	}
done:
	// 只解除挂载，不杀会话（会话继续运行并写入历史文件）
	if sess, ok := t.mgr.get(id); ok {
		sess.detach(conn)
	}
	conn.Close()
}