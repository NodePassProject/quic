# QUIC Package

[![Go Reference](https://pkg.go.dev/badge/github.com/NodePassProject/quic.svg)](https://pkg.go.dev/github.com/NodePassProject/quic)
[![License](https://img.shields.io/badge/License-BSD_3--Clause-blue.svg)](https://opensource.org/licenses/BSD-3-Clause)

A high-performance, reliable QUIC stream pool management system for Go applications.

## Table of Contents

- [Features](#features)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [Usage](#usage)
  - [Client Stream Pool](#client-stream-pool)
  - [Server Stream Pool](#server-stream-pool)
  - [Managing Pool Health](#managing-pool-health)
- [Security Features](#security-features)
  - [Client IP Restriction](#client-ip-restriction)
  - [TLS Security Modes](#tls-security-modes)
- [Stream Multiplexing](#stream-multiplexing)
- [Dynamic Adjustment](#dynamic-adjustment)
- [Advanced Usage](#advanced-usage)
- [Performance Considerations](#performance-considerations)
- [Troubleshooting](#troubleshooting)
- [Best Practices](#best-practices)
  - [Pool Configuration](#1-pool-configuration)
  - [Stream Management](#2-stream-management)
  - [Error Handling and Monitoring](#3-error-handling-and-monitoring)
  - [Production Deployment](#4-production-deployment)
  - [Performance Optimization](#5-performance-optimization)
  - [Testing and Development](#6-testing-and-development)
- [License](#license)

## Features

- **Lock-free design** using atomic operations for maximum performance
- **Thread-safe stream management** with `sync.Map` and atomic pointers
- **Support for both client and server stream pools**
- **Dynamic capacity adjustment** based on usage patterns
- **Automatic stream health monitoring**
- **QUIC connection multiplexing** for efficient resource usage
- **Multiple TLS security modes** (InsecureSkipVerify, self-signed, verified)
- **Stream identification and tracking**
- **Graceful error handling and recovery**
- **Configurable stream creation intervals**
- **Auto-reconnection** on connection failures
- **Built-in keep-alive management**
- **Zero lock contention** for high concurrency scenarios

## Installation

```bash
go get github.com/NodePassProject/quic
```

## Quick Start

Here's a minimal example to get you started:

```go
package main

import (
    "time"
    "github.com/NodePassProject/quic"
)

func main() {
    // Create a client pool
    clientPool := quic.NewClientPool(
        5, 20,                              // min/max capacity
        500*time.Millisecond, 5*time.Second, // min/max intervals
        30*time.Second,                     // keep-alive period
        "0",                                // TLS mode
        "example.com",                      // hostname
        "example.com:4433",                 // target QUIC address
    )
    
    // Start the pool manager
    go clientPool.ClientManager()
    
    // Get a stream from the pool by ID
    stream, err := clientPool.OutgoingGet("stream-id", 10*time.Second)
    if err != nil {
        // Handle error...
        return
    }
    if stream != nil {
        // Use stream...
        stream.Close()
    }
    
    // Clean up
    defer clientPool.Close()
}
```

## Usage

### Client Stream Pool

```go
package main

import (
    "time"
    "github.com/NodePassProject/quic"
)

func main() {
    // Create a new client pool with:
    // - Minimum capacity: 5 streams
    // - Maximum capacity: 20 streams
    // - Minimum interval: 500ms between stream creation attempts
    // - Maximum interval: 5s between stream creation attempts
    // - Keep-alive period: 30s for connection health monitoring
    // - TLS mode: "2" (verified certificates)
    // - Hostname for certificate verification: "example.com"
    // - Target QUIC address: "example.com:4433"
    clientPool := quic.NewClientPool(
        5, 20,
        500*time.Millisecond, 5*time.Second,
        30*time.Second,
        "2",
        "example.com",
        "example.com:4433",
    )
    
    // Start the client manager (usually in a goroutine)
    go clientPool.ClientManager()
    
    // Get a stream by ID with timeout (usually received from the server)
    timeout := 10 * time.Second
    stream, err := clientPool.OutgoingGet("stream-id", timeout)
    if err != nil {
        // Handle error...
        return
    }
    
    // Use the stream...
    
    // When finished with the pool
    clientPool.Close()
}
```

**Note:** `OutgoingGet` takes a stream ID and timeout duration, and returns `(net.Conn, error)`. 
The error indicates if the stream with the specified ID was not found or if the timeout was exceeded.

### Server Stream Pool

### Server Stream Pool

```go
package main

import (
    "crypto/tls"
    "log"
    "time"
    "github.com/NodePassProject/quic"
)

func main() {
    // Create TLS config
    tlsConfig := &tls.Config{
        Certificates: []tls.Certificate{cert},
        MinVersion:   tls.VersionTLS13,
    }
    
    // Create a new server pool
    // - Maximum capacity: 20 streams
    // - Restrict to specific client IP (optional, "" for any IP)
    // - Use TLS config
    // - Listen on address: "0.0.0.0:4433"
    // - Keep-alive period: 30s for connection health monitoring
    serverPool := quic.NewServerPool(
        20,                    // maxCap
        "192.168.1.10",       // clientIP (use "" to allow any IP)
        tlsConfig,            // TLS config
        "0.0.0.0:4433",       // listen address
        30*time.Second,       // keepAlive
    )
    
    // Start the server manager (usually in a goroutine)
    go serverPool.ServerManager()
    
    // Get a new stream from the pool (blocks until available)
    timeout := 30 * time.Second
    id, stream, err := serverPool.IncomingGet(timeout)
    if err != nil {
        // Handle error (timeout or pool closed)
        log.Printf("Failed to get stream: %v", err)
        return
    }
    
    // Use the stream...
    
    // When finished with the pool
    serverPool.Close()
}
```

**Note:** `IncomingGet` takes a timeout duration and returns `(string, net.Conn, error)`. The return values are:
- `string`: The stream ID generated by the server
- `net.Conn`: The stream object (wrapped as net.Conn)
- `error`: Can indicate timeout, context cancellation, or other pool-related errors

### Managing Pool Health

```go
// Get a stream from client pool by ID with timeout
timeout := 10 * time.Second
stream, err := clientPool.OutgoingGet("stream-id", timeout)
if err != nil {
    // Stream with the specified ID not found or timeout exceeded
    log.Printf("Stream not found: %v", err)
}

// Get a stream from server pool with timeout
timeout := 30 * time.Second
id, stream, err := serverPool.IncomingGet(timeout)
if err != nil {
    // Handle various error cases:
    // - Timeout: no stream available within the specified time
    // - Context cancellation: pool is being closed
    // - Other pool errors
    log.Printf("Failed to get stream: %v", err)
}

// Check if the pool is ready
if clientPool.Ready() {
    // The pool is initialized and ready for use
}

// Get current active stream count (streams available in the pool)
activeStreams := clientPool.Active()

// Get current capacity setting
capacity := clientPool.Capacity()

// Get current stream creation interval
interval := clientPool.Interval()

// Manually flush all streams (rarely needed)
clientPool.Flush()

// Record an error (increases internal error counter)
clientPool.AddError()

// Get the current error count
errorCount := clientPool.ErrorCount()

// Reset the error count to zero
clientPool.ResetError()
```

## Security Features

### Client IP Restriction

The `NewServerPool` function allows you to restrict incoming connections to a specific client IP address. The function signature is:

```go
func NewServerPool(
    maxCap int,
    clientIP string,
    tlsConfig *tls.Config,
    listenAddr string,
    keepAlive time.Duration,
) *Pool
```

- `maxCap`: Maximum pool capacity.
- `clientIP`: Restrict allowed client IP ("" for any).
- `tlsConfig`: TLS configuration (required for QUIC).
- `listenAddr`: QUIC listen address (host:port).
- `keepAlive`: Keep-alive period.

When the `clientIP` parameter is set:
- All connections from other IP addresses will be immediately closed.
- This provides an additional layer of security beyond network firewalls.
- Particularly useful for internal services or dedicated client-server applications.

To allow connections from any IP address, use an empty string:

```go
// Create a server pool that accepts connections from any IP
serverPool := quic.NewServerPool(20, "", tlsConfig, "0.0.0.0:4433", 30*time.Second)
```

### TLS Security Modes

| Mode | Description | Security Level | Use Case |
|------|-------------|----------------|----------|
| `"0"` | InsecureSkipVerify | Low | Internal networks, testing |
| `"1"` | Self-signed certificates | Medium | Development, testing environments |
| `"2"` | Verified certificates | High | Production, public networks |

**Note:** QUIC protocol requires TLS encryption. Mode `"0"` uses InsecureSkipVerify but still encrypts the connection.

#### Example Usage

```go
// InsecureSkipVerify - testing only
clientPool := quic.NewClientPool(5, 20, minIvl, maxIvl, keepAlive, "0", "example.com", "example.com:4433")

// Self-signed TLS - development/testing
clientPool := quic.NewClientPool(5, 20, minIvl, maxIvl, keepAlive, "1", "example.com", "example.com:4433")

// Verified TLS - production
clientPool := quic.NewClientPool(5, 20, minIvl, maxIvl, keepAlive, "2", "example.com", "example.com:4433")
```

---

**Implementation Details:**

- **Stream ID Generation:**
  - The server generates a 4-byte ID and sends it to the client after stream creation.
  - Stream IDs are used for tracking and managing individual streams.

- **OutgoingGet Method:**
  - For client pools: Returns `(net.Conn, error)` after retrieving a stream by ID.
  - Takes timeout parameter to wait for stream availability.

- **IncomingGet Method:**
  - For server pools: Returns `(string, net.Conn, error)` to get an available stream with its ID.
  - Takes timeout parameter to wait for stream availability.
  - Returns the stream ID and stream object for further use.

- **Flush/Close:**
  - `Flush` closes all streams and resets the pool.
  - `Close` cancels the context, closes QUIC connection, and flushes the pool.

- **Dynamic Adjustment:**
  - `adjustInterval` and `adjustCapacity` are used internally for pool optimization based on usage and success rate.

- **Error Handling:**
  - `AddError` and `ErrorCount` are thread-safe using atomic operations.

## Stream Multiplexing

QUIC provides native stream multiplexing, allowing multiple logical streams over a single UDP connection:

### Multiplexing Features

- **Single Connection**: One QUIC connection handles multiple concurrent streams
- **Independent Streams**: Each stream is isolated with its own flow control
- **Efficient Resource Usage**: Reduced overhead compared to multiple TCP connections
- **Automatic Management**: Pool handles stream creation and lifecycle
- **Built-in Keep-Alive**: QUIC's connection-level keep-alive maintains the underlying connection

### Usage Examples

```go
// Client pool - single QUIC connection, multiple streams
clientPool := quic.NewClientPool(
    5, 20,
    500*time.Millisecond, 5*time.Second,
    30*time.Second,  // Keep-alive period for QUIC connection
    "2",             // TLS mode
    "example.com",   // hostname
    "example.com:4433", // target address
)

// Server pool - accepts one client connection, multiple streams
serverPool := quic.NewServerPool(
    20,
    "192.168.1.10",
    tlsConfig,
    "0.0.0.0:4433",
    30*time.Second,  // Keep-alive period
)
```

### Multiplexing Best Practices

| Stream Count | Use Case | Pros | Cons |
|--------------|----------|------|------|
| 5-20 | Standard applications | Balanced resource usage | May hit limits under high load |
| 20-50 | High-concurrency services | Good throughput | Higher memory usage |
| 50-100 | Very high-traffic | Maximum concurrency | Requires careful tuning |

**Recommendations:**
- **Web applications**: 10-30 streams per connection
- **API services**: 20-50 streams per connection
- **Real-time systems**: 5-20 streams with fast creation intervals
- **Batch processing**: 5-10 streams with longer intervals

## Dynamic Adjustment

The pool automatically adjusts:

- Stream creation intervals based on idle stream count (using `adjustInterval` method)
  - Decreases interval when pool is under-utilized (< 20% idle streams)
  - Increases interval when pool is over-utilized (> 80% idle streams)
  
- Stream capacity based on stream creation success rate (using `adjustCapacity` method)
  - Decreases capacity when success rate is low (< 20%)
  - Increases capacity when success rate is high (> 80%)

These adjustments ensure optimal resource usage:

```go
// Check current capacity and interval settings
currentCapacity := clientPool.Capacity()
currentInterval := clientPool.Interval()
```

## Advanced Usage

### Custom Error Handling

```go
package main

import (
    "log"
    "time"
    "github.com/NodePassProject/quic"
)

func main() {
    clientPool := quic.NewClientPool(
        5, 20,
        500*time.Millisecond, 5*time.Second,
        30*time.Second,
        "2",
        "example.com",
        "example.com:4433",
    )
    
    go clientPool.ClientManager()
    
    // Track errors separately
    go func() {
        ticker := time.NewTicker(30 * time.Second)
        defer ticker.Stop()
        for range ticker.C {
            errorCount := clientPool.ErrorCount()
            if errorCount > 0 {
                log.Printf("Pool error count: %d", errorCount)
                clientPool.ResetError()
            }
        }
    }()
    
    // Your application logic...
}
```

### Working with Context

```go
package main

import (
    "context"
    "time"
    "github.com/NodePassProject/quic"
)

func main() {
    // Create a context that can be cancelled
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    
    clientPool := quic.NewClientPool(
        5, 20,
        500*time.Millisecond, 5*time.Second,
        30*time.Second,
        "2",
        "example.com",
        "example.com:4433",
    )
    
    go clientPool.ClientManager()
    
    // When needed to stop the pool:
    // cancel()
    // clientPool.Close()
}
```

### Load Balancing with Multiple Pools

```go
package main

import (
    "sync/atomic"
    "time"
    "github.com/NodePassProject/quic"
)

func main() {
    // Create pools for different servers
    serverAddresses := []string{
        "server1.example.com:4433",
        "server2.example.com:4433",
        "server3.example.com:4433",
    }
    
    pools := make([]*quic.Pool, len(serverAddresses))
    for i, addr := range serverAddresses {
        hostname := "server" + string(rune(i+49)) + ".example.com"
        pools[i] = quic.NewClientPool(
            5, 20,
            500*time.Millisecond, 5*time.Second,
            30*time.Second,
            "2",
            hostname,
            addr,
        )
        go pools[i].ClientManager()
    }
    
    // Simple round-robin load balancer
    var counter int32 = 0
    getNextPool := func() *quic.Pool {
        next := atomic.AddInt32(&counter, 1) % int32(len(pools))
        return pools[next]
    }
    
    // Usage
    id, stream, err := getNextPool().IncomingGet(30 * time.Second)
    if err != nil {
        // Handle error...
        return
    }
    
    // Use stream...
    
    // When done with all pools
    for _, p := range pools {
        p.Close()
    }
}
```

## Performance Considerations

### Lock-Free Architecture

This package uses a completely **lock-free design** for maximum concurrency:

| Component | Implementation | Benefit |
|-----------|----------------|---------|
| **Stream Storage** | `sync.Map` | Lock-free concurrent access |
| **Connection Pointers** | `atomic.Pointer[quic.Conn]` | Wait-free reads/writes |
| **Counters** | `atomic.Int32` / `atomic.Int64` | Lock-free increments |
| **ID Channel** | Buffered `chan string` | Native Go concurrency |

**Performance Impact:**
- Zero lock contention in high-concurrency scenarios
- No context switching overhead from mutex waits
- Scales linearly with CPU cores
- Consistent sub-microsecond operation latency

### Stream Pool Sizing

| Pool Size | Pros | Cons | Best For |
|-----------|------|------|----------|
| Too Small (< 5) | Low resource usage | Stream contention, delays | Low-traffic applications |
| Optimal (5-50) | Balanced performance | Requires monitoring | Most applications |
| Too Large (> 100) | No contention | Resource waste, connection overhead | Very high-traffic services |

**Sizing Guidelines:**
- Start with `minCap = baseline_load` and `maxCap = peak_load × 1.5`
- Monitor stream usage with `pool.Active()` and `pool.Capacity()`
- Adjust based on observed patterns

### QUIC Performance Impact

| Aspect | QUIC | TCP |
|--------|------|-----|
| **Handshake Time** | ~50-100ms (1-RTT) | ~100-200ms (3-way + TLS) |
| **Head-of-Line Blocking** | None (stream-level) | Yes (connection-level) |
| **Connection Migration** | Supported | Not supported |
| **Multiplexing Overhead** | Low (native) | High (needs application layer) |
| **Throughput** | ~90% of TCP | Baseline |

### Stream Creation Overhead

Stream creation in QUIC is lightweight:
- **Cost**: ~1-2ms per stream creation
- **Frequency**: Controlled by pool intervals
- **Trade-off**: Fast creation vs. resource usage

For ultra-high-throughput systems, consider pre-creating streams during idle periods.

## Troubleshooting

### Common Issues

#### 1. Connection Timeout
**Symptoms:** QUIC connection fails to establish  
**Solutions:**
- Check network connectivity to target host
- Verify server address and port are correct
- Ensure UDP port is not blocked by firewall
- Check for NAT/firewall UDP timeout issues

#### 2. TLS Handshake Failure
**Symptoms:** QUIC connections fail with certificate errors  
**Solutions:**
- Verify certificate validity and expiration
- Check hostname matches certificate Common Name
- Ensure TLS 1.3 is supported
- For testing, temporarily use TLS mode `"1"` (self-signed)

#### 3. Pool Exhaustion
**Symptoms:** `IncomingGet()` returns an error or times out  
**Solutions:**
- Check QUIC connection status
- Increase maximum capacity
- Reduce stream hold time in application code
- Check for stream leaks (ensure streams are properly closed)
- Monitor with `pool.Active()` and `pool.ErrorCount()`
- Use appropriate timeout values with `IncomingGet(timeout)`

#### 4. High Error Rate
**Symptoms:** Frequent stream creation failures  
**Solutions:**
- Check QUIC connection stability
- Monitor network packet loss
- Verify server is accepting connections
- Track errors with `pool.AddError()` and `pool.ErrorCount()`

### Debugging Checklist

- [ ] **Network connectivity**: Can you reach the target host?
- [ ] **UDP port**: Is the QUIC port open and not blocked?
- [ ] **Certificate validity**: For TLS, are certificates valid and not expired?
- [ ] **Pool capacity**: Is `maxCap` sufficient for your load?
- [ ] **Stream leaks**: Are you properly closing streams?
- [ ] **Error monitoring**: Are you tracking `pool.ErrorCount()`?
- [ ] **Firewall/NAT**: Are there UDP-specific restrictions?

### Debug Logging

Add logging at key points for better debugging:

```go
// Log successful stream creation
id, stream, err := serverPool.IncomingGet(30 * time.Second)
if err != nil {
    log.Printf("Stream creation failed: %v", err)
    serverPool.AddError()
} else {
    log.Printf("Stream created successfully: %s", id)
}
```

## Best Practices

### 1. Pool Configuration

#### Capacity Sizing
```go
// For most applications, start with these guidelines:
minCap := expectedConcurrentStreams
maxCap := peakConcurrentStreams * 1.5

// Example for a web service handling 100 concurrent requests
clientPool := quic.NewClientPool(
    100, 150,                           // min/max capacity based on load
    500*time.Millisecond, 2*time.Second, // stream creation intervals
    30*time.Second,                     // keep-alive
    "2",                                // verified TLS for production
    "api.example.com",                  // hostname
    "api.example.com:4433",             // target address
)
```

#### Interval Configuration
```go
// Aggressive (high-frequency applications)
minInterval := 100 * time.Millisecond
maxInterval := 1 * time.Second

// Balanced (general purpose)
minInterval := 500 * time.Millisecond
maxInterval := 5 * time.Second

// Conservative (low-frequency, batch processing)
minInterval := 2 * time.Second
maxInterval := 10 * time.Second
```

#### Leverage Lock-Free Architecture
```go
// The lock-free design allows safe concurrent access to pool metrics
// No need to worry about mutex contention or race conditions

func monitorPoolMetrics(pool *quic.Pool) {
    ticker := time.NewTicker(1 * time.Second)
    defer ticker.Stop()
    
    for range ticker.C {
        // All these operations are lock-free using atomic primitives
        active := pool.Active()       // Uses len() on channel
        capacity := pool.Capacity()   // atomic.Int32.Load()
        errors := pool.ErrorCount()   // atomic.Int32.Load()
        interval := pool.Interval()   // atomic.Int64.Load()
        
        // Safe to call from multiple goroutines simultaneously
        log.Printf("Pool: %d/%d streams, %d errors, %v interval", 
            active, capacity, errors, interval)
    }
}
```

### 2. Stream Management

#### Always Close Streams
```go
// GOOD: Always close streams
id, stream, err := serverPool.IncomingGet(30 * time.Second)
if err != nil {
    // Handle timeout or other errors
    log.Printf("Failed to get stream: %v", err)
    return err
}
if stream != nil {
    defer stream.Close()  // Close the stream when done
    // Use stream...
}

// BAD: Forgetting to close streams leads to resource leaks
id, stream, _ := serverPool.IncomingGet(30 * time.Second)
// Missing Close() - causes resource leak!
```

#### Handle Timeouts Gracefully
```go
// Use reasonable timeouts for IncomingGet
timeout := 10 * time.Second
id, stream, err := serverPool.IncomingGet(timeout)
if err != nil {
    // Handle timeout or other errors
    log.Printf("Failed to get stream within %v: %v", timeout, err)
    return err
}
if stream == nil {
    // This should not happen if err is nil, but keeping for safety
    log.Printf("Unexpected: got nil stream without error")
    return errors.New("unexpected nil stream")
}
```

### 3. Error Handling and Monitoring

#### Implement Comprehensive Error Tracking
```go
type PoolManager struct {
    pool        *quic.Pool
    metrics     *metrics.Registry
    logger      *log.Logger
}

func (pm *PoolManager) getStreamWithRetry(maxRetries int) (string, net.Conn, error) {
    for i := 0; i < maxRetries; i++ {
        id, stream, err := pm.pool.IncomingGet(5 * time.Second)
        if err == nil && stream != nil {
            return id, stream, nil
        }
        
        // Log and track the error
        pm.logger.Printf("Stream attempt %d failed: %v", i+1, err)
        pm.pool.AddError()
        
        // Exponential backoff
        time.Sleep(time.Duration(math.Pow(2, float64(i))) * time.Second)
    }
    
    return "", nil, errors.New("max retries exceeded")
}

// Monitor pool health periodically
func (pm *PoolManager) healthCheck() {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    
    for range ticker.C {
        active := pm.pool.Active()
        capacity := pm.pool.Capacity()
        errors := pm.pool.ErrorCount()
        
        pm.logger.Printf("Pool health: %d/%d active, %d errors", active, capacity, errors)
        
        // Reset error count periodically
        if errors > 100 {
            pm.pool.ResetError()
        }
        
        // Alert if pool utilization is consistently high
        if float64(active)/float64(capacity) > 0.9 {
            pm.logger.Printf("WARNING: Pool utilization high (%d/%d)", active, capacity)
        }
    }
}
```

### 4. Production Deployment

#### Security Configuration
```go
// Production setup with proper TLS
func createProductionPool() *quic.Pool {
    // Load production certificates
    cert, err := tls.LoadX509KeyPair("cert.pem", "key.pem")
    if err != nil {
        log.Fatal(err)
    }
    
    tlsConfig := &tls.Config{
        Certificates: []tls.Certificate{cert},
        MinVersion:   tls.VersionTLS13,
        NextProtos:   []string{"nodepass-quic"},
    }
    
    return quic.NewServerPool(
        100,                            // Production-scale capacity
        "",                             // Allow any IP (or specify for restriction)
        tlsConfig,
        "0.0.0.0:4433",                // Listen address
        30*time.Second,                // Keep-alive
    )
}

// Client with verified certificates
func createProductionClient() *quic.Pool {
    return quic.NewClientPool(
        20, 100,                         // Production-scale capacity
        500*time.Millisecond, 5*time.Second,
        30*time.Second,
        "2",                            // Always use verified TLS in production
        "secure-api.company.com",       // Proper hostname for certificate verification
        "secure-api.company.com:4433",  // Target address
    )
}
```

#### Graceful Shutdown
```go
func (app *Application) Shutdown(ctx context.Context) error {
    // Stop accepting new requests first
    app.server.Shutdown(ctx)
    
    // Allow existing streams to complete
    select {
    case <-time.After(30 * time.Second):
        app.logger.Println("Forcing pool shutdown after timeout")
    case <-ctx.Done():
    }
    
    // Close all pool streams and QUIC connections
    app.clientPool.Close()
    app.serverPool.Close()
    
    return nil
}
```

### 5. Performance Optimization

#### Avoid Common Anti-patterns
```go
// ANTI-PATTERN: Creating pools repeatedly
func badHandler(w http.ResponseWriter, r *http.Request) {
    // DON'T: Create a new pool for each request
    pool := quic.NewClientPool(5, 10, time.Second, time.Second, 30*time.Second, "2", "api.com", "api.com:4433")
    defer pool.Close()
}

// GOOD PATTERN: Reuse pools
type Server struct {
    quicPool *quic.Pool // Shared pool instance
}

func (s *Server) goodHandler(w http.ResponseWriter, r *http.Request) {
    // DO: Reuse existing pool
    id, stream, err := s.quicPool.IncomingGet(10 * time.Second)
    if err != nil {
        // Handle error
        http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
        return
    }
    if stream != nil {
        defer stream.Close()
        // Use stream...
    }
}
```

#### Optimize for Your Use Case
```go
// High-throughput, low-latency services
highThroughputPool := quic.NewClientPool(
    50, 200,                           // Large pool for many concurrent streams
    100*time.Millisecond, 1*time.Second, // Fast stream creation
    15*time.Second,                    // Short keep-alive for quick failure detection
    "2", "fast-api.com", "fast-api.com:4433",
)

// Batch processing, memory-constrained services  
batchPool := quic.NewClientPool(
    5, 20,                             // Smaller pool to conserve memory
    2*time.Second, 10*time.Second,     // Slower stream creation
    60*time.Second,                    // Longer keep-alive for stable connections
    "2", "batch-api.com", "batch-api.com:4433",
)
```

#### Lock-Free Design Benefits
```go
// This package's lock-free design shines in high-concurrency scenarios
func benchmarkConcurrentAccess() {
    pool := quic.NewClientPool(100, 500, 100*time.Millisecond, 1*time.Second, 30*time.Second, "1", "localhost", "localhost:4433")
    go pool.ClientManager()
    
    // Simulate 1000 concurrent goroutines accessing the pool
    var wg sync.WaitGroup
    for i := 0; i < 1000; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            // Lock-free operations: no mutex contention!
            active := pool.Active()      // atomic.Load
            capacity := pool.Capacity()  // atomic.Load
            _ = active + capacity
        }()
    }
    wg.Wait()
    
    // No performance degradation even with 1000+ concurrent readers
    // Thanks to atomic operations instead of mutexes
}
```

### 6. Testing and Development

#### Development Configuration
```go
// Development/testing setup
func createDevPool() *quic.Pool {
    return quic.NewClientPool(
        2, 5,                           // Smaller pool for development
        time.Second, 3*time.Second,
        30*time.Second,
        "1",                           // Self-signed TLS acceptable for dev
        "localhost",                   // Local development hostname
        "localhost:4433",              // Local address
    )
}
```

#### Unit Testing with Pools
```go
func TestPoolIntegration(t *testing.T) {
    // Create TLS config for testing
    cert, err := tls.LoadX509KeyPair("test-cert.pem", "test-key.pem")
    require.NoError(t, err)
    
    tlsConfig := &tls.Config{
        Certificates: []tls.Certificate{cert},
        MinVersion:   tls.VersionTLS13,
    }
    
    // Create server pool
    serverPool := quic.NewServerPool(5, "", tlsConfig, "localhost:14433", 10*time.Second)
    go serverPool.ServerManager()
    defer serverPool.Close()
    
    // Create client pool  
    clientPool := quic.NewClientPool(
        2, 5, time.Second, 3*time.Second, 10*time.Second,
        "1", // Self-signed for testing
        "localhost",
        "localhost:14433",
    )
    go clientPool.ClientManager()
    defer clientPool.Close()
    
    // Test stream flow
    id, stream, err := serverPool.IncomingGet(5 * time.Second)
    require.NoError(t, err)
    require.NotNil(t, stream)
    require.NotEmpty(t, id)
    
    // Test client get stream
    clientStream, err := clientPool.OutgoingGet(id, 5 * time.Second)
    require.NoError(t, err)
    require.NotNil(t, clientStream)
    
    // Test error case - non-existent ID
    _, err = clientPool.OutgoingGet("non-existent-id", 1 * time.Millisecond)
    require.Error(t, err)
    
    // Test timeout case
    _, _, err = serverPool.IncomingGet(1 * time.Millisecond)
    require.Error(t, err)
}
```

These best practices will help you get the most out of the QUIC package while maintaining reliability and performance in production environments.

## License

Copyright (c) 2025, NodePassProject. Licensed under the BSD 3-Clause License.
See the [LICENSE](LICENSE) file for details.
