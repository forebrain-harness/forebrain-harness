// Package qodercn is the closed-source Qoder CN subscription plugin. It never
// ships in the repository: scripts/gen-subscription-plugins.sh bridges it into
// the binary at build time from a developer's local plugins/subscriptions
// directory.
//
// crypto.go reproduces the qoder_auth_wasm module embedded in the official
// Qoder CN client (v0.5.7 / CLI runtime 1.1.79-hotfix.19) with the Go standard
// library only: the credential file cipher, the per-login runtime auth fields,
// the custom-alphabet payload codec behind Encode=1, the Bearer COSY request
// signature, and the local model-cache cipher. Every function was validated
// byte-for-byte against the original WASM export; the test file pins those
// golden vectors.
package qodercn

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

// qoderRSAPublicKeyPEM is the 1024-bit RSA public key the client's WASM embeds
// (qoder-worker-runtime.obf.mjs). GenerateRuntimeAuthFields seals the one-time
// AES key with it; only the server holds the private half.
const qoderRSAPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

var (
	rsaPublicKeyOnce sync.Once
	rsaPublicKey     *rsa.PublicKey
	rsaPublicKeyErr  error
)

func qoderRSAPublicKey() (*rsa.PublicKey, error) {
	rsaPublicKeyOnce.Do(func() {
		block, _ := pem.Decode([]byte(qoderRSAPublicKeyPEM))
		if block == nil {
			rsaPublicKeyErr = errors.New("qodercn: embedded public key is not PEM")
			return
		}
		pub, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			rsaPublicKeyErr = fmt.Errorf("qodercn: parse embedded public key: %w", err)
			return
		}
		key, ok := pub.(*rsa.PublicKey)
		if !ok {
			rsaPublicKeyErr = fmt.Errorf("qodercn: embedded public key is %T, want RSA", pub)
			return
		}
		rsaPublicKey = key
	})
	return rsaPublicKey, rsaPublicKeyErr
}

// CredentialEncrypt seals plaintext the way the client seals its credential
// file: AES-128-CBC with the key AND the IV both equal to key16 (exactly 16
// bytes, e.g. the first 16 characters of the machine id), PKCS#7 padding,
// standard Base64 output.
func CredentialEncrypt(plaintext, key16 string) (string, error) {
	block, err := aes.NewCipher([]byte(key16))
	if err != nil {
		return "", fmt.Errorf("qodercn: credential key: %w", err)
	}
	padded := pkcs7Pad([]byte(plaintext), aes.BlockSize)
	cipherText := make([]byte, len(padded))
	// The client uses the key itself as the IV — a WASM-verified quirk, kept
	// verbatim for compatibility.
	cipher.NewCBCEncrypter(block, []byte(key16)).CryptBlocks(cipherText, padded)
	return base64.StdEncoding.EncodeToString(cipherText), nil
}

// CredentialDecrypt is CredentialEncrypt's inverse.
func CredentialDecrypt(cipherB64, key16 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cipherB64))
	if err != nil {
		return "", fmt.Errorf("qodercn: credential base64: %w", err)
	}
	block, err := aes.NewCipher([]byte(key16))
	if err != nil {
		return "", fmt.Errorf("qodercn: credential key: %w", err)
	}
	if len(raw) == 0 || len(raw)%aes.BlockSize != 0 {
		return "", errors.New("qodercn: credential ciphertext is not block-sized")
	}
	plain := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, []byte(key16)).CryptBlocks(plain, raw)
	unpadded, err := pkcs7Unpad(plain, aes.BlockSize)
	if err != nil {
		return "", err
	}
	return string(unpadded), nil
}

func pkcs7Pad(data []byte, size int) []byte {
	n := size - len(data)%size
	return append(data, bytes.Repeat([]byte{byte(n)}, n)...)
}

