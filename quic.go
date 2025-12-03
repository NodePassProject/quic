// Package quic 实现了基于QUIC协议的高性能、可靠的网络连接池管理系统
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
	reconnectRetryInterval  = 500 * time.Millisecond
	intervalAdjustStep      = 100 * time.Millisecond
	capacityAdjustLowRatio  = 0.2
	capacityAdjustHighRatio = 0.8
	intervalLowThreshold    = 0.2
	intervalHighThreshold   = 0.8
	defaultALPN             = "np-quic"
	defaultStreamsPerConn   = 128
	minConnsPerPool         = 1
	maxConnsPerPool         = 64
)

// Shard 连接分片，封装单个QUIC连接及其流管理
type Shard struct {
	streams    sync.Map                  // 存储流的映射表
	idChan     chan string               // 可用流ID通道
	first      atomic.Bool               // 首次标志
	quicConn   atomic.Pointer[quic.Conn] // QUIC连接
	listenAddr atomic.Pointer[net.Addr]  // 监听器地址（服务端）
	index      int                       // 分片索引
	maxStreams int                       // 此分片的最大流数
}

// Pool QUIC连接池结构体，用于管理QUIC流
type Pool struct {
	shards         []*Shard                      // 连接分片切片
	numShards      int                           // 分片数量
	idChan         chan string                   // 全局可用流ID通道
	tlsCode        string                        // TLS安全模式代码
	hostname       string                        // 主机名
	clientIP       string                        // 客户端IP
	tlsConfig      *tls.Config                   // TLS配置
	addrResolver   func() (string, error)        // 地址解析器
	listenAddr     string                        // 监听地址
	sharedListener atomic.Pointer[quic.Listener] // 共享监听器（服务端）
	errCount       atomic.Int32                  // 错误计数
	capacity       atomic.Int32                  // 当前容量
	minCap         int                           // 最小容量
	maxCap         int                           // 最大容量
	interval       atomic.Int64                  // 流创建间隔
	minIvl         time.Duration                 // 最小间隔
	maxIvl         time.Duration                 // 最大间隔
	keepAlive      time.Duration                 // 保活间隔
	ctx            context.Context               // 上下文
	cancel         context.CancelFunc            // 取消函数
}

// StreamConn 将QUIC流包装为接口
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
	if err := s.Stream.SetReadDeadline(t); err != nil {
		return err
	}
	return s.Stream.SetWriteDeadline(t)
}

// ConnectionState 返回TLS状态
func (s *StreamConn) ConnectionState() tls.ConnectionState {
	return s.conn.ConnectionState().TLS
}

// validateCapacity 验证并规范化容量参数
func validateCapacity(minCap, maxCap int) (int, int) {
	if minCap <= 0 {
		minCap = defaultMinCap
	}
	if maxCap <= 0 {
		maxCap = defaultMaxCap
	}
	if minCap > maxCap {
		minCap, maxCap = maxCap, minCap
	}
	return minCap, maxCap
}

// validateInterval 验证并规范化间隔参数
func validateInterval(minIvl, maxIvl time.Duration) (time.Duration, time.Duration) {
	if minIvl <= 0 {
		minIvl = defaultMinIvl
	}
	if maxIvl <= 0 {
		maxIvl = defaultMaxIvl
	}
	if minIvl > maxIvl {
		minIvl, maxIvl = maxIvl, minIvl
	}
	return minIvl, maxIvl
}

// calculateShardCount 计算所需的分片数量
func calculateShardCount(maxCap int) int {
	numShards := min(max((maxCap+defaultStreamsPerConn-1)/defaultStreamsPerConn, minConnsPerPool), maxConnsPerPool)
	return numShards
}

// createShards 创建并初始化分片切片
func createShards(numShards int) []*Shard {
	shards := make([]*Shard, numShards)
	for i := range shards {
		shards[i] = &Shard{
			streams:    sync.Map{},
			idChan:     make(chan string, defaultStreamsPerConn),
			index:      i,
			maxStreams: defaultStreamsPerConn,
		}
	}
	return shards
}

// buildTLSConfig 构建TLS配置
func buildTLSConfig(tlsCode, hostname string) *tls.Config {
	var tlsConfig *tls.Config
	switch tlsCode {
	case "0", "1":
		// 使用自签名证书（不验证）
		tlsConfig = &tls.Config{
			InsecureSkipVerify: true,
			NextProtos:         []string{defaultALPN},
			MinVersion:         tls.VersionTLS13,
		}
	case "2":
		// 使用验证证书（安全模式）
		tlsConfig = &tls.Config{
			InsecureSkipVerify: false,
			ServerName:         hostname,
			NextProtos:         []string{defaultALPN},
			MinVersion:         tls.VersionTLS13,
		}
	}
	return tlsConfig
}

