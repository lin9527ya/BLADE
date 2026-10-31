package server

import (
	"context"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lin9527ya/blade/db"
)

// LogLine is one captured backend log entry exposed by the /api/logs endpoints.
type LogLine struct {
	Seq   int64  `json:"seq"`
	DBID  int64  `json:"db_id,omitempty"` // server_logs.id; 0 for pre-persistence entries
	TS    string `json:"ts"`
	Level string `json:"level"` // info | warn | error
	Tag   string `json:"tag"`   // the leading [tag] (pg / planner / activity / …), if any
	Text  string `json:"text"`
}

// Log levels, ordered by severity. A line below the current threshold is
// dropped before it reaches the ring, the DB queue or SSE subscribers; stderr
// still receives everything.
const (
	logLevelDebug = iota
	logLevelInfo
	logLevelWarn
	logLevelError
)

var logLevelNames = []string{"debug", "info", "warn", "error"}

// logThreshold is the active minimum level (atomic for hot switching).
var logThreshold atomic.Int32

func init() { logThreshold.Store(int32(logLevelInfo)) }

func parseLogLevel(name string) (int32, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return int32(logLevelDebug), true
	case "info", "":
		return int32(logLevelInfo), true
	case "warn", "warning":
		return int32(logLevelWarn), true
	case "error":
		return int32(logLevelError), true
	}
	return 0, false
}

func logLevelValue(level string) int32 {
	if v, ok := parseLogLevel(level); ok {
		return v
	}
	return int32(logLevelInfo)
}

// LogLevelName returns the active threshold for the settings payload.
func LogLevelName() string { return logLevelNames[logThreshold.Load()] }

// SetLogLevel applies a threshold immediately. Returns false for unknown names.
func SetLogLevel(name string) bool {
	v, ok := parseLogLevel(name)
	if !ok {
		return false
	}
	logThreshold.Store(v)
	return true
}

// dbWriteReq carries a log line to the async DB writer.
type dbWriteReq struct {
	seq int64
	ll  LogLine
}

// logSinkT tees the standard logger into an in-memory ring buffer and fans new
// lines out to SSE subscribers, so the UI can show a live backend log stream.
// It still passes everything through to stderr (the terminal keeps working).
type logSinkT struct {
	mu   sync.Mutex
	ring []LogLine
	cap  int
	seq  int64
	subs map[chan LogLine]struct{}
	out  io.Writer // passthrough (stderr)

	// dbMu 保护 dbCh / dbWG。SetDB 可重入：每个 Server 把自己的池接进来，
	// 关掉时 CloseDB 排空自己的队列。此前是 sync.Once，第一个 Server 的 ctx 与池
	// 会绑定整个进程——第二个 Server 的日志写进第一个的池，那个池一关就永远写不进去。
	dbMu sync.RWMutex
	dbCh chan dbWriteReq // buffered async channel; nil when no DB is wired
	dbWG sync.WaitGroup

	dbDropped     atomic.Int64
	dbWriteFailed atomic.Int64
}

var logSink = &logSinkT{cap: 3000, subs: map[chan LogLine]struct{}{}, out: os.Stderr}

// InitLogLevel applies the startup threshold: BLADE_LOG_LEVEL env first, then
// the stored setting (set after the DB is open). Unknown values keep the default.
func InitLogLevel(env, stored string) {
	envOK := env != "" && SetLogLevel(env)
	if envOK {
		return
	}
	if stored != "" {
		SetLogLevel(stored)
	}
}

// ApplyStoredLogLevel is called once the DB is available.
func ApplyStoredLogLevel(pg *db.DB) {
	if pg == nil {
		return
	}
	if v, ok, err := pg.GetSetting(settingLogLevel); err == nil && ok {
		if env := strings.TrimSpace(os.Getenv("BLADE_LOG_LEVEL")); env != "" {
			return // explicit env wins over the stored value
		}
		SetLogLevel(v)
	}
}

// StartLogCapture redirects the standard log package through the in-memory sink
// (still writing to stderr). Call once at startup, as early as possible.
//
// 注意这里**不要**顺手 slog.SetDefault：那会把 log 包的输出整个劫持进 slog
// （见 slogbridge.go 里 appLogger 的说明）。slog 侧由 appLogger 独立接入本 sink。
func StartLogCapture() { log.SetOutput(logSink) }

