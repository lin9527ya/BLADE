package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/Norma/transcript"
	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/llmrec"
	_ "golang.org/x/image/webp"
)

type manualVisionModel struct {
	provider           llm.Provider
	format, model, key string
}

func (s *Server) visionModel(t *Task, c *db.Conversation) (manualVisionModel, error) {
	var id *int64
	if t != nil {
		res, err := s.resolveTaskRoleLLM(t, "mainagent")
		if err != nil {
			return manualVisionModel{}, err
		}
		if !res.Available {
			return manualVisionModel{}, errors.New("当前会话没有可用模型")
		}
		id = res.ProfileID
	} else {
		id = s.effectiveProfileForAgent(c.AgentKey, c.LLMProfileID)
	}
	var p llm.Provider
	var cfg agent.Config
	var ok bool
	if id != nil {
		p, cfg, ok = s.providerForProfile(*id)
	} else {
		p, cfg, ok = s.globalLLMConfig()
	}
	if !ok || p == nil {
		return manualVisionModel{}, errors.New("当前会话没有可用模型")
	}
	b, _ := json.Marshal([]any{id, cfg.Provider(), cfg.Model, cfg.BaseURL, cfg.APIKey, cfg.Proxy, cfg.CustomHeaders})
	return manualVisionModel{p, cfg.Provider(), cfg.Model, fmt.Sprintf("chat_vision_%x", sha256.Sum256(b))}, nil
}
func (s *Server) chatVision(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var task *Task
	var conv *db.Conversation
	switch q.Get("scope") {
	case "task":
		var ok bool
		task, ok = s.m.Task(q.Get("id"))
		if !ok {
			writeErr(w, 404, "任务不存在")
			return
		}
	case "session":
		id, err := strconv.ParseInt(strings.TrimPrefix(q.Get("id"), "conv-"), 10, 64)
		if err != nil {
			writeErr(w, 400, "无效会话")
			return
		}
		conv, err = s.m.pg.GetConversation(id)
		if err != nil {
			writeErr(w, 500, "会话读取失败")
			return
		}
		if conv == nil {
			writeErr(w, 404, "会话不存在")
			return
		}
	default:
		writeErr(w, 400, "未知聊天范围")
		return
	}
	model, err := s.visionModel(task, conv)
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	status, _, err := s.m.pg.GetSetting(model.key)
	if err != nil {
		writeErr(w, 500, "读取视觉状态失败")
		return
	}
	if status == "" {
		status = "unknown"
	}
	if r.Method == http.MethodPost {
		colors := []color.RGBA{{R: 245, A: 255}, {G: 245, A: 255}, {B: 245, A: 255}}
		names := []string{"red", "green", "blue"}
		var seed [4]byte
		if _, err = rand.Read(seed[:]); err != nil {
			writeErr(w, 500, "探测初始化失败")
			return
		}
		im := image.NewRGBA(image.Rect(0, 0, 192, 48))
		want := make([]string, len(seed))
		for j, v := range seed {
			i := int(v) % len(colors)
			want[j] = names[i]
			for y := 0; y < 48; y++ {
				for x := j * 48; x < (j+1)*48; x++ {
					im.SetRGBA(x, y, colors[i])
				}
			}
		}
		var buf bytes.Buffer
		if err = png.Encode(&buf, im); err != nil {
			writeErr(w, 500, "探测图片生成失败")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		reply, e := agent.ManualImageAnswer(llmrec.WithWorker(ctx, "vision-probe"), model.provider, model.format, "Read the four equal swatches from left to right, including repeated colors. Reply with four English color words separated by spaces.", []agent.ChatImage{{MIME: "image/png", Data: buf.Bytes()}})
		status = "unsupported"
		if e != nil {
			msg := strings.ToLower(e.Error())
			mentionsImage := strings.Contains(msg, "image") || strings.Contains(msg, "vision") || strings.Contains(msg, "multimodal")
			rejected := strings.Contains(msg, "not support") || strings.Contains(msg, "unsupported") || strings.Contains(msg, "text only")
			if !mentionsImage || !rejected {
				writeErr(w, 502, "模型未完成读图测试，请核对协议和连接后重试")
				return
			}
		} else {
			words := strings.FieldsFunc(strings.ToLower(reply), func(r rune) bool { return r < 'a' || r > 'z' })
			var found []string
			for _, w := range words {
				if w == "red" || w == "green" || w == "blue" {
					found = append(found, w)
				}
			}
			if strings.Join(found, " ") == strings.Join(want, " ") {
				status = "supported"
			}
		}
		if err = s.m.pg.SetSettings(map[string]string{model.key: status}); err != nil {
			writeErr(w, 500, "保存视觉状态失败")
			return
		}
	}
	writeJSON(w, 200, map[string]string{"status": status, "model": model.model})
}

func chatImageMIME(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpeg", ".jpg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	}
	return ""
}

