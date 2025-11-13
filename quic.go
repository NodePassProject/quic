// Package quic 实现了基于 QUIC 协议的高性能、可靠的网络连接池管理系统
package quic

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	quic "github.com/quic-go/quic-go"
)

const (
	defaultMinCap           = 1
	defaultMaxCap           = 1
	defaultMinIvl           = 1 * time.Second
	defaultMaxIvl           = 1 * time.Second
	idReadTimeout           = 1 * time.Minute
	idRetryInterval         = 50 * time.Millisecond
	acceptRetryInterval     = 50 * time.Millisecond
	intervalAdjustStep      = 100 * time.Millisecond
	capacityAdjustLowRatio  = 0.2
	capacityAdjustHighRatio = 0.8
	intervalLowThreshold    = 0.2
	intervalHighThreshold   = 0.8
	defaultALPN             = "np-quic"
)

// Pool QUIC 连接池结构体，用于管理 QUIC streams
type Pool struct {
	streams      sync.Map                      // 存储 stream 的映射表
	idChan       chan string                   // 可用 stream ID 通道
	tlsCode      string                        // TLS 安全模式代码
	hostname     string                        // 主机名
	clientIP     string                        // 客户端 IP
	tlsConfig    *tls.Config                   // TLS 配置
	targetAddr   string                        // 目标地址
	listenAddr   string                        // 监听地址
	errCount     atomic.Int32                  // 错误计数
	capacity     atomic.Int32                  // 当前容量
	minCap       int                           // 最小容量
	maxCap       int                           // 最大容量
	interval     atomic.Int64                  // stream 创建间隔
	minIvl       time.Duration                 // 最小间隔
	maxIvl       time.Duration                 // 最大间隔
	keepAlive    time.Duration                 // 保活间隔
	ctx          context.Context               // 上下文
	cancel       context.CancelFunc            // 取消函数
	quicConn     atomic.Pointer[quic.Conn]     // QUIC 连接
	quicListener atomic.Pointer[quic.Listener] // QUIC 监听器
}

// StreamConn 将 QUIC Stream 包装为 net.Conn 接口
type StreamConn struct {
	*quic.Stream
	conn       *quic.Conn
	localAddr  net.Addr
	remoteAddr net.Addr
}

// LocalAddr 返回本地地址
func (s *StreamConn) LocalAddr() net.Addr {
	return s.localAddr
}

// RemoteAddr 返回远程地址
func (s *StreamConn) RemoteAddr() net.Addr {
	return s.remoteAddr
}

// SetDeadline 设置读写截止时间
func (s *StreamConn) SetDeadline(t time.Time) error {
	if err := s.SetReadDeadline(t); err != nil {
		return err
	}
	return s.SetWriteDeadline(t)
}

// NewClientPool 创建新的客户端 QUIC 池
func NewClientPool(
	minCap, maxCap int,
	minIvl, maxIvl time.Duration,
	keepAlive time.Duration,
	tlsCode string,
	hostname string,
	targetAddr string,
) *Pool {
	if minCap <= 0 {
		minCap = defaultMinCap
	}
	if maxCap <= 0 {
		maxCap = defaultMaxCap
	}
	if minCap > maxCap {
		minCap, maxCap = maxCap, minCap
	}

	if minIvl <= 0 {
		minIvl = defaultMinIvl
	}
	if maxIvl <= 0 {
		maxIvl = defaultMaxIvl
	}
	if minIvl > maxIvl {
		minIvl, maxIvl = maxIvl, minIvl
	}

	pool := &Pool{
		streams:    sync.Map{},
		idChan:     make(chan string, maxCap),
		tlsCode:    tlsCode,
		hostname:   hostname,
		targetAddr: targetAddr,
		minCap:     minCap,
		maxCap:     maxCap,
		minIvl:     minIvl,
		maxIvl:     maxIvl,
		keepAlive:  keepAlive,
	}
	pool.capacity.Store(int32(minCap))
	pool.interval.Store(int64(minIvl))
	pool.ctx, pool.cancel = context.WithCancel(context.Background())
	return pool
}

// NewServerPool 创建新的服务端 QUIC 池
func NewServerPool(
	maxCap int,
	clientIP string,
	tlsConfig *tls.Config,
	listenAddr string,
	keepAlive time.Duration,
) *Pool {
	if maxCap <= 0 {
		maxCap = defaultMaxCap
	}

	if listenAddr == "" {
		return nil
	}

	pool := &Pool{
		streams:    sync.Map{},
		idChan:     make(chan string, maxCap),
		clientIP:   clientIP,
		tlsConfig:  tlsConfig,
		listenAddr: listenAddr,
		maxCap:     maxCap,
		keepAlive:  keepAlive,
	}
	pool.ctx, pool.cancel = context.WithCancel(context.Background())
	return pool
}

