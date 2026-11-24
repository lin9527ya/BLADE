package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/im"
)

const (
	imQueueSize     = 64
	imWorkerCount   = 4
	imMaxTextRunes  = 4000
	imMaxReplyRunes = 3500
)

type imJob struct {
	messageID int64
	conn      *db.IMConnection
	user      *db.IMAllowedUser
	in        im.Incoming
}

type imResult struct {
	reply          string
	action         string
	detail         string
	taskID         *int64
	conversationID *int64
	status         int
	err            error
}

type imRuntime struct {
	sig       string
	cancel    context.CancelFunc
	connector im.Connector
}

type imService struct {
	s        *Server
	factory  im.Factory
	mu       sync.Mutex
	runtimes map[int64]*imRuntime
	queue    chan imJob
	trigger  chan struct{}
	rate     *imRateLimiter
}

func newIMService(s *Server) *imService {
	return &imService{
		s:        s,
		factory:  im.New,
		runtimes: map[int64]*imRuntime{},
		queue:    make(chan imJob, imQueueSize),
		trigger:  make(chan struct{}, 1),
		rate:     newIMRateLimiter(),
	}
}

func (svc *imService) Run(ctx context.Context) {
	if svc == nil || svc.s == nil || svc.s.m == nil || svc.s.m.PG() == nil {
		return
	}
	if n, err := svc.s.m.PG().RecoverStaleIMMessages(ctx, 10*time.Minute); err != nil {
		log.Printf("[im] 恢复未完成消息失败: %v", err)
	} else if n > 0 {
		log.Printf("[im] 已将 %d 条重启前未完成的消息标记为失败", n)
	}

	var wg sync.WaitGroup
	for i := 0; i < imWorkerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job := <-svc.queue:
					svc.processJob(ctx, job)
				}
			}
		}()
	}
	svc.reload(ctx)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			svc.stopAll()
			wg.Wait()
			return
		case <-svc.trigger:
			svc.reload(ctx)
		case <-ticker.C:
			svc.reload(ctx)
		}
	}
}

// Reload asks the running service to diff database connections and reconnect
// changed ones. It never blocks the caller.
func (svc *imService) Reload() {
	if svc == nil {
		return
	}
	select {
	case svc.trigger <- struct{}{}:
	default:
	}
}

func (svc *imService) reload(ctx context.Context) {
	conns, err := svc.s.m.PG().ListIMConnections()
	if err != nil {
		log.Printf("[im] 读取连接配置失败: %v", err)
		return
	}
	desired := map[int64]*db.IMConnection{}
	for _, c := range conns {
		if c.Enabled {
			desired[c.ID] = c
		}
	}
	svc.mu.Lock()
	for id, rt := range svc.runtimes {
		if c, ok := desired[id]; !ok || imConnectionSignature(c) != rt.sig {
			rt.cancel()
			delete(svc.runtimes, id)
		}
	}
	svc.mu.Unlock()
	for _, c := range conns {
		if !c.Enabled {
			continue
		}
		svc.mu.Lock()
		_, running := svc.runtimes[c.ID]
		svc.mu.Unlock()
		if !running {
			svc.startConnector(ctx, c)
		}
	}
}

func imConnectionSignature(c *db.IMConnection) string {
	if c == nil {
		return ""
	}
	return fmt.Sprintf("%s|%t|%s|%s|%s", c.Platform, c.Enabled, c.AppID, c.AppSecret, string(c.Extra))
}

func (svc *imService) startConnector(ctx context.Context, c *db.IMConnection) {
	cctx, cancel := context.WithCancel(ctx)
	rt := &imRuntime{sig: imConnectionSignature(c), cancel: cancel}
	svc.mu.Lock()
	svc.runtimes[c.ID] = rt
	svc.mu.Unlock()

	go func() {
		backoff := 5 * time.Second
		for {
			if cctx.Err() != nil {
				return
			}
			connector, err := svc.factory(im.Config{
				ConnectionID: c.ID, Name: c.Name, Platform: c.Platform,
				AppID: c.AppID, AppSecret: c.AppSecret, Extra: imExtra(c.Extra),
			})
			if err != nil {
				svc.setStatus(c.ID, "error", err.Error())
				if !sleepContext(cctx, backoff) {
					return
				}
				backoff = nextIMBackoff(backoff)
				continue
			}
			svc.mu.Lock()
			if cur := svc.runtimes[c.ID]; cur == rt {
				rt.connector = connector
			}
			svc.mu.Unlock()
			err = connector.Run(cctx, im.Callbacks{
				OnMessage: func(mctx context.Context, in im.Incoming) error {
					return svc.handleIncoming(mctx, c.ID, in)
				},
				OnStatus: func(status string, statusErr error) {
					msg := ""
					if statusErr != nil {
						msg = statusErr.Error()
					}
					svc.setStatus(c.ID, status, msg)
				},
			})
			if cctx.Err() != nil {
				return
			}
			if err == nil {
				err = errors.New("连接已断开")
			}
			svc.setStatus(c.ID, "error", err.Error())
			if !sleepContext(cctx, backoff) {
				return
			}
			backoff = nextIMBackoff(backoff)
		}
	}()
}

func nextIMBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > time.Minute {
		return time.Minute
	}
	return d
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (svc *imService) stopAll() {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	for id, rt := range svc.runtimes {
		rt.cancel()
		delete(svc.runtimes, id)
	}
}

func (svc *imService) connectorFor(id int64) im.Connector {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if rt := svc.runtimes[id]; rt != nil {
		return rt.connector
	}
	return nil
}

func (svc *imService) setStatus(id int64, status, msg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := svc.s.m.PG().SetIMConnectionStatus(ctx, id, status, msg, status == "connected"); err != nil {
		log.Printf("[im] 更新连接 %d 状态失败: %v", id, err)
	}
}

func imExtra(raw json.RawMessage) map[string]any {
	var extra map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &extra) != nil {
		return map[string]any{}
	}
	return extra
}

func (svc *imService) handleIncoming(ctx context.Context, connID int64, in im.Incoming) error {
	if strings.TrimSpace(in.MessageID) == "" || strings.TrimSpace(in.SenderID) == "" {
		return nil
	}
	in.Text = clipIMText(in.Text, imMaxTextRunes)
	if in.Text == "" {
		return nil
	}
	conn, err := svc.s.m.PG().GetIMConnection(connID)
	if err != nil || conn == nil || !conn.Enabled || conn.Platform != in.Platform {
		return nil
	}
	if in.ChatType == "group" && !im.AllowGroup(imExtra(conn.Extra)) {
		svc.recordIgnored(ctx, conn, in, "群聊未开启控制")
		return nil
	}
	user, err := svc.s.m.PG().GetIMAllowedUser(in.Platform, in.SenderID)
	if err != nil {
		log.Printf("[im] 读取允许用户失败: %v", err)
		return err
	}
	if user == nil || !user.Enabled {
		svc.recordIgnored(ctx, conn, in, "未授权用户")
		_ = svc.sendReply(ctx, conn.ID, in, "未授权：请联系管理员在 BLADE 的系统设置 → IM 控制中登记你的用户 ID。")
		return nil
	}
	if !svc.rate.allow(in.Platform+"|"+in.SenderID, connID) {
		svc.recordIgnored(ctx, conn, in, "触发频率限制")
		return nil
	}
	id, first, err := svc.s.m.PG().InsertIMMessage(ctx, &db.IMMessage{
		ConnectionID: conn.ID, PlatformMsgID: in.MessageID, ChatID: in.ChatID,
		ChatType: in.ChatType, SenderID: in.SenderID, SenderName: in.SenderName,
		Text: in.Text, State: "received",
	})
	if err != nil {
		log.Printf("[im] 保存消息失败: %v", err)
		return err
	}
	if !first {
		return nil // platform retry: already accepted
	}
	job := imJob{messageID: id, conn: conn, user: user, in: in}
	select {
	case svc.queue <- job:
		return nil
	default:
		_ = svc.s.m.PG().UpdateIMMessage(ctx, id, "failed", nil, nil, "", "处理队列已满")
		_ = svc.sendReply(ctx, conn.ID, in, "服务繁忙，请稍后重试。")
		return nil
	}
}

func (svc *imService) recordIgnored(ctx context.Context, conn *db.IMConnection, in im.Incoming, reason string) {
	id, first, err := svc.s.m.PG().InsertIMMessage(ctx, &db.IMMessage{
		ConnectionID: conn.ID, PlatformMsgID: in.MessageID, ChatID: in.ChatID,
		ChatType: in.ChatType, SenderID: in.SenderID, SenderName: in.SenderName,
		Text: in.Text, State: "ignored",
	})
	if err != nil || !first {
		return
	}
	_ = svc.s.m.PG().UpdateIMMessage(ctx, id, "ignored", nil, nil, "", reason)
	_ = svc.s.m.PG().RecordIMAction(ctx, id, "message", nil, "ignored", reason, imActor(in))
	_ = svc.s.m.PG().RecordIMAudit(ctx, imActor(in), "message", 403, reason)
}

func imActor(in im.Incoming) string {
	return fmt.Sprintf("im:%s:%s", in.Platform, in.SenderID)
}