func pkcs7Unpad(data []byte, size int) ([]byte, error) {
	if len(data) == 0 || len(data)%size != 0 {
		return nil, errors.New("qodercn: padded data is not block-sized")
	}
	n := int(data[len(data)-1])
	if n == 0 || n > size || n > len(data) {
		return nil, errors.New("qodercn: bad PKCS#7 padding")
	}
	for _, b := range data[len(data)-n:] {
		if int(b) != n {
			return nil, errors.New("qodercn: bad PKCS#7 padding")
		}
	}
	return data[:len(data)-n], nil
}

// RuntimeAuthInput is the payload GenerateRuntimeAuthFields seals. The field
// order is the wire order — the server reads the JSON the WASM produced, so
// the struct must not be reordered.
type RuntimeAuthInput struct {
	UID              string   `json:"uid"`
	SecurityOAuthTok string   `json:"security_oauth_token"`
	OrganizationID   string   `json:"organization_id,omitempty"`
	OrganizationTags []string `json:"organization_tags,omitempty"`
	DataPolicyAgreed bool     `json:"data_policy_agreed"`
}

// GenerateRuntimeAuthFields produces the two per-login fields every algo
// request carries: the AES-sealed user info (encryptUserInfo) and the RSA-sealed
// one-time AES key (cosyKey). The key is 16 ASCII characters: a random UUIDv4
// minus dashes, truncated.
func GenerateRuntimeAuthFields(in RuntimeAuthInput) (encryptUserInfo, cosyKey string, err error) {
	id, err := newUUIDv4()
	if err != nil {
		return "", "", err
	}
	aesKey16 := strings.ReplaceAll(id, "-", "")[:16]
	payload, err := json.Marshal(in)
	if err != nil {
		return "", "", fmt.Errorf("qodercn: runtime auth payload: %w", err)
	}
	encryptUserInfo, err = CredentialEncrypt(string(payload), aesKey16)
	if err != nil {
		return "", "", err
	}
	pub, err := qoderRSAPublicKey()
	if err != nil {
		return "", "", err
	}
	sealed, err := rsa.EncryptPKCS1v15(rand.Reader, pub, []byte(aesKey16))
	if err != nil {
		return "", "", fmt.Errorf("qodercn: seal runtime auth key: %w", err)
	}
	return encryptUserInfo, base64.StdEncoding.EncodeToString(sealed), nil
}

// The Base64 alphabets behind Encode=1: the standard one the WASM starts from,
// and Qoder's 64-character substitute with '$' padding, applied before the
// three-segment rotation below.
const (
	stdAlpha    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	customAlpha = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
)

var customToStd [256]byte

func init() {
	if len(stdAlpha) != 64 || len(customAlpha) != 64 {
		panic("qodercn: payload alphabets must be 64 characters")
	}
	for i := 0; i < 256; i++ {
		customToStd[i] = 0xFF
	}
	for i, c := range []byte(customAlpha) {
		customToStd[c] = stdAlpha[i]
	}
}

// EncodePayload is the wire codec for every Encode=1 body: substitute the
// custom alphabet into the standard Base64 of the plaintext, then swap the
// first and last thirds of the string (k = len/3).
func EncodePayload(plain []byte) string {
	s := base64.StdEncoding.EncodeToString(plain)
	out := make([]byte, len(s))
	for i, c := range []byte(s) {
		switch {
		case c == '=':
			out[i] = '$'
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '+', c == '/':
			out[i] = customAlpha[strings.IndexByte(stdAlpha, c)]
		default:
			out[i] = c
		}
	}
	k := len(out) / 3
	rotated := make([]byte, 0, len(out))
	rotated = append(rotated, out[len(out)-k:]...)
	rotated = append(rotated, out[k:len(out)-k]...)
	rotated = append(rotated, out[:k]...)
	return string(rotated)
}