// createStream 创建新的客户端 stream
func (p *Pool) createStream() bool {
	conn := p.quicConn.Load()
	if conn == nil {
		return false
	}

	// 打开新的 stream
	stream, err := conn.OpenStreamSync(p.ctx)
	if err != nil {
		return false
	}

	var id string

	// 接收 stream ID
	stream.SetReadDeadline(time.Now().Add(idReadTimeout))
	buf := make([]byte, 4)
	n, err := io.ReadFull(stream, buf)
	if err != nil || n != 4 {
		stream.Close()
		return false
	}
	id = hex.EncodeToString(buf)
	stream.SetReadDeadline(time.Time{})

	// 建立映射并存入通道
	p.streams.Store(id, stream)
	select {
	case p.idChan <- id:
		return true
	default:
		p.streams.Delete(id)
		stream.Close()
		return false
	}
}

// handleStream 处理新的服务端 stream
func (p *Pool) handleStream(stream *quic.Stream) {
	var streamClosed bool
	defer func() {
		if !streamClosed {
			stream.Close()
		}
	}()

	// 检查池是否已满
	if p.Active() >= p.maxCap {
		return
	}

	// 生成 stream ID
	rawID := make([]byte, 4)
	if _, err := rand.Read(rawID); err != nil {
		return
	}
	id := hex.EncodeToString(rawID)

	// 防止重复 stream ID
	if _, exist := p.streams.Load(id); exist {
		return
	}

	// 发送 ID 给客户端并在成功后建立映射
	if _, err := stream.Write(rawID); err != nil {
		return
	}

	// 尝试放入 idChan
	select {
	case p.idChan <- id:
		p.streams.Store(id, stream)
		streamClosed = true
	default:
		// 池满
		return
	}
}

// establishConnection 建立 QUIC 连接
func (p *Pool) establishConnection() error {
	if p.quicConn.Load() != nil {
		return nil // 已连接
	}

	// 配置 TLS
	var tlsConf *tls.Config
	switch p.tlsCode {
	case "0", "1":
		// 使用自签名证书（不验证）
		tlsConf = &tls.Config{
			InsecureSkipVerify: true,
			NextProtos:         []string{defaultALPN},
		}
	case "2":
		// 使用验证证书（安全模式）
		tlsConf = &tls.Config{
			InsecureSkipVerify: false,
			ServerName:         p.hostname,
			NextProtos:         []string{defaultALPN},
		}
	default:
		tlsConf = &tls.Config{
			InsecureSkipVerify: true,
			NextProtos:         []string{defaultALPN},
		}
	}

	// 建立 QUIC 连接
	conn, err := quic.DialAddr(p.ctx, p.targetAddr, tlsConf, &quic.Config{
		KeepAlivePeriod: p.keepAlive,
		MaxIdleTimeout:  p.keepAlive * 3,
	})
	if err != nil {
		return err
	}

	p.quicConn.Store(conn)
	return nil
}

// startListener 启动 QUIC 监听器
func (p *Pool) startListener() error {
	if p.quicListener.Load() != nil {
		return nil // 已启动
	}

	// 配置 TLS
	if p.tlsConfig == nil {
		return fmt.Errorf("server mode requires TLS config")
	}

	tlsConf := p.tlsConfig.Clone()
	tlsConf.NextProtos = []string{defaultALPN}

	// 启动 QUIC 监听器
	listener, err := quic.ListenAddr(p.listenAddr, tlsConf, &quic.Config{
		KeepAlivePeriod: p.keepAlive,
		MaxIdleTimeout:  p.keepAlive * 3,
	})
	if err != nil {
		return err
	}

	p.quicListener.Store(listener)
	return nil
}

// ClientManager 客户端 QUIC 池管理器
func (p *Pool) ClientManager() {
	if p.cancel != nil {
		p.cancel()
	}
	p.ctx, p.cancel = context.WithCancel(context.Background())

	// 建立 QUIC 连接
	for p.ctx.Err() == nil {
		if err := p.establishConnection(); err == nil {
			break
		}
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}

	// 管理 stream 池
	for p.ctx.Err() == nil {
		p.adjustInterval()
		capacity := int(p.capacity.Load())
		need := capacity - len(p.idChan)
		created := 0

		if need > 0 {
			var wg sync.WaitGroup
			results := make(chan int, need)
			for range need {
				wg.Go(func() {
					if p.createStream() {
						results <- 1
					}
				})
			}
			wg.Wait()
			close(results)
			for r := range results {
				created += r
			}
		}

		p.adjustCapacity(created)

		select {
		case <-p.ctx.Done():
			return
		case <-time.After(time.Duration(p.interval.Load())):
		}
	}
}

