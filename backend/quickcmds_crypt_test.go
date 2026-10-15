package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestCryptor 在临时目录里造一个加密器（顺带生成 0600 的 master.key）。
func newTestCryptor(t *testing.T, dir string) *cryptor {
	t.Helper()
	c, err := loadCryptor(filepath.Join(dir, masterKeyFileName), "")
	if err != nil {
		t.Fatalf("loadCryptor: %v", err)
	}
	return c
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// 命令内容里带前导/行尾空格、tab、CRLF、多行与中文的样本：
// 「加解密不得破坏命令的内容和格式」这条承诺必须逐字节成立。
const trickyContent = "  mysql -uroot -pS3cret \\\n\t--execute=\"select 1\"  \r\nprintf '  x  '\n"

func TestQuickCmdsContentEncryptedAtRest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quickcmds.json")
	crypt := newTestCryptor(t, dir)

	orig := []QuickCmd{
		{ID: "c1", Name: "数据库", Content: trickyContent, Auto: true},
		{ID: "c2", Name: "磁盘", Content: "df -h", Auto: false},
	}
	if err := saveQuickCmds(path, crypt, orig); err != nil {
		t.Fatalf("saveQuickCmds: %v", err)
	}

	raw := readFile(t, path)
	// 密文落盘：内容一个字都不该在磁盘上出现；名称/是否自动回车仍是明文（给人看、可排查）。
	for _, secret := range []string{"S3cret", "mysql -uroot", "select 1", "df -h"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("磁盘上出现了明文命令内容 %q：\n%s", secret, raw)
		}
	}
	if !strings.Contains(raw, "数据库") || !strings.Contains(raw, "磁盘") {
		t.Fatalf("名称应保持明文：\n%s", raw)
	}

	var f quickCmdsFile
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		t.Fatalf("磁盘格式不是合法 JSON：%v", err)
	}
	if f.Version != 2 {
		t.Fatalf("版本号应为 2，得到 %d", f.Version)
	}
	if len(f.Commands) != 2 || f.Commands[0].Content == nil || f.Commands[0].Content.V != secretVersion {
		t.Fatalf("content 应是版本 %d 的信封：%+v", secretVersion, f.Commands)
	}

	// 重启（同一把主密钥重新加载）后读回，内容逐字节相同。
	again := newTestCryptor(t, dir)
	got, err := loadQuickCmds(path, again)
	if err != nil {
		t.Fatalf("loadQuickCmds: %v", err)
	}
	if len(got) != 2 || got[0].Content != trickyContent || got[0].Auto != true || got[1].Content != "df -h" {
		t.Fatalf("往返后内容/auto 变了：%+v", got)
	}

	// 文件权限：密文旁边就是主密钥，没理由让同机其他用户读。
	for _, p := range []string{path, filepath.Join(dir, masterKeyFileName)} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("%s 权限应为 0600，得到 %o", p, st.Mode().Perm())
		}
	}
}

func TestQuickCmdsLegacyPlaintextFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quickcmds.json")
	crypt := newTestCryptor(t, dir)

	// 版本 1（旧版）落盘形态：content 是明文字符串。
	legacy := map[string]any{
		"version": 1,
		"commands": []map[string]any{
			{"id": "legacy01", "name": "旧表", "content": trickyContent, "auto": true},
			{"id": "legacy02", "name": "已经加密的", "content": "keep", "auto": false},
		},
	}
	b, _ := json.Marshal(legacy)
	writeFile(t, path, string(b))

	got, err := loadQuickCmds(path, crypt)
	if err != nil {
		t.Fatalf("旧明文文件应能照常读出：%v", err)
	}
	if len(got) != 2 || got[0].Content != trickyContent || got[0].Auto != true {
		t.Fatalf("读出的内容/auto 不对：%+v", got)
	}

	// 读一次就该就地加密重写：磁盘上不再有明文内容，版本升到 2。
	raw := readFile(t, path)
	if strings.Contains(raw, "S3cret") || strings.Contains(raw, "select 1") {
		t.Fatalf("迁移后磁盘上仍有明文内容：\n%s", raw)
	}
	var f quickCmdsFile
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		t.Fatal(err)
	}
	if f.Version != 2 {
		t.Fatalf("迁移后版本号应为 2，得到 %d", f.Version)
	}

	// 再读一次：内容与格式逐字节不变（真实重启路径）。
	again := newTestCryptor(t, dir)
	got2, err := loadQuickCmds(path, again)
	if err != nil {
		t.Fatalf("迁移后重读失败：%v", err)
	}
	if got2[0].Content != trickyContent || got2[0].Auto != true {
		t.Fatalf("迁移改动了内容或 auto：%+v", got2)
	}
}

func TestQuickCmdsLegacyBareArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quickcmds.json")
	crypt := newTestCryptor(t, dir)

	// 比版本 1 更早的形态：顶层就是一个裸数组。
	writeFile(t, path, `[{"id":"bare01","name":"裸数组","content":"echo  裸数组  ","auto":true}]`)

	got, err := loadQuickCmds(path, crypt)
	if err != nil {
		t.Fatalf("裸数组旧文件应能照常读出：%v", err)
	}
	if len(got) != 1 || got[0].Content != "echo  裸数组  " || !got[0].Auto {
		t.Fatalf("裸数组内容不对：%+v", got)
	}
	raw := readFile(t, path)
	if strings.Contains(raw, "裸数组  ") && !strings.Contains(raw, `"ct"`) {
		t.Fatalf("裸数组文件没有被迁移成密文：\n%s", raw)
	}
	if !strings.Contains(raw, `"version": 2`) {
		t.Fatalf("迁移后应写成版本 2 整表信封：\n%s", raw)
	}
}

// 启动迁移：不打任何接口、不读列表，磁盘上的明文也必须被加密掉——
// 前端只在打开快捷指令弹窗时才加载列表，用户几个月不点它就永远迁不掉。
func TestQuickCmdsLegacyMigrationAtStartup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quickcmds.json")
	crypt := newTestCryptor(t, dir)

	legacy := map[string]any{
		"version": 1,
		"commands": []map[string]any{
			{"id": "g1", "name": "多行", "content": trickyContent, "auto": true},
			{"id": "g2", "name": "空内容", "content": "", "auto": true},
		},
	}
	b, _ := json.Marshal(legacy)
	writeFile(t, path, string(b))

	renv := &RuntimeEnv{QuickCmdsFile: path, MasterKeyFile: filepath.Join(dir, masterKeyFileName)}
	if err := migrateQuickCmdsAtStartup(renv, crypt); err != nil {
		t.Fatalf("migrateQuickCmdsAtStartup: %v", err)
	}

	raw := readFile(t, path)
	if strings.Contains(raw, "S3cret") || strings.Contains(raw, quickCmdMigrationPlaceholder) {
		t.Fatalf("迁移后磁盘上仍有明文：\n%s", raw)
	}
	if !strings.Contains(raw, `"version": 2`) {
		t.Fatalf("迁移后应写成版本 2：\n%s", raw)
	}

	got, err := loadQuickCmds(path, crypt)
	if err != nil {
		t.Fatalf("迁移后读取失败：%v", err)
	}
	if got[0].Content != trickyContent || got[0].Auto != true {
		t.Fatalf("多行内容/auto 没保住：%+v", got[0])
	}
	// 空内容拿不回来（空内容存不下来），换成占位文案并把 auto 关掉：
	// 一句中文提示不该被「点一下就自动回车」当成命令跑出去。
	if got[1].Content != quickCmdMigrationPlaceholder || got[1].Auto {
		t.Fatalf("空内容条目应换成占位文案且 auto=false：%+v", got[1])
	}
}

func TestQuickCmdsDecryptFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quickcmds.json")
	crypt := newTestCryptor(t, dir)

	cmds := []QuickCmd{
		{ID: "a", Name: "甲", Content: "echo A", Auto: true},
		{ID: "b", Name: "乙", Content: "echo B", Auto: false},
	}
	if err := saveQuickCmds(path, crypt, cmds); err != nil {
		t.Fatal(err)
	}

	t.Run("密文被改一个字符", func(t *testing.T) {
		raw := readFile(t, path)
		// 只动密文里的一个字符（保证仍是合法 base64 形状）。
		idx := strings.Index(raw, `"ct"`)
		pos := idx + len(`"ct": "`) + 4
		orig := raw[pos]
		repl := byte('A')
		if orig == 'A' {
			repl = 'B'
		}
		tampered := raw[:pos] + string(repl) + raw[pos+1:]
		writeFile(t, path, tampered)
		defer writeFile(t, path, raw)

		_, err := loadQuickCmds(path, crypt)
		if err == nil {
			t.Fatal("密文被篡改时必须报错，不能静默给空内容")
		}
		if !strings.Contains(err.Error(), "甲") || !strings.Contains(err.Error(), "(a)") {
			t.Fatalf("错误里应带上指令名与 id，便于排查：%v", err)
		}
	})

	t.Run("两条指令的密文互换", func(t *testing.T) {
		var f quickCmdsFile
		raw := readFile(t, path)
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			t.Fatal(err)
		}
		f.Commands[0].Content, f.Commands[1].Content = f.Commands[1].Content, f.Commands[0].Content
		swapped, _ := json.MarshalIndent(f, "", "  ")
		writeFile(t, path, string(swapped))
		defer writeFile(t, path, raw)

		// AAD 绑定了指令 id：互换后必然解不开（否则按钮上写着乙、点下去跑的是甲的命令）。
		if _, err := loadQuickCmds(path, crypt); err == nil {
			t.Fatal("密文互换必须解密失败")
		}
	})

	t.Run("换了主密钥", func(t *testing.T) {
		otherDir := t.TempDir()
		other := newTestCryptor(t, otherDir)
		if _, err := loadQuickCmds(path, other); err == nil {
			t.Fatal("换主密钥后必须解密失败（不能静默给空内容）")
		}
	})
}

// 命令内容一律原样保留：只把「全是空白」判成空。
func TestQuickCmdsContentNotTrimmed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quickcmds.json")
	crypt := newTestCryptor(t, dir)

	in := []QuickCmd{{ID: "x", Name: "  名字  ", Content: trickyContent, Auto: false}}
	cmds, err := sanitizeQuickCmds(in)
	if err != nil {
		t.Fatalf("sanitizeQuickCmds: %v", err)
	}
	if cmds[0].Name != "名字" {
		t.Fatalf("名称应 trim：%q", cmds[0].Name)
	}
	if cmds[0].Content != trickyContent {
		t.Fatalf("命令内容不得 trim：%q", cmds[0].Content)
	}
	if err := saveQuickCmds(path, crypt, cmds); err != nil {
		t.Fatal(err)
	}
	got, err := loadQuickCmds(path, crypt)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Content != trickyContent {
		t.Fatalf("落盘往返后内容变了：%q", got[0].Content)
	}

	if _, err := sanitizeQuickCmds([]QuickCmd{{ID: "y", Name: "空", Content: " \t\n "}}); err == nil {
		t.Fatal("全是空白的命令内容应被拒绝")
	}
}

