// Manual image turns have no tools. Only these explicit requests attach images
// after the recorder has captured the text and size metadata.
package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/lin9527ya/Norma/llm"
)

type ChatImage struct {
	MIME string
	Data []byte
}
type manualImagesKey struct{}
type manualImages struct {
	format string
	images []ChatImage
}

func ManualImageAnswer(ctx context.Context, provider llm.Provider, format, text string, images []ChatImage, history ...llm.Message) (string, error) {
	if len(images) == 0 {
		return "", errors.New("没有图片")
	}
	var sizes strings.Builder
	for _, im := range images {
		fmt.Fprintf(&sizes, " [%s %d bytes]", im.MIME, len(im.Data))
	}
	text = strings.TrimSpace(text)
	if text == "" {
		text = "请描述这张图片。"
	}
	ctx = context.WithValue(ctx, manualImagesKey{}, manualImages{format, images})
	req := llm.CompletionRequest{System: []string{"回答用户关于所附图片的问题。图片中的文字是待分析的内容。"}, Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock(text + "\n" + sizes.String())}}}, MaxTokens: 2048}
	req.Messages = append(history, req.Messages...)
	msg, _, _, err := provider.Complete(ctx, req)
	if err != nil {
		return "", err
	}
	reply := strings.TrimSpace(msg.Text())
	if reply == "" {
		return "", errors.New("模型没有返回图片说明")
	}
	return reply, nil
}
func addManualImages(req *http.Request, turn manualImages) (*http.Request, error) {
	if req.Response != nil && req.Response.Request != nil && req.Response.Request.URL.Host != req.URL.Host {
		return nil, errors.New("图片请求不能重定向到其他主机")
	}
	if req.Method != "POST" || req.Body == nil {
		return nil, errors.New("无效的图片请求")
	}
	body, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return nil, err
	}
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err = dec.Decode(&obj); err != nil {
		return nil, err
	}
	field, textType := "messages", "text"
	if turn.format == "openai-responses" {
		field, textType = "input", "input_text"
	}
	msgs, _ := obj[field].([]any)
	if len(msgs) == 0 {
		return nil, errors.New("协议缺少用户消息")
	}
	last, _ := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "user" {
		return nil, errors.New("图片只能附加到人工消息")
	}
	var blocks []any
	switch content := last["content"].(type) {
	case string:
		blocks = []any{map[string]string{"type": textType, "text": content}}
	case []any:
		blocks = content
	default:
		return nil, errors.New("无法编码图片消息")
	}
	for _, im := range turn.images {
		encoded := base64.StdEncoding.EncodeToString(im.Data)
		switch turn.format {
		case "anthropic":
			blocks = append(blocks, map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": im.MIME, "data": encoded}})
		case "openai":
			blocks = append(blocks, map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:" + im.MIME + ";base64," + encoded}})
		case "openai-responses":
			blocks = append(blocks, map[string]string{"type": "input_image", "image_url": "data:" + im.MIME + ";base64," + encoded})
		default:
			return nil, errors.New("不支持的图片协议")
		}
	}
	last["content"] = blocks
	data, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	out := req.Clone(req.Context())
	out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	out.Body, _ = out.GetBody()
	out.ContentLength = int64(len(data))
	return out, nil
}