// ServerManager 服务端 QUIC 池管理器
func (p *Pool) ServerManager() {
	if p.cancel != nil {
		p.cancel()
	}
	p.ctx, p.cancel = context.WithCancel(context.Background())

	// 启动 QUIC 监听器
	if err := p.startListener(); err != nil {
		return
	}

	// 接受 QUIC 连接
	for p.ctx.Err() == nil {
		listener := p.quicListener.Load()
		if listener == nil {
			return
		}

		conn, err := listener.Accept(p.ctx)
		if err != nil {
			if p.ctx.Err() != nil {
				return
			}
			select {
			case <-p.ctx.Done():
				return
			case <-time.After(acceptRetryInterval):
			}
			continue
		}

		// 验证客户端 IP
		if p.clientIP != "" {
			remoteAddr := conn.RemoteAddr().(*net.UDPAddr)
			if remoteAddr.IP.String() != p.clientIP {
				conn.CloseWithError(0, "unauthorized IP")
				continue
			}
		}

		// 存储连接并接受 streams
		p.quicConn.Store(conn)

		// 在新 goroutine 中接受 streams
		go func(conn *quic.Conn) {
			for p.ctx.Err() == nil {
				stream, err := conn.AcceptStream(p.ctx)
				if err != nil {
					return
				}
				go p.handleStream(stream)
			}
		}(conn)
	}
}

// OutgoingGet 根据 ID 获取可用 stream
func (p *Pool) OutgoingGet(id string, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(p.ctx, timeout)
	defer cancel()

	for {
		if stream, ok := p.streams.LoadAndDelete(id); ok {
			<-p.idChan

			conn := p.quicConn.Load()
			if conn == nil {
				return nil, fmt.Errorf("OutgoingGet: QUIC connection not available")
			}

			// 包装为 net.Conn
			streamConn := &StreamConn{
				Stream:     stream.(*quic.Stream),
				conn:       conn,
				localAddr:  conn.LocalAddr(),
				remoteAddr: conn.RemoteAddr(),
			}
			return streamConn, nil
		}

		select {
		case <-time.After(idRetryInterval):
		case <-ctx.Done():
			return nil, fmt.Errorf("OutgoingGet: stream not found")
		}
	}
}

// IncomingGet 获取可用 stream 返回 ID
func (p *Pool) IncomingGet(timeout time.Duration) (string, net.Conn, error) {
	ctx, cancel := context.WithTimeout(p.ctx, timeout)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return "", nil, fmt.Errorf("IncomingGet: insufficient streams")
		case id := <-p.idChan:
			if stream, ok := p.streams.LoadAndDelete(id); ok {
				conn := p.quicConn.Load()
				if conn == nil {
					continue
				}

				// 包装为 net.Conn
				streamConn := &StreamConn{
					Stream:     stream.(*quic.Stream),
					conn:       conn,
					localAddr:  conn.LocalAddr(),
					remoteAddr: conn.RemoteAddr(),
				}
				return id, streamConn, nil
			}
			continue
		}
	}
}

// Flush 清空池中的所有 streams
func (p *Pool) Flush() {
	var wg sync.WaitGroup
	p.streams.Range(func(key, value any) bool {
		wg.Go(func() {
			if stream, ok := value.(*quic.Stream); ok {
				stream.Close()
			}
		})
		return true
	})
	wg.Wait()

	p.streams = sync.Map{}
	p.idChan = make(chan string, p.maxCap)
}

// Close 关闭连接池并释放资源
func (p *Pool) Close() {
	if p.cancel != nil {
		p.cancel()
	}
	p.Flush()

	if conn := p.quicConn.Swap(nil); conn != nil {
		conn.CloseWithError(0, "pool closed")
	}

	if listener := p.quicListener.Swap(nil); listener != nil {
		listener.Close()
	}
}

// Ready 检查连接池是否已初始化
func (p *Pool) Ready() bool {
	return p.ctx != nil
}

// Active 获取当前活跃 stream 数
func (p *Pool) Active() int {
	return len(p.idChan)
}

// Capacity 获取当前池容量
func (p *Pool) Capacity() int {
	return int(p.capacity.Load())
}

// Interval 获取当前 stream 创建间隔
func (p *Pool) Interval() time.Duration {
	return time.Duration(p.interval.Load())
}

// AddError 增加错误计数
func (p *Pool) AddError() {
	p.errCount.Add(1)
}

// ErrorCount 获取错误计数
func (p *Pool) ErrorCount() int {
	return int(p.errCount.Load())
}

// ResetError 重置错误计数
func (p *Pool) ResetError() {
	p.errCount.Store(0)
}

// adjustInterval 根据池使用情况动态调整 stream 创建间隔
func (p *Pool) adjustInterval() {
	idle := len(p.idChan)
	capacity := int(p.capacity.Load())
	interval := time.Duration(p.interval.Load())

	if idle < int(float64(capacity)*intervalLowThreshold) && interval > p.minIvl {
		newInterval := max(interval-intervalAdjustStep, p.minIvl)
		p.interval.Store(int64(newInterval))
	}

	if idle > int(float64(capacity)*intervalHighThreshold) && interval < p.maxIvl {
		newInterval := min(interval+intervalAdjustStep, p.maxIvl)
		p.interval.Store(int64(newInterval))
	}
}

// adjustCapacity 根据创建成功率动态调整池容量
func (p *Pool) adjustCapacity(created int) {
	capacity := int(p.capacity.Load())
	ratio := float64(created) / float64(capacity)

	if ratio < capacityAdjustLowRatio && capacity > p.minCap {
		p.capacity.Add(-1)
	}

	if ratio > capacityAdjustHighRatio && capacity < p.maxCap {
		p.capacity.Add(1)
	}
}