// emit 收下一条**级别与 tag 已确定**的记录（slog 桥用）。与 Write 的区别：不再走
// parseLog 的关键词猜测——slog 的调用点本来就写明了级别与 tag。
func (s *logSinkT) emit(level, tag, text string) {
	_, _ = s.out.Write([]byte(time.Now().Format("2006/01/02 15:04:05") + " " + text + "\n"))
	s.add(LogLine{TS: time.Now().Format(time.RFC3339), Level: level, Tag: tag, Text: text})
}

// SetDB wires a postgres DB into the sink. It:
//  1. Restores the last 100 log rows from DB into the ring so the /logs page
//     shows history immediately after restart.
//  2. Starts an async goroutine that persists every subsequent log line to DB.
//
// 可重入：重复调用会先把上一条写入队列排空并停掉旧 writer，再接管新池。
// 这样测试里第二个 Server 不会把日志写进第一个 Server 已经关掉的池，生产侧热重启
// 也不会留下一个绑在旧 ctx 上的 writer。
//
// ctx 只用于「恢复历史」这一步；writer 的退出条件是显式 CloseDB 关掉 channel，
// 而不是 ctx.Done——否则一个用例取消自己的 ctx 就会让整轮测试的日志持久化永久停摆。
func (s *logSinkT) SetDB(ctx context.Context, pg *db.DB) {
	if pg == nil {
		return
	}
	s.CloseDB()
	ch := make(chan dbWriteReq, 2000)
	s.dbMu.Lock()
	s.dbCh = ch
	s.dbMu.Unlock()

	// Restore last 100 rows from DB → prepend to ring as history context.
	if logs, err := pg.RecentLogs(100); err == nil && len(logs) > 0 {
		s.mu.Lock()
		restored := make([]LogLine, 0, len(logs))
		for _, l := range logs {
			s.seq++
			restored = append(restored, LogLine{
				Seq:   s.seq,
				DBID:  l.ID,
				TS:    l.CreatedAt.Format(time.RFC3339),
				Level: l.Level,
				Tag:   l.Tag,
				Text:  l.Text,
			})
		}
		// Prepend history before any in-memory startup logs already in ring.
		s.ring = append(restored, s.ring...)
		if len(s.ring) > s.cap {
			s.ring = s.ring[len(s.ring)-s.cap:]
		}
		s.mu.Unlock()
	}

	// Async writer: picks from channel and inserts into server_logs.
	// Uses os.Stderr directly to report errors and avoids recursive log calls.
	s.dbWG.Add(1)
	goSafe("logsink/writer", nil, func() {
		defer s.dbWG.Done()
		for req := range ch {
			id, err := pg.InsertLog(req.ll.Level, req.ll.Tag, req.ll.Text)
			if err != nil {
				_, _ = os.Stderr.Write([]byte("[logsink] db write: " + err.Error() + "\n"))
				continue
			}
			// Stamp DBID back into the ring entry so history pagination works.
			s.mu.Lock()
			for i := range s.ring {
				if s.ring[i].Seq == req.seq {
					s.ring[i].DBID = id
					break
				}
			}
			s.mu.Unlock()
		}
	})
}

// CloseDB 停掉 DB 写入并等它排空。必须在关闭连接池**之前**调用：队列里剩下的行
// 是关机前最后一段日志，丢在这里就再也找不回来。幂等，可再次 SetDB。
func (s *logSinkT) CloseDB() {
	s.dbMu.Lock()
	ch := s.dbCh
	s.dbCh = nil
	if ch != nil {
		close(ch) // 持写锁关闭：此刻不可能有 add 正在发送
	}
	s.dbMu.Unlock()
	if ch == nil {
		return
	}
	s.dbWG.Wait()
}

// Write implements io.Writer for the log package: one call per log.Printf line.
func (s *logSinkT) Write(p []byte) (int, error) {
	_, _ = s.out.Write(p) // keep the terminal output
	line := strings.TrimRight(string(p), "\n")
	if strings.TrimSpace(line) != "" {
		s.add(parseLog(line))
	}
	return len(p), nil
}