// Open each directory relative to its already-confined parent. A concurrent
// symlink swap cannot substitute another session's uploads directory.
func openChatUploads(base string) (*os.Root, error) {
	group := filepath.Dir(base)
	root, err := os.OpenRoot(filepath.Dir(group))
	if err != nil {
		return nil, err
	}
	for _, part := range []string{filepath.Base(group), filepath.Base(base), "uploads"} {
		info, e := root.Lstat(part)
		if e != nil || !info.IsDir() {
			root.Close()
			return nil, errors.New("图片目录不可读")
		}
		next, e := root.OpenRoot(part)
		root.Close()
		if e != nil {
			return nil, e
		}
		actual, e := next.Stat(".")
		if e != nil || !os.SameFile(actual, info) {
			next.Close()
			return nil, errors.New("图片目录已经变化")
		}
		root = next
	}
	return root, nil
}
func readChatImages(base string, atts []chatAttachment) ([]agent.ChatImage, error) {
	var out []agent.ChatImage
	var total int
	for _, a := range atts {
		kind := chatImageMIME(a.Path)
		if kind == "" {
			kind = a.ImageMIME
		}
		if kind == "" {
			continue
		}
		if kind != "image/png" && kind != "image/jpeg" && kind != "image/webp" {
			return nil, errors.New("仅支持 PNG、JPEG 和 WebP")
		}
		if len(out) >= 4 {
			return nil, errors.New("每次最多发送四张截图")
		}
		name := filepath.Base(a.Path)
		if a.Path != "uploads/"+name || name != a.Name || strings.ContainsAny(name, "/\\") {
			return nil, errors.New("图片不属于当前会话")
		}
		root, err := openChatUploads(base)
		if err != nil {
			return nil, err
		}
		expected, err := root.Lstat(name)
		if err != nil || !expected.Mode().IsRegular() {
			root.Close()
			return nil, errors.New("图片必须是当前会话上传的普通文件")
		}
		f, err := root.Open(name)
		root.Close()
		if err != nil {
			return nil, errors.New("图片不存在")
		}
		info, err := f.Stat()
		if err != nil || !os.SameFile(expected, info) || !info.Mode().IsRegular() || info.Size() > 8<<20 {
			f.Close()
			return nil, errors.New("单张图片需小于等于 8 MiB")
		}
		data, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
		f.Close()
		if err != nil {
			return nil, err
		}
		total += len(data)
		if len(data) > 8<<20 || total > 16<<20 {
			return nil, errors.New("图片总大小超出限制")
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || "image/"+format != kind || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 24_000_000 {
			return nil, errors.New("图片内容、尺寸或类型无效")
		}
		if _, _, err = image.Decode(bytes.NewReader(data)); err != nil {
			return nil, errors.New("图片文件损坏")
		}
		out = append(out, agent.ChatImage{MIME: kind, Data: data})
	}
	return out, nil
}

type manualVisionTurn struct {
	model  manualVisionModel
	images []agent.ChatImage
	store  *transcript.Store
}

func (s *Server) prepareVisionTurn(w http.ResponseWriter, t *Task, c *db.Conversation, base string, atts []chatAttachment) (*manualVisionTurn, bool) {
	images, err := readChatImages(base, atts)
	if err != nil {
		writeErr(w, 400, err.Error())
		return nil, false
	}
	if len(images) == 0 {
		return nil, true
	}
	model, err := s.visionModel(t, c)
	if err != nil {
		writeErr(w, 409, err.Error())
		return nil, false
	}
	status, _, err := s.m.pg.GetSetting(model.key)
	if err != nil {
		writeErr(w, 500, "读取视觉状态失败")
		return nil, false
	}
	if status != "supported" {
		writeErr(w, 409, "请先在输入框测试当前模型的读图能力")
		return nil, false
	}
	return &manualVisionTurn{model: model, images: images, store: transcript.NewStore(filepath.Join(s.m.dir, "transcripts"))}, true
}
func (v *manualVisionTurn) answer(ctx context.Context, session, lane, text string) db.Activity {
	ctx, cancel := context.WithTimeout(transcript.WithSessionID(llmrec.WithWorker(ctx, lane), session), 3*time.Minute)
	defer cancel()
	// Only human-visible text is included in this isolated, tool-free turn.
	recs, loadErr := v.store.Load(v.store.MainPath(session))
	if loadErr != nil {
		return db.Activity{Worker: lane, Kind: "text", IsError: true, Summary: "读取会话历史失败"}
	}
	var history []llm.Message
	remaining := 8192
	for i := len(recs) - 1; i >= 0 && len(history) < 12 && remaining > 0; i-- {
		m := recs[i].Message
		if m == nil || (m.Role != llm.RoleUser && m.Role != llm.RoleAssistant) {
			continue
		}
		// Never carry a tool result, an image, or hidden thinking into this request.
		if len(m.Content) != 1 || m.Content[0].Type != llm.BlockText {
			continue
		}
		runes := []rune(m.Content[0].Text)
		if len(runes) > remaining {
			runes = runes[:remaining]
		}
		remaining -= len(runes)
		history = append(history, llm.Message{Role: m.Role, Content: []llm.ContentBlock{llm.TextBlock(string(runes))}})
	}
	for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
		history[i], history[j] = history[j], history[i]
	}
	result, err := agent.ManualImageAnswer(ctx, v.model.provider, v.model.format, text, v.images, history...)
	if err == nil {
		writer := v.store.ResumeWriter(session, transcript.LastUUID(recs))
		writer.RecordMessage(llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock(fmt.Sprintf("%s [附 %d 张图片]", text, len(v.images)))}}, llm.Usage{})
		writer.RecordMessage(llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(result)}}, llm.Usage{})
		if writer.Err() != nil {
			log.Printf("manual image transcript: %v", writer.Err())
		}
	}
	if err != nil {
		result = "图片请求失败，请检查模型连接或重新测试读图能力。"
		if ctx.Err() != nil {
			result = "图片请求已结束：" + ctx.Err().Error()
		}
	}
	return db.Activity{Worker: lane, Kind: "text", Summary: result, Detail: result, IsError: err != nil}
}