// buildQUICConfig 构建QUIC配置
func buildQUICConfig(keepAlive time.Duration) *quic.Config {
	return &quic.Config{
		KeepAlivePeriod:    keepAlive,
		MaxIdleTimeout:     keepAlive * 3,
		MaxIncomingStreams: int64(defaultStreamsPerConn),
	}
}

// createStreamConn 从分片中创建StreamConn
func createStreamConn(shard *Shard, stream any) (*StreamConn, bool) {
	conn := shard.quicConn.Load()
	if conn == nil {
		return nil, false
	}

	localAddr := conn.LocalAddr()
	if listenAddrPtr := shard.listenAddr.Load(); listenAddrPtr != nil {
		localAddr = *listenAddrPtr
	}

	return &StreamConn{
		Stream:     stream.(*quic.Stream),
		conn:       conn,
		localAddr:  localAddr,
		remoteAddr: conn.RemoteAddr(),
	}, true
}

// findStreamInShards 在所有分片中查找并删除流
func (p *Pool) findStreamInShards(id string, consumeShardChan bool) (*StreamConn, bool) {
	for _, shard := range p.shards {
		if stream, ok := shard.streams.LoadAndDelete(id); ok {
			if consumeShardChan {
				<-shard.idChan
			}
			if streamConn, ok := createStreamConn(shard, stream); ok {
				return streamConn, true
			}
		}
	}
	return nil, false
}

// flushShard 清空分片中的所有流
func (s *Shard) flushShard() {
	s.streams.Range(func(key, value any) bool {
		if stream, ok := value.(*quic.Stream); ok {
			stream.Close()
		}
		return true
	})
	s.streams = sync.Map{}
	s.idChan = make(chan string, s.maxStreams)
}

// closeShard 关闭分片的连接
func (s *Shard) closeShard() {
	if conn := s.quicConn.Swap(nil); conn != nil {
		conn.CloseWithError(0, "pool closed")
	}
}

// NewClientPool 创建新的客户端QUIC池
func NewClientPool(
	minCap, maxCap int,
	minIvl, maxIvl time.Duration,
	keepAlive time.Duration,
	tlsCode string,
	hostname string,
	addrResolver func() (string, error),
) *Pool {
	minCap, maxCap = validateCapacity(minCap, maxCap)
	minIvl, maxIvl = validateInterval(minIvl, maxIvl)
	numShards := calculateShardCount(maxCap)
	shards := createShards(numShards)

	pool := &Pool{
		shards:       shards,
		numShards:    numShards,
		idChan:       make(chan string, maxCap),
		tlsCode:      tlsCode,
		hostname:     hostname,
		addrResolver: addrResolver,
		minCap:       minCap,
		maxCap:       maxCap,
		minIvl:       minIvl,
		maxIvl:       maxIvl,
		keepAlive:    keepAlive,
	}
	pool.capacity.Store(int32(minCap))
	pool.interval.Store(int64(minIvl))
	pool.ctx, pool.cancel = context.WithCancel(context.Background())
	return pool
}

// NewServerPool 创建新的服务端QUIC池
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

	numShards := calculateShardCount(maxCap)
	shards := createShards(numShards)

	pool := &Pool{
		shards:     shards,
		numShards:  numShards,
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

// createStream 在指定分片上创建新的客户端流
func (s *Shard) createStream(ctx context.Context, globalChan chan string) bool {
	conn := s.quicConn.Load()
	if conn == nil {
		return false
	}

	// 打开新的流
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		s.quicConn.Store(nil)
		return false
	}

	// 发送握手字节
	if _, err := stream.Write([]byte{0x00}); err != nil {
		stream.Close()
		s.quicConn.Store(nil)
		return false
	}

	var id string

	// 接收流ID
	stream.SetReadDeadline(time.Now().Add(idReadTimeout))
	buf := make([]byte, 4)
	n, err := io.ReadFull(stream, buf)
	if err != nil || n != 4 {
		stream.Close()
		s.quicConn.Store(nil)
		return false
	}
	id = hex.EncodeToString(buf)
	stream.SetReadDeadline(time.Time{})

	// 建立映射并存入分片通道和全局通道
	s.streams.Store(id, stream)
	select {
	case s.idChan <- id:
		// 同时尝试放入全局通道
		select {
		case globalChan <- id:
			return true
		default:
			// 全局通道满
			<-s.idChan
			s.streams.Delete(id)
			stream.Close()
			return false
		}
	default:
		s.streams.Delete(id)
		stream.Close()
		return false
	}
}

