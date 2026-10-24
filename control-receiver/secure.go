package main

// 传输层加密通道（v1.2.0，对齐 QuestPhoneMirror #25 加密通道）。
//
// 与 QPM app 的 Crypto.kt / SecureChannel.kt 逐位对齐：
//   - 双方各生成 EC(secp256r1) 临时密钥对，用 X.509(SPKI) 编码的公钥通过
//     KEY_EXCHANGE(0x1E) 帧互换（KE 帧本身永不明文加密之外的改动——永远明文发送）；
//   - ECDH 共享密钥过 SHA-256 得到 AES-256 会话密钥；
//   - 帧体 AES/GCM/NoPadding（128-bit tag）加密，nonce 隐式生成：
//     高 4 字节大端方向（0=手机→接收端，1=接收端→手机），低 8 字节大端同方向帧序号；
//   - TCP 有序且双方向各自独立计数，收发两端计数天然同步；方向位保证同一份
//     密钥材料两个方向的 nonce 不重叠（GCM 严禁 nonce 重用）。
//
// 只加密帧体：[type:u8][length:u32 LE] 头保持明文，上层协议帧解析完全不变。
// 未武装（握手前/失败回落）时加解密都原样透传。

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"sync"
)

// 方向常量：与 QPM Crypto.kt 的 DIR_PHONE / DIR_MIRROR 一致，参与 nonce 构成。
const (
	dirPhoneToMirror = 0 // 手机发送端 → 接收端（本插件解密对端帧用）
	dirMirrorToPhone = 1 // 接收端 → 手机发送端（本插件加密发出帧用）
)

// ecKeyPair 一方临时密钥对。spki 为 X.509(SubjectPublicKeyInfo) 编码的公钥，
// 即 KE 帧的载荷——与 Java KeyPair.getPublic().getEncoded() 同构，可直接互换。
type ecKeyPair struct {
	spki []byte
	priv *ecdh.PrivateKey
}

func newECKeyPair() (*ecKeyPair, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	spki, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		return nil, err
	}
	priv, err := k.ECDH()
	if err != nil {
		return nil, err
	}
	return &ecKeyPair{spki: spki, priv: priv}, nil
}

// parsePeerPublic 解析对端 X.509 编码的 EC 公钥（Java KeyFactory("EC") 的输出）。
func parsePeerPublic(der []byte) (*ecdh.PublicKey, error) {
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	ep, ok := pub.(*ecdsa.PublicKey)
	if !ok || ep.Curve != elliptic.P256() {
		return nil, errors.New("对端公钥不是 EC(secp256r1) 密钥")
	}
	return ep.ECDH()
}

// deriveSessionKey ECDH → SHA-256 → AES-256 会话密钥（32 字节）。
func deriveSessionKey(self *ecKeyPair, peerDER []byte) ([]byte, error) {
	peer, err := parsePeerPublic(peerDER)
	if err != nil {
		return nil, err
	}
	shared, err := self.priv.ECDH(peer)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(shared)
	return sum[:], nil
}

// gcmNonce nonce = [dir:u32 BE][counter:u64 BE]，共 12 字节。
func gcmNonce(dir int, counter uint64) []byte {
	n := make([]byte, 12)
	binary.BigEndian.PutUint32(n[0:4], uint32(dir))
	binary.BigEndian.PutUint64(n[4:12], counter)
	return n
}

// secureChannel 帧体加解密单点。调用方约定：
//   - seal 与 socket 写出必须在同一把写锁（a.writeMu）内原子完成，
//     否则计数器自增顺序与 wire 顺序错位会让对端 GCM 校验失败；
//   - open 只在读循环单协程调用。
type secureChannel struct {
	mu          sync.Mutex
	key         []byte
	sendCounter uint64
	recvCounter uint64
	armed       bool
	broken      bool
}

func (sc *secureChannel) arm(key []byte) {
	sc.mu.Lock()
	sc.key = key
	sc.sendCounter = 0
	sc.recvCounter = 0
	sc.armed = true
	sc.broken = false
	sc.mu.Unlock()
}

func (sc *secureChannel) isArmed() bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.armed && !sc.broken
}

// seal 加密一整帧的 wire 字节（[type][len LE][body] → 同头 + 密文）。
// 未武装/已降级时原样返回；任何异常都降级透传，绝不外抛。
func (sc *secureChannel) seal(wire []byte) []byte {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if !sc.armed || sc.broken || len(wire) < 5 {
		return wire
	}
	bodyLen := int(binary.LittleEndian.Uint32(wire[1:5]))
	if 5+bodyLen > len(wire) {
		return wire
	}
	plain := wire[5 : 5+bodyLen]
	block, err := aes.NewCipher(sc.key)
	if err != nil {
		sc.broken = true
		return wire
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		sc.broken = true
		return wire
	}
	cipher := aead.Seal(nil, gcmNonce(dirMirrorToPhone, sc.sendCounter), plain, nil)
	sc.sendCounter++
	out := make([]byte, 5+len(cipher))
	out[0] = wire[0]
	binary.LittleEndian.PutUint32(out[1:5], uint32(len(cipher)))
	copy(out[5:], cipher)
	return out
}

// open 解密一帧的帧体；未武装/已降级时原样返回。解密失败（tag 校验不过等）
// 时降级透传并返回错误，由调用方决定是否断开——计数错位只会发生在协议被破坏时。
func (sc *secureChannel) open(typ byte, body []byte) (byte, []byte, error) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if !sc.armed || sc.broken {
		return typ, body, nil
	}
	block, err := aes.NewCipher(sc.key)
	if err != nil {
		sc.broken = true
		return typ, body, nil
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		sc.broken = true
		return typ, body, nil
	}
	plain, err := aead.Open(nil, gcmNonce(dirPhoneToMirror, sc.recvCounter), body, nil)
	if err != nil {
		// 不推进计数器：降级透传本帧，后续帧计数同样对不上，连接实际上已不可信
		sc.broken = true
		return typ, body, err
	}
	sc.recvCounter++
	return typ, plain, nil
}
