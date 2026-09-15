// Command loadtest hammers messageServer's HTTP API with concurrent workers
// that mix polling GETs and message POSTs, then reports latency percentiles
// and error rates.
//
// Usage:
//
//	go run ./loadtest -addr http://localhost:8080 -workers 100 -duration 30s -rate 500 -write-ratio 0.3
//
// The tool runs as one user: its key is authorized once and it claims a
// username (-username) the way any client does. Every worker therefore posts as
// that user and reads that user's chat, which is the point — the run measures
// the server, not a conversation.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type config struct {
	addr       string
	workers    int
	duration   time.Duration
	rate       float64
	writeRatio float64
	timeout    time.Duration
	// identity signs every request; the server rejects unsigned ones.
	identity *identity
}

type workerStats struct {
	getLatencies  []time.Duration
	postLatencies []time.Duration
	getErrors     int64
	postErrors    int64
	statusCounts  map[int]int64
}

func newWorkerStats() *workerStats {
	return &workerStats{statusCounts: make(map[int]int64)}
}

func main() {
	cfg := config{}
	flag.StringVar(&cfg.addr, "addr", "http://localhost:8080", "base URL of messageServer")
	flag.IntVar(&cfg.workers, "workers", 50, "number of concurrent worker goroutines")
	flag.DurationVar(&cfg.duration, "duration", 30*time.Second, "how long to run the load test")
	flag.Float64Var(&cfg.rate, "rate", 0, "total requests/sec across all workers (0 = unlimited, closed-loop)")
	flag.Float64Var(&cfg.writeRatio, "write-ratio", 0.3, "fraction of requests that are POST sends (0-1)")
	flag.DurationVar(&cfg.timeout, "timeout", 5*time.Second, "per-request timeout")
	keyPath := flag.String("key", "loadtest.key", "file holding this tool's RSA private key")
	username := flag.String("username", "loadtest", "username this tool posts under")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, cfg.duration)
	defer cancel()

	client := &http.Client{
		Timeout: cfg.timeout,
		Transport: &http.Transport{
			MaxIdleConns:        cfg.workers * 2,
			MaxIdleConnsPerHost: cfg.workers * 2,
		},
	}

	id, err := loadIdentity(*keyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "identity:", err)
		os.Exit(1)
	}
	if err := checkAuthorized(client, cfg.addr, id); err != nil {
		fmt.Fprintln(os.Stderr, "\n"+err.Error())
		os.Exit(1)
	}
	// The server takes the sender from the key, so the key needs a name before
	// a single POST will be accepted.
	if err := ensureUsername(client, cfg.addr, id, *username); err != nil {
		fmt.Fprintln(os.Stderr, "\n"+err.Error())
		os.Exit(1)
	}
	cfg.identity = id

	var ticker *time.Ticker
	if cfg.rate > 0 {
		ticker = time.NewTicker(time.Duration(float64(time.Second) / cfg.rate))
		defer ticker.Stop()
	}

	var totalReqs int64
	statsCh := make(chan *workerStats, cfg.workers)
	var wg sync.WaitGroup
	start := time.Now()

	for i := 0; i < cfg.workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			statsCh <- runWorker(ctx, id, cfg, client, ticker, &totalReqs)
		}(i)
	}

	go reportProgress(ctx, &totalReqs, start)

	wg.Wait()
	close(statsCh)
	elapsed := time.Since(start)

	merged := newWorkerStats()
	for ws := range statsCh {
		merged.getLatencies = append(merged.getLatencies, ws.getLatencies...)
		merged.postLatencies = append(merged.postLatencies, ws.postLatencies...)
		merged.getErrors += ws.getErrors
		merged.postErrors += ws.postErrors
		for code, n := range ws.statusCounts {
			merged.statusCounts[code] += n
		}
	}

	printReport(merged, elapsed)
}

func reportProgress(ctx context.Context, totalReqs *int64, start time.Time) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n := atomic.LoadInt64(totalReqs)
			fmt.Printf("[%6.1fs] %d requests (%.0f req/s)\n", time.Since(start).Seconds(), n, float64(n)/time.Since(start).Seconds())
		}
	}
}