// 主密钥不可用（master.key 损坏/丢失等）时：进程照常跑，但**绝不退化成写明文**，
// 也**不会把那份明文当成「读取成功」端出去**（那等于悄悄违背「磁盘上不留明文」这条改动）。
func TestQuickCmdsRefusesPlaintextWithoutCryptor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quickcmds.json")

	cmds := []QuickCmd{{ID: "n1", Name: "无密钥", Content: "secret-cmd", Auto: true}}
	if err := saveQuickCmds(path, nil, cmds); err == nil {
		t.Fatal("主密钥不可用时保存必须报错")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("主密钥不可用时不得写出任何文件：%v", err)
	}

	// 还没有任何指令（文件不存在 / 空表）：与加密无关，照常返回空列表。
	if got, err := loadQuickCmds(path, nil); err != nil || len(got) != 0 {
		t.Fatalf("空表应与主密钥无关：%+v, %v", got, err)
	}

	// 旧明文文件：缺主密钥时**不返回明文**，也不动文件（修好密钥后重试即可）。
	legacy := `{"version":1,"commands":[{"id":"n2","name":"旧","content":"legacy-cmd","auto":false}]}`
	writeFile(t, path, legacy)
	if _, err := loadQuickCmds(path, nil); err == nil {
		t.Fatal("缺主密钥时不得把明文命令内容当读取成功返回")
	}
	if raw := readFile(t, path); raw != legacy {
		t.Fatalf("缺主密钥时不该动文件：\n%s", raw)
	}

	// 已加密的文件：明确报错，不能给空内容。
	encrypted := `{"version":2,"commands":[{"id":"n3","name":"密","auto":false,"content":{"v":1,"ct":"AAAA"}}]}`
	writeFile(t, path, encrypted)
	if _, err := loadQuickCmds(path, nil); err == nil {
		t.Fatal("缺主密钥时读加密文件必须报错")
	}
}

func TestMasterKeyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, masterKeyFileName)
	c1, err := loadCryptor(keyPath, "")
	if err != nil {
		t.Fatal(err)
	}
	// 第二次加载必须复用同一把密钥（否则密文全解不开）。
	c2, err := loadCryptor(keyPath, "")
	if err != nil {
		t.Fatal(err)
	}
	env, err := c1.encrypt(quickCmdAADField("id1"), quickCmdScopeGlobal, "hello 世界")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := c2.decrypt(quickCmdAADField("id1"), quickCmdScopeGlobal, env)
	if err != nil || plain != "hello 世界" {
		t.Fatalf("同一把密钥应能解开：%q, %v", plain, err)
	}
	// AAD 绑定指令 id：搬到别的指令上解不开。
	if _, err := c2.decrypt(quickCmdAADField("id2"), quickCmdScopeGlobal, env); err == nil {
		t.Fatal("密文搬到别的指令 id 上必须解不开")
	}
	// 外部注入的主密钥（base64）：与环境变量路径一致地可用。
	if _, err := loadCryptor(filepath.Join(dir, "unused.key"), "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="); err != nil {
		t.Fatalf("合法的 base64 主密钥应可用：%v", err)
	}
	if _, err := loadCryptor(keyPath, "not-a-key"); err == nil {
		t.Fatal("非法的 TERMINAL_MASTER_KEY 必须直接失败，不得回退到本地文件")
	}
}

// 加密后写出的内容里不应残留明文的 base64「意外可读」形式也无所谓：
// 这里只确认密文长度符合 nonce+GCM 开销，避免「其实什么都没加」。
func TestSecretEnvelopeShape(t *testing.T) {
	dir := t.TempDir()
	c := newTestCryptor(t, dir)
	env, err := c.encrypt("f", "s", strings.Repeat("x", 10))
	if err != nil {
		t.Fatal(err)
	}
	if env.V != secretVersion {
		t.Fatalf("信封版本应为 %d：%+v", secretVersion, env)
	}
	if bytes.Contains([]byte(env.CT), []byte(strings.Repeat("x", 10))) {
		t.Fatal("密文里不该出现明文")
	}
	empty, err := c.encrypt("f", "s", "")
	if err != nil || empty != nil {
		t.Fatalf("空明文应返回 nil 信封：%+v, %v", empty, err)
	}
}
