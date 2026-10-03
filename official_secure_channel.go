package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

const (
	officialSecureChannelVersion  = "v1"
	officialSecureEnvelopeVersion = byte(0x01)
	officialSecureNonceSize       = 12
	officialSecureHeaderSize      = 9
	officialSecureTagSize         = 16
)

var officialSecureRuntimePublicKey = [curve25519.PointSize]byte{
	0xfb, 0x74, 0x41, 0xfc, 0xbb, 0xa8, 0xa4, 0x3b,
	0x6a, 0x5b, 0x05, 0x81, 0x88, 0x87, 0x03, 0x4f,
	0x62, 0x2f, 0xc3, 0xae, 0x11, 0x31, 0x3f, 0xb5,
	0xd2, 0xf4, 0x7b, 0x96, 0xc3, 0xf2, 0xd4, 0x5d,
}

type officialSecureChannel struct {
	sessionID string
	send      cipher.AEAD
	receive   cipher.AEAD
	sendNonce [officialSecureNonceSize]byte
	recvNonce [officialSecureNonceSize]byte
	sendSeq   atomic.Uint64
	recvMu    sync.Mutex
	recvMax   uint64
	recvBits  uint64
}

func openOfficialSecureChannel(ctxRequest *http.Request, client *http.Client, target *url.URL) (*officialSecureChannel, error) {
	privateKey := make([]byte, curve25519.ScalarSize)
	if _, err := rand.Read(privateKey); err != nil {
		return nil, fmt.Errorf("generate secure-channel key: %w", err)
	}
	publicKey, err := curve25519.X25519(privateKey, curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("generate secure-channel public key: %w", err)
	}
	audience := target.Scheme + "://" + target.Host
	payload, err := json.Marshal(map[string]string{
		"client_pub_b64": base64.StdEncoding.EncodeToString(publicKey),
		"audience":       audience,
		"proto":          "v2",
	})
	if err != nil {
		return nil, err
	}
	endpoint := *target
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/api/securechan/handshake"
	endpoint.RawQuery = ""
	request, err := http.NewRequestWithContext(ctxRequest.Context(), http.MethodPost, endpoint.String(), strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	request.Host = target.Host
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("secure-channel handshake: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil {
		return nil, fmt.Errorf("read secure-channel handshake: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("secure-channel handshake returned HTTP %d", response.StatusCode)
	}
	if len(raw) > 4096 {
		return nil, errors.New("secure-channel handshake response too large")
	}
	var result struct {
		Proto        string `json:"proto"`
		SessionID    string `json:"session_id"`
		ServerPubB64 string `json:"server_pub_b64"`
		RuntimeProof string `json:"runtime_proof"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Proto != "v2" || strings.TrimSpace(result.SessionID) == "" || strings.TrimSpace(result.RuntimeProof) == "" {
		return nil, errors.New("secure-channel handshake returned invalid JSON")
	}
	serverPublicKey, err := base64.StdEncoding.DecodeString(result.ServerPubB64)
	if err != nil || len(serverPublicKey) != curve25519.PointSize {
		return nil, errors.New("secure-channel handshake returned invalid server key")
	}
	sharedSecret, err := curve25519.X25519(privateKey, serverPublicKey)
	if err != nil {
		return nil, fmt.Errorf("derive secure-channel shared secret: %w", err)
	}
	runtimeSharedSecret, err := curve25519.X25519(privateKey, officialSecureRuntimePublicKey[:])
	if err != nil {
		return nil, fmt.Errorf("derive secure-channel runtime secret: %w", err)
	}
	keyMaterial := append(append(make([]byte, 0, len(sharedSecret)+len(runtimeSharedSecret)), sharedSecret...), runtimeSharedSecret...)
	info := []byte("securechan-v2\n" + result.SessionID)
	return deriveOfficialSecureChannel(result.SessionID, keyMaterial, publicKey, serverPublicKey, info, false)
}

func deriveOfficialSecureChannel(sessionID string, sharedSecret, clientPublicKey, serverPublicKey, info []byte, server bool) (*officialSecureChannel, error) {
	salt := append(append(make([]byte, 0, len(clientPublicKey)+len(serverPublicKey)), clientPublicKey...), serverPublicKey...)
	reader := hkdf.New(sha256.New, sharedSecret, salt, info)
	masterKey := make([]byte, 32)
	agentKey := make([]byte, 32)
	masterNonce := make([]byte, officialSecureNonceSize)
	agentNonce := make([]byte, officialSecureNonceSize)
	for _, output := range [][]byte{masterKey, agentKey, masterNonce, agentNonce} {
		if _, err := io.ReadFull(reader, output); err != nil {
			return nil, fmt.Errorf("derive secure-channel keys: %w", err)
		}
	}
	sendKey, receiveKey := agentKey, masterKey
	sendNonce, receiveNonce := agentNonce, masterNonce
	if server {
		sendKey, receiveKey = masterKey, agentKey
		sendNonce, receiveNonce = masterNonce, agentNonce
	}
	sendBlock, err := aes.NewCipher(sendKey)
	if err != nil {
		return nil, err
	}
	receiveBlock, err := aes.NewCipher(receiveKey)
	if err != nil {
		return nil, err
	}
	sendCipher, err := cipher.NewGCM(sendBlock)
	if err != nil {
		return nil, err
	}
	receiveCipher, err := cipher.NewGCM(receiveBlock)
	if err != nil {
		return nil, err
	}
	channel := &officialSecureChannel{sessionID: strings.TrimSpace(sessionID), send: sendCipher, receive: receiveCipher}
	copy(channel.sendNonce[:], sendNonce)
	copy(channel.recvNonce[:], receiveNonce)
	return channel, nil
}

func (c *officialSecureChannel) encrypt(plaintext []byte) []byte {
	sequence := c.sendSeq.Add(1)
	nonce := officialSecureNonce(c.sendNonce, sequence)
	ciphertext := c.send.Seal(nil, nonce[:], plaintext, nil)
	envelope := make([]byte, officialSecureHeaderSize+len(ciphertext))
	envelope[0] = officialSecureEnvelopeVersion
	binary.BigEndian.PutUint64(envelope[1:officialSecureHeaderSize], sequence)
	copy(envelope[officialSecureHeaderSize:], ciphertext)
	return envelope
}

func (c *officialSecureChannel) decrypt(envelope []byte) ([]byte, error) {
	if len(envelope) < officialSecureHeaderSize+officialSecureTagSize || envelope[0] != officialSecureEnvelopeVersion {
		return nil, errors.New("invalid secure-channel envelope")
	}
	sequence := binary.BigEndian.Uint64(envelope[1:officialSecureHeaderSize])
	c.recvMu.Lock()
	accepted := c.acceptSequence(sequence)
	c.recvMu.Unlock()
	if !accepted {
		return nil, errors.New("replayed secure-channel envelope")
	}
	nonce := officialSecureNonce(c.recvNonce, sequence)
	plaintext, err := c.receive.Open(nil, nonce[:], envelope[officialSecureHeaderSize:], nil)
	if err != nil {
		return nil, errors.New("decrypt secure-channel response")
	}
	return plaintext, nil
}

func (c *officialSecureChannel) acceptSequence(sequence uint64) bool {
	if sequence == 0 {
		return false
	}
	if sequence > c.recvMax {
		shift := sequence - c.recvMax
		if shift >= 64 {
			c.recvBits = 0
		} else {
			c.recvBits <<= shift
		}
		c.recvMax = sequence
		c.recvBits |= 1
		return true
	}
	difference := c.recvMax - sequence
	if difference >= 64 || c.recvBits&(uint64(1)<<difference) != 0 {
		return false
	}
	c.recvBits |= uint64(1) << difference
	return true
}

func officialSecureNonce(base [officialSecureNonceSize]byte, sequence uint64) [officialSecureNonceSize]byte {
	nonce := base
	var sequenceBytes [officialSecureNonceSize]byte
	binary.BigEndian.PutUint64(sequenceBytes[4:], sequence)
	for index := range nonce {
		nonce[index] ^= sequenceBytes[index]
	}
	return nonce
}