// handleStream 处理新的服务端流
func (s *Shard) handleStream(stream *quic.Stream, globalChan chan string, maxCap int, globalActive func() int) {
	var streamClosed bool
	defer func() {
		if !streamClosed {
			(*stream).Close()
		}
	}()

	// 检查全局池是否已满
	if globalActive() >= maxCap {
		return
	}

	// 读取握手字节
	handshake := make([]byte, 1)
	if _, err := (*stream).Read(handshake); err != nil {
		return
	}

	// 生成流ID
	rawID, id, err := s.generateID()
	if err != nil {
		return
	}

	// 防止重复流ID
	if _, exist := s.streams.Load(id); exist {
		return
	}

	// 发送流ID给客户端
	if _, err := (*stream).Write(rawID); err != nil {
		return
	}

	// 尝试放入分片通道和全局通道
	select {
	case s.idChan <- id:
		select {
		case globalChan <- id:
			s.streams.Store(id, stream)
			streamClosed = true
		default:
			<-s.idChan
			return
		}
	default:
		return
	}
}

// establishConnection 为分片建立QUIC连接
func (s *Shard) establishConnection(ctx context.Context, addrResolver func() (string, error), tlsCode, hostname string, keepAlive time.Duration) error {
	conn := s.quicConn.Load()
	if conn != nil {
		if conn.Context().Err() == nil {
			return nil
		}
		s.quicConn.Store(nil)
	}

	targetAddr, err := addrResolver()
	if err != nil {
		return fmt.Errorf("establishConnection: address resolution failed: %w", err)
	}

	tlsConfig := buildTLSConfig(tlsCode, hostname)
	quicConfig := buildQUICConfig(keepAlive)

	newConn, err := quic.DialAddr(ctx, targetAddr, tlsConfig, quicConfig)
	if err != nil {
		return err
	}

	s.quicConn.Store(newConn)
	return nil
}

// startSharedListener 启动共享监听器（服务端）
func (p *Pool) startSharedListener() error {
	if p.sharedListener.Load() != nil {
		return nil
	}
	if p.tlsConfig == nil {
		return fmt.Errorf("startSharedListener: server mode requires TLS config")
	}

	clonedTLS := p.tlsConfig.Clone()
	clonedTLS.NextProtos = []string{defaultALPN}
	clonedTLS.MinVersion = tls.VersionTLS13

	quicConfig := buildQUICConfig(p.keepAlive)
	listener, err := quic.ListenAddr(p.listenAddr, clonedTLS, quicConfig)
	if err != nil {
		return fmt.Errorf("startSharedListener: %w", err)
	}

	p.sharedListener.Store(listener)

	// 保存监听器地址到所有分片
	addr := listener.Addr()
	for _, shard := range p.shards {
		shard.listenAddr.Store(&addr)
	}

	return nil
}

// acceptAndDistribute 接受连接并轮询分配到分片
func (p *Pool) acceptAndDistribute() {
	var shardIndex int32 = -1

	for p.ctx.Err() == nil {
		listener := p.sharedListener.Load()
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

		// 验证客户端IP
		if p.clientIP != "" {
			remoteAddr := conn.RemoteAddr().(*net.UDPAddr)
			if remoteAddr.IP.String() != p.clientIP {
				conn.CloseWithError(0, "unauthorized IP")
				continue
			}
		}

		// 轮询分配到分片
		idx := int(atomic.AddInt32(&shardIndex, 1)) % p.numShards
		shard := p.shards[idx]
		shard.quicConn.Store(conn)

		// 为该连接启动流接受协程
		go p.acceptStreams(shard, conn)
	}
}

// acceptStreams 为单个连接接受流
func (p *Pool) acceptStreams(shard *Shard, conn *quic.Conn) {
	for p.ctx.Err() == nil {
		stream, err := (*conn).AcceptStream(p.ctx)
		if err != nil {
			return
		}
		go shard.handleStream(stream, p.idChan, p.maxCap, p.Active)
	}
}

