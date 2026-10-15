package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 快捷指令「命令内容」的落盘加密（AES-256-GCM）与主密钥管理。
//
// **威胁模型（如实写清，别夸大）**：这一层防的是「拿到磁盘快照 / 备份 / 卷拷贝的人直接
// 读到明文命令」。它**不防**「已经能以同权限读取数据目录的攻击者」——master.key 就躺在
// 密文旁边，能读密文的人多半也能读密钥。想避免密钥与密文同放，用 TERMINAL_MASTER_KEY
// 从外部注入（启动脚本 / 容器 secret 提供），这样数据目录的备份里就没有可用密钥。
//
// AAD 绑定 `terminal:v1:quickcmd:<指令 id>:<表作用域>`：把某条指令的密文剪到另一条指令上
// 会直接解密失败（GCM 的认证标签覆盖 AAD），而不是悄悄把甲的命令接到乙的按钮上（按钮上
// 写的是乙的名字，点下去跑的是甲的内容）。

const (
	// masterKeyLen 是 AES-256 的密钥长度。
	masterKeyLen = 32
	// masterKeyFileName 是主密钥文件名（默认与快捷指令文件同目录）。
	masterKeyFileName = "master.key"
	// secretVersion 是密文信封的版本号。**改动 aad 的组成会让已存的密文全部解不开**，
	// 所以要动就跟着这个版本号一起动。
	secretVersion = 1
	// quickCmdScopeGlobal 是快捷指令表的作用域。本项目只有一张全局表；AAD 里留出这一格是
	// 为了以后的会话专属表（作用域 = 会话 id）能与全局表区分开——把专属表改名成全局表也解不开。
	quickCmdScopeGlobal = "global"
)

// secretEnvelope 是密文在磁盘上的形态：`{"v":1,"ct":"<base64(nonce||ciphertext)>"}`。
type secretEnvelope struct {
	V  int    `json:"v"`
	CT string `json:"ct"`
}

// cryptor 是加密器：只持有 AEAD，无状态，可并发使用（cipher.AEAD 本身并发安全）。
type cryptor struct {
	aead cipher.AEAD
}

// loadCryptor 取得主密钥并构造 AEAD。
//
// 密钥来源二选一：环境变量（外部提供，优先）或 keyPath 指向的主密钥文件。
// 环境变量给了但**解不出来必须直接失败**，绝不「回退到本地文件」：那种静默回退的表现是
// 用户换了密钥后旧命令全部解密失败，而进程却「一切正常」地启动。
//
// 失败由调用方决定怎么处理：本项目**不因此退出进程**（见 main.go）——终端本身是这个 NAS
// 的带外访问入口，为一条快捷指令的密钥把整个终端停掉不划算；但失败时**写路径必须拒绝把
// 明文落盘**（见 quickcmds.go 的 saveQuickCmds）。
func loadCryptor(keyPath, envKey string) (*cryptor, error) {
	var key []byte
	if strings.TrimSpace(envKey) != "" {
		k, err := decodeMasterKey(envKey)
		if err != nil {
			return nil, fmt.Errorf("TERMINAL_MASTER_KEY 不可用：%w", err)
		}
		key = k
	} else {
		k, err := loadOrCreateMasterKey(keyPath)
		if err != nil {
			return nil, err
		}
		key = k
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("初始化 AES 失败：%w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化 AES-GCM 失败：%w", err)
	}
	return &cryptor{aead: aead}, nil
}

// decodeMasterKey 解析外部提供的主密钥：32 字节的 base64（标准/URL 编码都认）或 hex。
func decodeMasterKey(raw string) ([]byte, error) {
	s := strings.TrimSpace(raw)
	if len(s) == hex.EncodedLen(masterKeyLen) {
		if b, err := hex.DecodeString(s); err == nil {
			return b, nil
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == masterKeyLen {
			return b, nil
		}
	}
	return nil, fmt.Errorf("需要 %d 字节的 base64 或 hex 编码密钥", masterKeyLen)
}

// loadOrCreateMasterKey 读取或生成主密钥文件（0600）。
func loadOrCreateMasterKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) != masterKeyLen {
			return nil, fmt.Errorf("%s 长度不是 %d 字节（疑似被截断或损坏），请恢复备份或删除它重新生成（删除后已存的快捷指令内容将无法解密）", path, masterKeyLen)
		}
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取 %s 失败：%w", path, err)
	}

	key := make([]byte, masterKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("生成主密钥失败：%w", err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("创建主密钥目录失败：%w", err)
		}
	}
	// O_EXCL：并发/重复启动时不要互相覆盖密钥（覆盖等于把已存的内容全部变成解不开的密文）。
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return loadOrCreateMasterKey(path)
		}
		return nil, fmt.Errorf("创建 %s 失败：%w", path, err)
	}
	if _, err := f.Write(key); err != nil {
		f.Close()
		os.Remove(path)
		return nil, fmt.Errorf("写入主密钥失败：%w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return nil, fmt.Errorf("写入主密钥失败：%w", err)
	}
	// 飞牛存储卷上 O_CREAT 的模式位不可靠（会落成 0000），必须显式 chmod 一次。
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("设置主密钥权限失败：%w", err)
	}
	return key, nil
}

// aad 是本项目的附加认证数据：版本 + 字段名 + 作用域。
func (c *cryptor) aad(field, scope string) []byte {
	return []byte(fmt.Sprintf("terminal:v%d:%s:%s", secretVersion, field, scope))
}

// encrypt 加密一个字段。明文为空串时返回 nil（表示「没有这个值」）。
//
// crypt 为 nil（主密钥不可用）时返回错误而不是落明文：调用方据此拒绝保存。
func (c *cryptor) encrypt(field, scope, plain string) (*secretEnvelope, error) {
	if c == nil {
		return nil, errors.New("主密钥不可用")
	}
	if plain == "" {
		return nil, nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("生成随机数失败：%w", err)
	}
	ct := c.aead.Seal(nil, nonce, []byte(plain), c.aad(field, scope))
	return &secretEnvelope{V: secretVersion, CT: base64.StdEncoding.EncodeToString(append(nonce, ct...))}, nil
}

// decrypt 解密一个字段。nil / 空密文 = 该字段没有值（返回 ""）。
//
// 解密失败**必须**把错误带出去：静默返回空内容的表现是「我的命令莫名没了」，而真正的原因
// （换了主密钥 / 密文被搬到了别的指令上）永远查不出来，用户一点保存还会把服务端那份覆盖成空的。
func (c *cryptor) decrypt(field, scope string, s *secretEnvelope) (string, error) {
	if c == nil {
		return "", errors.New("主密钥不可用")
	}
	if s == nil || strings.TrimSpace(s.CT) == "" {
		return "", nil
	}
	if s.V != 0 && s.V != secretVersion {
		return "", fmt.Errorf("密文信封版本 v=%d 不受支持", s.V)
	}
	raw, err := base64.StdEncoding.DecodeString(s.CT)
	if err != nil {
		return "", fmt.Errorf("密文不是合法 base64：%w", err)
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns+c.aead.Overhead() {
		return "", errors.New("密文长度不足，文件可能被截断")
	}
	plain, err := c.aead.Open(nil, raw[:ns], raw[ns:], c.aad(field, scope))
	if err != nil {
		return "", fmt.Errorf("解密失败（主密钥变了或密文被搬到了别的指令上）：%w", err)
	}
	return string(plain), nil
}
