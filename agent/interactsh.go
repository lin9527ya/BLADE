package agent

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 最小 interactsh 客户端：只做注册 / 轮询 / 解密，不引入 ProjectDiscovery 依赖。
// 协议与官方客户端一致：RSA-OAEP(SHA-256) 解出 AES key，数据为 AES-CFB（IV 前置）。
// 用于 requestrepo 不可用时回退到 oast.fun 等公共 interactsh 服务器。

type interactshSession struct {
	Server        string
	CorrelationID string
	Secret        string
	Key           *rsa.PrivateKey
	Host          string
	CreatedAt     time.Time
}

func randomToken(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return strings.Repeat("0", n)
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf)
}

func normalizeInteractshServer(server string) string {
	server = strings.TrimSpace(server)
	if server == "" {
		return ""
	}
	if !strings.HasPrefix(server, "http://") && !strings.HasPrefix(server, "https://") {
		server = "https://" + server
	}
	return strings.TrimRight(server, "/")
}

func newInteractshSession(ctx context.Context, server string) (*interactshSession, error) {
	server = normalizeInteractshServer(server)
	if server == "" {
		return nil, fmt.Errorf("interactsh server is empty")
	}
	u, err := url.Parse(server)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("bad interactsh server %q", server)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: pubDER})
	s := &interactshSession{
		Server: server, CorrelationID: randomToken(20), Secret: randomToken(32),
		Key: key, Host: u.Host, CreatedAt: time.Now(),
	}
	payload, _ := json.Marshal(map[string]string{
		"public-key":     base64.StdEncoding.EncodeToString(pubPEM),
		"secret-key":     s.Secret,
		"correlation-id": s.CorrelationID,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+"/register", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s/register HTTP %d：%s", server, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var reg struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &reg)
	if reg.Message != "" && !strings.Contains(strings.ToLower(reg.Message), "success") {
		return nil, fmt.Errorf("%s/register：%s", server, reg.Message)
	}
	return s, nil
}

// URL 生成一个带随机 nonce 的回调主机名（官方客户端同形：correlationID+nonce.host）。
func (s *interactshSession) URL() string {
	return s.CorrelationID + randomToken(13) + "." + s.Host
}

func (s *interactshSession) Poll(ctx context.Context) ([]oastRequest, error) {
	endpoint := fmt.Sprintf("%s/poll?id=%s&secret=%s", s.Server, url.QueryEscape(s.CorrelationID), url.QueryEscape(s.Secret))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 12 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s/poll HTTP %d：%s", s.Server, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Data    []string `json:"data"`
		Extra   []string `json:"extra"`
		AESKey  string   `json:"aes_key"`
		TLDData []string `json:"tlddata"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	type interaction struct {
		Protocol      string    `json:"protocol"`
		UniqueID      string    `json:"unique-id"`
		FullID        string    `json:"full-id"`
		RawRequest    string    `json:"raw-request"`
		RemoteAddress string    `json:"remote-address"`
		Timestamp     time.Time `json:"timestamp"`
	}
	out := []oastRequest{}
	appendInteraction := func(raw []byte) {
		var it interaction
		if json.Unmarshal(raw, &it) != nil {
			return
		}
		id := it.UniqueID + "|" + it.FullID + "|" + it.Timestamp.UTC().Format(time.RFC3339Nano)
		if id == "||" {
			id = randomToken(16)
		}
		req := oastRequest{
			ID: id, Type: it.Protocol, Date: it.Timestamp.Unix(),
			Domain: it.FullID, URL: "http://" + it.FullID,
			IP: it.RemoteAddress,
		}
		if fields := strings.Fields(it.RawRequest); len(fields) >= 2 {
			req.Method = fields[0]
			req.Path = fields[1]
		}
		out = append(out, req)
	}
	for _, data := range payload.Data {
		raw, err := decryptInteractsh(s.Key, payload.AESKey, data)
		if err == nil {
			appendInteraction(raw)
		}
	}
	for _, data := range append(append([]string{}, payload.Extra...), payload.TLDData...) {
		appendInteraction([]byte(data))
	}
	return out, nil
}

func decryptInteractsh(key *rsa.PrivateKey, aesKeyB64, data string) ([]byte, error) {
	encKey, err := base64.StdEncoding.DecodeString(aesKeyB64)
	if err != nil {
		return nil, err
	}
	keyPlain, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, key, encKey, nil)
	if err != nil {
		return nil, err
	}
	cipherText, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(keyPlain)
	if err != nil {
		return nil, err
	}
	if len(cipherText) < aes.BlockSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	iv, cipherText := cipherText[:aes.BlockSize], cipherText[aes.BlockSize:]
	// 新版 interactsh 用 AES-CTR；老版用 AES-CFB。先试 CTR，解出来不是合法 JSON 再退回 CFB。
	ctr := make([]byte, len(cipherText))
	cipher.NewCTR(block, iv).XORKeyStream(ctr, cipherText)
	if json.Valid(ctr) {
		return ctr, nil
	}
	cfb := make([]byte, len(cipherText))
	//lint:ignore SA1019 兼容旧版 interactsh 服务端：仅在 CTR 解出非 JSON 时回退
	cipher.NewCFBDecrypter(block, iv).XORKeyStream(cfb, cipherText)
	if json.Valid(cfb) {
		return cfb, nil
	}
	return ctr, nil
}