// DecodePayload is EncodePayload's inverse. A body that is already plain JSON
// (the server sometimes answers unencoded) passes through untouched.
func DecodePayload(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	// An empty payload encodes from (and decodes to) zero bytes.
	if encoded == "" {
		return nil, nil
	}
	if encoded[0] == '{' || encoded[0] == '[' {
		if json.Valid([]byte(encoded)) {
			return []byte(encoded), nil
		}
	}
	k := len(encoded) / 3
	rotated := make([]byte, 0, len(encoded))
	rotated = append(rotated, encoded[len(encoded)-k:]...)
	rotated = append(rotated, encoded[k:len(encoded)-k]...)
	rotated = append(rotated, encoded[:k]...)

	std := make([]byte, len(rotated))
	for i, c := range []byte(rotated) {
		switch {
		case c == '$':
			std[i] = '='
		default:
			mapped := customToStd[c]
			if mapped == 0xFF {
				return nil, fmt.Errorf("qodercn: payload byte %q is outside the alphabet", string(rune(c)))
			}
			std[i] = mapped
		}
	}
	return base64.StdEncoding.DecodeString(string(std))
}

// cosyBearerJSON is the signed part of the Bearer COSY credential. Field order
// is the wire order.
type cosyBearerJSON struct {
	Version     string `json:"version"`
	RequestID   string `json:"requestId"`
	Info        string `json:"info"`
	CosyVersion string `json:"cosyVersion"`
	IDEVersion  string `json:"ideVersion"`
}

// SignCosyBearer builds the Authorization header every algo request carries:
// a Base64'd identity JSON plus an MD5 over the five-line preimage (identity,
// sealed key, date, encoded body, path). Signing is pure in-memory work — the
// expensive fields were computed once at login.
func SignCosyBearer(requestID, encryptUserInfo, cosyKey, cosyDate, encodedBody, signPath string) string {
	payload, _ := json.Marshal(cosyBearerJSON{
		Version:     "v1",
		RequestID:   requestID,
		Info:        encryptUserInfo,
		CosyVersion: CosyVersion,
		IDEVersion:  "",
	})
	b64Part := base64.StdEncoding.EncodeToString(payload)
	preimage := b64Part + "\n" + cosyKey + "\n" + cosyDate + "\n" + encodedBody + "\n" + signPath
	sum := md5.Sum([]byte(preimage))
	return "Bearer COSY." + b64Part + "." + hex.EncodeToString(sum[:])
}

// The local model cache (~/.qoder-cn/.models/<uid>/catalog-v7) format:
// base64( "QMC\x01" + 12-byte nonce + AES-256-GCM(catalogJSON) ), the key
// HKDF-SHA256-derived from the uid.
const (
	modelCacheMagic     = "QMC\x01"
	modelCacheHKDFSalt  = "qoder-model-cache-enc"
	modelCacheHKDFInfo  = "model-cache-v1"
	modelCacheKeyLength = 32
)

// DecryptModelCache opens one catalog-v7 blob.
func DecryptModelCache(encryptedB64, uid string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encryptedB64))
	if err != nil {
		return nil, fmt.Errorf("qodercn: model cache base64: %w", err)
	}
	if len(raw) < 32 || string(raw[:4]) != modelCacheMagic {
		return nil, errors.New("qodercn: model cache blob is not QMC\\x01 framed")
	}
	key, err := hkdf.Key(sha256.New, []byte(uid), []byte(modelCacheHKDFSalt), modelCacheHKDFInfo, modelCacheKeyLength)
	if err != nil {
		return nil, fmt.Errorf("qodercn: model cache key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, raw[4:16], raw[16:], nil)
	if err != nil {
		return nil, fmt.Errorf("qodercn: model cache open: %w", err)
	}
	return plain, nil
}

// EncryptModelCache seals a blob in the same format; the tests use it to build
// their synthetic catalog vectors without touching a real cache.
func EncryptModelCache(plainJSON []byte, uid string) (string, error) {
	key, err := hkdf.Key(sha256.New, []byte(uid), []byte(modelCacheHKDFSalt), modelCacheHKDFInfo, modelCacheKeyLength)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, plainJSON, nil)
	raw := append([]byte(modelCacheMagic), nonce...)
	raw = append(raw, sealed...)
	return base64.StdEncoding.EncodeToString(raw), nil
}