func (s *logSinkT) add(l LogLine) {
	// Filter before the ring so debug output cannot grow memory, the DB queue or
	// an SSE subscriber's backlog. stderr already got the line in Write/emit.
	if logLevelValue(l.Level) < logThreshold.Load() {
		return
	}
	s.mu.Lock()
	s.seq++
	l.Seq = s.seq
	s.ring = append(s.ring, l)
	if len(s.ring) > s.cap {
		s.ring = s.ring[len(s.ring)-s.cap:]
	}
	subs := make([]chan LogLine, 0, len(s.subs))
	for sub := range s.subs {
		subs = append(subs, sub)
	}
	s.mu.Unlock()

	// Async DB write (non-blocking; drops if channel is full under extreme load).
	// 读锁与 CloseDB 的写锁互斥：CloseDB 不会在有人正往 channel 里发的瞬间关掉它。
	s.dbMu.RLock()
	ch := s.dbCh
	if ch != nil {
		select {
		case ch <- dbWriteReq{seq: l.Seq, ll: l}:
		default:
			s.dbDropped.Add(1)
		}
	}
	s.dbMu.RUnlock()

	for _, sub := range subs {
		select {
		case sub <- l:
		default:
		}
	}
}

// DBStats reports persistence loss: a full queue drops a line by design, and a
// write failure is retried only by the caller's next line. Both must be visible.
func (s *logSinkT) DBStats() map[string]int64 {
	return map[string]int64{
		"db_queue_dropped": s.dbDropped.Load(),
		"db_write_failed":  s.dbWriteFailed.Load(),
	}
}

// recent returns ring lines with Seq > since (capped to limit), and the latest seq.
func (s *logSinkT) recent(since int64, limit int) (lines []LogLine, cursor int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cursor = s.seq
	if limit <= 0 || limit > s.cap {
		limit = s.cap
	}
	for _, l := range s.ring {
		if l.Seq > since {
			lines = append(lines, l)
		}
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines, cursor
}

func (s *logSinkT) subscribe() (<-chan LogLine, func()) {
	ch := make(chan LogLine, 256)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.subs, ch)
			s.mu.Unlock()
			// Do NOT close(ch): add() snapshots subscribers under the lock, then
			// sends after unlocking. A concurrent send/close is a panic. Removing
			// the channel from the map is enough; the handler exits on ctx.Done.
		})
	}
}

// parseLog pulls a level + [tag] out of a standard-logger line; the timestamp is
// the capture time (RFC3339).
func parseLog(line string) LogLine {
	msg := line
	// strip the "2006/01/02 15:04:05" LstdFlags prefix if present
	if len(msg) >= 20 && msg[4] == '/' && msg[7] == '/' && msg[10] == ' ' {
		msg = strings.TrimSpace(msg[19:])
	}
	// Explicit level prefixes (written by slog and by call sites that need a
	// debug line) win over keyword guessing; the prefix is stripped from Text.
	level := ""
	for _, p := range []struct{ prefix, lvl string }{
		{"[ERROR]", "error"}, {"[WARN]", "warn"}, {"[INFO]", "info"}, {"[DEBUG]", "debug"},
	} {
		if strings.HasPrefix(msg, p.prefix) {
			level = p.lvl
			msg = strings.TrimSpace(strings.TrimPrefix(msg, p.prefix))
			break
		}
	}
	tag := ""
	if strings.HasPrefix(msg, "[") {
		if i := strings.IndexByte(msg, ']'); i > 1 {
			tag = msg[1:i]
		}
	}
	if level == "" {
		level = levelOf(msg)
	}
	return LogLine{TS: time.Now().Format(time.RFC3339), Level: level, Tag: tag, Text: msg}
}

func levelOf(msg string) string {
	low := strings.ToLower(msg)
	for _, k := range []string{"fatal", "panic", "error", "err:", "失败", "丢弃", "拒绝", "✕", "不可达"} {
		if strings.Contains(low, k) {
			return "error"
		}
	}
	for _, k := range []string{"warn", "disabled", "禁用", "skip", "stopped", "⚠", "重试"} {
		if strings.Contains(low, k) {
			return "warn"
		}
	}
	return "info"
}