func runWorker(ctx context.Context, id int, cfg config, client *http.Client, ticker *time.Ticker, totalReqs *int64) *workerStats {
	stats := newWorkerStats()
	rng := rand.New(rand.NewSource(time.Now().UnixNano() ^ int64(id)))
	since := 0

	for {
		select {
		case <-ctx.Done():
			return stats
		default:
		}
		if ticker != nil {
			select {
			case <-ctx.Done():
				return stats
			case <-ticker.C:
			}
		}

		atomic.AddInt64(totalReqs, 1)
		if rng.Float64() < cfg.writeRatio {
			status, lat, err := doPost(ctx, client, cfg, rng)
			if err != nil {
				stats.postErrors++
				continue
			}
			stats.postLatencies = append(stats.postLatencies, lat)
			stats.statusCounts[status]++
		} else {
			status, lat, maxID, err := doGet(ctx, client, cfg, since)
			if err != nil {
				stats.getErrors++
				continue
			}
			if maxID > since {
				since = maxID
			}
			stats.getLatencies = append(stats.getLatencies, lat)
			stats.statusCounts[status]++
		}
	}
}

func doGet(ctx context.Context, client *http.Client, cfg config, since int) (status int, latency time.Duration, maxID int, err error) {
	url := fmt.Sprintf("%s/api/messages?since=%d", cfg.addr, since)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, since, err
	}
	if err := cfg.identity.sign(req, nil); err != nil {
		return 0, 0, since, err
	}

	start := time.Now()
	resp, err := client.Do(req)
	latency = time.Since(start)
	if err != nil {
		return 0, latency, since, err
	}
	defer resp.Body.Close()

	var msgs []struct {
		ID int `json:"id"`
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		if jsonErr := json.Unmarshal(body, &msgs); jsonErr == nil {
			for _, m := range msgs {
				if m.ID > maxID {
					maxID = m.ID
				}
			}
		}
	}
	if maxID < since {
		maxID = since
	}
	return resp.StatusCode, latency, maxID, nil
}

func doPost(ctx context.Context, client *http.Client, cfg config, rng *rand.Rand) (status int, latency time.Duration, err error) {
	payload, _ := json.Marshal(map[string]string{
		"text": fmt.Sprintf("load test message %d", rng.Int()),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.addr+"/api/messages", bytes.NewReader(payload))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := cfg.identity.sign(req, payload); err != nil {
		return 0, 0, err
	}

	start := time.Now()
	resp, err := client.Do(req)
	latency = time.Since(start)
	if err != nil {
		return 0, latency, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, latency, nil
}

func printReport(stats *workerStats, elapsed time.Duration) {
	total := len(stats.getLatencies) + len(stats.postLatencies) + int(stats.getErrors) + int(stats.postErrors)

	fmt.Println()
	fmt.Println("==== Load Test Report ====")
	fmt.Printf("duration:       %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("total requests: %d (%.1f req/s)\n", total, float64(total)/elapsed.Seconds())
	fmt.Println()

	printKindStats("GET  /api/messages", stats.getLatencies, stats.getErrors)
	printKindStats("POST /api/messages", stats.postLatencies, stats.postErrors)

	fmt.Println()
	fmt.Println("status codes:")
	codes := make([]int, 0, len(stats.statusCounts))
	for code := range stats.statusCounts {
		codes = append(codes, code)
	}
	sort.Ints(codes)
	for _, code := range codes {
		fmt.Printf("  %d: %d\n", code, stats.statusCounts[code])
	}
}

func printKindStats(label string, latencies []time.Duration, errs int64) {
	fmt.Printf("%s: %d ok, %d errors\n", label, len(latencies), errs)
	if len(latencies) == 0 {
		return
	}
	sorted := append([]time.Duration(nil), latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	pct := func(p float64) time.Duration {
		idx := int(p * float64(len(sorted)-1))
		return sorted[idx]
	}
	var sum time.Duration
	for _, d := range sorted {
		sum += d
	}
	mean := sum / time.Duration(len(sorted))

	fmt.Printf("  min=%s p50=%s p90=%s p99=%s max=%s mean=%s\n",
		sorted[0].Round(time.Millisecond),
		pct(0.50).Round(time.Millisecond),
		pct(0.90).Round(time.Millisecond),
		pct(0.99).Round(time.Millisecond),
		sorted[len(sorted)-1].Round(time.Millisecond),
		mean.Round(time.Millisecond),
	)
}
