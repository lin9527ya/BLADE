package agent

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestInteractshProtocolPrimitives(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	aesKey := make([]byte, 32)
	_, _ = rand.Read(aesKey)
	block, _ := aes.NewCipher(aesKey)
	plain := []byte(`{"protocol":"dns","full-id":"cb9.example","remote-address":"1.2.3.4:53"}`)
	iv := make([]byte, aes.BlockSize)
	_, _ = rand.Read(iv)
	ct := make([]byte, len(plain))
	cipher.NewCFBEncrypter(block, iv).XORKeyStream(ct, plain)
	encData := base64.StdEncoding.EncodeToString(append(iv, ct...))
	encKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, &key.PublicKey, aesKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decryptInteractsh(key, base64.StdEncoding.EncodeToString(encKey), encData)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(plain) {
		t.Fatalf("decrypt = %q, want %q", got, plain)
	}
}

func TestInteractshURLShape(t *testing.T) {
	s := &interactshSession{CorrelationID: "abcdefghijklmnopqrst", Host: "oast.fun"}
	u := s.URL()
	if len(u) < len(s.CorrelationID)+len(s.Host)+2 {
		t.Fatalf("URL too short: %q", u)
	}
	if u[:len(s.CorrelationID)] != s.CorrelationID {
		t.Fatalf("URL must start with correlation id: %q", u)
	}
}