// ClientManager 客户端QUIC池管理器
func (p *Pool) ClientManager() {
	if p.cancel != nil {
		p.cancel()
	}
	p.ctx, p.cancel = context.WithCancel(context.Background())

	var wg sync.WaitGroup
	for i := range p.shards {
		wg.Add(1)
		go func(shard *Shard) {
			defer wg.Done()
			p.manageShardClient(shard)
		}(p.shards[i])
	}
	wg.Wait()
}

// manageShardClient 管理单个分片的客户端流创建
func (p *Pool) manageShardClient(shard *Shard) {
	for p.ctx.Err() == nil {
		// 确保分片连接已建立
		if err := shard.establishConnection(p.ctx, p.addrResolver, p.tlsCode, p.hostname, p.keepAlive); err != nil {
			select {
			case <-p.ctx.Done():
				return
			case <-time.After(reconnectRetryInterval):
			}
			continue
		}

		p.adjustInterval()
		capacity := int(p.capacity.Load())
		// 计算分片所需容量并创建流
		shardCapacity := (capacity + p.numShards - 1) / p.numShards
		need := shardCapacity - len(shard.idChan)
		created := 0

		if need > 0 {
			var shardWg sync.WaitGroup
			results := make(chan int, need)
			for range need {
				shardWg.Go(func() {
					if shard.createStream(p.ctx, p.idChan) {
						results <- 1
					}
				})
			}
			shardWg.Wait()
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

// ServerManager 服务端QUIC池管理器
func (p *Pool) ServerManager() {
	if p.cancel != nil {
		p.cancel()
	}
	p.ctx, p.cancel = context.WithCancel(context.Background())

	// 启动共享监听器
	if err := p.startSharedListener(); err != nil {
		return
	}

	// 启动连接分配器
	go p.acceptAndDistribute()

	// 等待上下文取消
	<-p.ctx.Done()
}

// OutgoingGet 根据ID获取可用流
func (p *Pool) OutgoingGet(id string, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(p.ctx, timeout)
	defer cancel()

	for {
		if streamConn, ok := p.findStreamInShards(id, true); ok {
			<-p.idChan
			return streamConn, nil
		}

		select {
		case <-time.After(idRetryInterval):
		case <-ctx.Done():
			return nil, fmt.Errorf("OutgoingGet: stream not found")
		}
	}
}

// IncomingGet 获取可用流并返回ID
func (p *Pool) IncomingGet(timeout time.Duration) (string, net.Conn, error) {
	ctx, cancel := context.WithTimeout(p.ctx, timeout)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return "", nil, fmt.Errorf("IncomingGet: insufficient streams")
		case id := <-p.idChan:
			if streamConn, ok := p.findStreamInShards(id, true); ok {
				return id, streamConn, nil
			}
			continue
		}
	}
}

// Flush 清空池中的所有流
func (p *Pool) Flush() {
	var wg sync.WaitGroup
	for _, shard := range p.shards {
		wg.Add(1)
		go func(s *Shard) {
			defer wg.Done()
			s.flushShard()
		}(shard)
	}
	wg.Wait()

	// 重建全局通道
	p.idChan = make(chan string, p.maxCap)
}

// Close 关闭连接池并释放资源
func (p *Pool) Close() {
	if p.cancel != nil {
		p.cancel()
	}
	p.Flush()

	// 关闭共享监听器
	if listener := p.sharedListener.Swap(nil); listener != nil {
		listener.Close()
	}

	// 并行关闭所有分片的连接
	var wg sync.WaitGroup
	for _, shard := range p.shards {
		wg.Add(1)
		go func(s *Shard) {
			defer wg.Done()
			s.closeShard()
		}(shard)
	}
	wg.Wait()
}

// Ready 检查连接池是否已初始化
func (p *Pool) Ready() bool {
	return p.ctx != nil
}

// Active 获取当前活跃流数
func (p *Pool) Active() int {
	return len(p.idChan)
}

// ShardCount 获取连接分片数量
func (p *Pool) ShardCount() int {
	return p.numShards
}

// Capacity 获取当前池容量
func (p *Pool) Capacity() int {
	return int(p.capacity.Load())
}

// Interval 获取当前流创建间隔
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

// adjustInterval 根据池使用情况动态调整流创建间隔
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

// generateID 生成唯一流ID
func (s *Shard) generateID() ([]byte, string, error) {
	if s.first.CompareAndSwap(false, true) {
		return []byte{0, 0, 0, 0}, "00000000", nil
	}

	rawID := make([]byte, 4)
	if _, err := rand.Read(rawID); err != nil {
		return nil, "", err
	}
	id := hex.EncodeToString(rawID)
	return rawID, id, nil
}