func (svc *imService) processJob(ctx context.Context, job imJob) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[im] 处理消息 %d 发生 panic: %v", job.messageID, r)
			_ = svc.s.m.PG().UpdateIMMessage(context.Background(), job.messageID, "failed", nil, nil, "", fmt.Sprintf("内部错误: %v", r))
		}
	}()
	_ = svc.s.m.PG().UpdateIMMessage(ctx, job.messageID, "processing", nil, nil, "", "")

	res := svc.route(ctx, job)
	sendErr := error(nil)
	if strings.TrimSpace(res.reply) != "" {
		sendErr = svc.sendReply(ctx, job.conn.ID, job.in, res.reply)
	}
	state, errMsg := "replied", ""
	if res.err != nil {
		state, errMsg = "failed", res.err.Error()
	}
	if sendErr != nil {
		state = "failed"
		if errMsg == "" {
			errMsg = "回复发送失败: " + sendErr.Error()
		} else {
			errMsg += "；回复发送失败: " + sendErr.Error()
		}
	}
	_ = svc.s.m.PG().UpdateIMMessage(ctx, job.messageID, state, res.taskID, res.conversationID, clipIMReply(res.reply), errMsg)
	_ = svc.s.m.PG().RecordIMAction(ctx, job.messageID, res.action, res.taskID, state, res.detail, imActor(job.in))
	auditStatus := res.status
	if auditStatus == 0 {
		auditStatus = 200
	}
	_ = svc.s.m.PG().RecordIMAudit(ctx, imActor(job.in), res.action, auditStatus, res.detail)
}

func (svc *imService) route(ctx context.Context, job imJob) imResult {
	if cmd, ok := parseIMCommand(job.in.Text); ok {
		return svc.executeCommand(ctx, job, cmd)
	}
	return svc.naturalLanguage(ctx, job)
}

func (svc *imService) naturalLanguage(ctx context.Context, job imJob) imResult {
	res := imResult{action: "chat", status: 200}
	pg := svc.s.m.PG()
	title := "IM " + job.in.Platform + " " + job.in.SenderName
	conv, err := pg.GetOrCreateIMConversation(ctx, job.conn.ID, job.in.ChatID, job.in.SenderID, title)
	if err != nil {
		res.err, res.status = err, 500
		res.reply = "创建会话失败：" + err.Error()
		return res
	}
	res.conversationID = &conv.ID
	actor := imActor(job.in)
	p := runPrincipal{Role: job.user.Role, Actor: actor, ID: actor, Surface: "im"}
	msg := fmt.Sprintf("来自%s用户的消息（请用简洁中文回答；需要时调用任务工具）：\n%s", platformLabel(job.in.Platform), job.in.Text)
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	reply, err := svc.s.runConversationForIM(runCtx, conv, msg, p)
	if errors.Is(err, errIMConversationBusy) {
		res.status = 409
		res.reply = "上一条消息仍在处理中，请稍后再试，或发送 /tasks 查看任务状态。"
		return res
	}
	if err != nil {
		res.err, res.status = err, 500
		res.reply = "处理失败：" + err.Error()
		return res
	}
	if strings.TrimSpace(reply) == "" {
		reply = "已处理完成，但没有可展示的文本结果；请在 Web 端查看。"
	}
	res.reply = reply
	return res
}

func (svc *imService) sendReply(ctx context.Context, connID int64, in im.Incoming, text string) error {
	connector := svc.connectorFor(connID)
	if connector == nil {
		return errors.New("连接未就绪")
	}
	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return connector.Send(sctx, im.Outgoing{
		ConnectionID: connID, ChatID: in.ChatID, ReplyTo: in.MessageID,
		Text: clipIMReply(text), ReplyMeta: in.ReplyMeta,
	})
}

func platformLabel(platform string) string {
	switch platform {
	case im.PlatformDingTalk:
		return "钉钉"
	case im.PlatformFeishu:
		return "飞书"
	default:
		return platform
	}
}

func clipIMText(s string, limit int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > limit {
		s = string(r[:limit]) + "…"
	}
	return s
}

func clipIMReply(s string) string {
	return clipIMText(s, imMaxReplyRunes)
}

type imRateLimiter struct {
	mu     sync.Mutex
	window time.Time
	users  map[string]int
	conns  map[int64]int
}

func newIMRateLimiter() *imRateLimiter {
	return &imRateLimiter{users: map[string]int{}, conns: map[int64]int{}}
}

func (r *imRateLimiter) allow(userKey string, connID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if r.window.IsZero() || now.Sub(r.window) >= time.Minute {
		r.window = now
		r.users = map[string]int{}
		r.conns = map[int64]int{}
	}
	if r.users[userKey] >= 10 || r.conns[connID] >= 60 {
		return false
	}
	r.users[userKey]++
	r.conns[connID]++
	return true
}
