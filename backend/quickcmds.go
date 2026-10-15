package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// 快捷指令（命令内容加密落盘）
//
// 磁盘格式**版本 2**：`content` 是密文信封 `{"v":1,"ct":"<base64(nonce||ciphertext)>"}`，
// `id` / `name` / `auto` 仍是明文——用户往快捷指令里放的常常是 `mysql -uroot -p<密码>`、
// 带 token 的 curl 这类东西，而拿到磁盘快照/备份的人不该顺手读到它们；名称是给人看的标签，
// 加密它反而让文件没法排查（威胁模型见 secret.go 的包注释）。
//
// 加密边界只在**落盘这一层**：API 收发的始终是明文 `content`，前端完全不需要知道这件事。
//
// 旧文件（版本 1 的明文内容，甚至更早的裸数组）照常能读，并且会被**主动迁移**成密文：
// 读一张表时就地加密重写，进程启动时还会再迁一次（见 migrateQuickCmdsAtStartup）——
// 前端只在打开弹窗时才加载列表，光靠读时迁移兜不住「用户几个月没打开过快捷指令弹窗」。
// ---------------------------------------------------------------------------

// quickCmdMigrationPlaceholder 是「明文内容加密失败」时替换进去的文案（它自己也是密文落盘）。
//
// 为什么要一个可见的占位而不是清成空串：空内容在界面上看不出来被清过，用户会以为是自己记错了；
// 而这条内容已经拿不回来了（加密都失败的机器上，原明文也不该再留在磁盘上）。它是**落盘的数据**
// 而不是界面文案，所以故意不进 i18n 字典——用户以后把界面切成英文，磁盘上那句话也不该跟着变。
const quickCmdMigrationPlaceholder = "命令加密失败已清空，请重新添加"

// QuickCmd is a user-defined terminal quick command as exchanged with the API
// (`content` 是**明文**；加密只发生在落盘那一层）。
type QuickCmd struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Auto    bool   `json:"auto"`
}

// quickCmdStored 是一条指令在磁盘上的形态。
type quickCmdStored struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Auto bool   `json:"auto"`
	// Content 是加密后的命令内容。nil 表示这条指令没有内容（sanitize 拒绝了空内容，正常数据
	// 里不会出现；手工编辑过的文件可能出现，那时照「没有内容」处理）。
	Content *secretEnvelope `json:"content"`
	// plain / legacy 只在读到**版本 1**（明文）文件时有意义，见 UnmarshalJSON。
	// 两个字段都未导出 → 不参与 json.Marshal，也就不会把明文写回磁盘。
	plain  string
	legacy bool
}

// UnmarshalJSON 兼容旧格式的明文内容（`"content": "df -h"`）。
//
// 按 JSON 类型分流：字符串 = 版本 1 的明文，对象 = 版本 2 的密文信封——两种形态不会歧义。
// 读到明文会打上 `legacy` 标记，由 migrateQuickCmds 加密后重写（**不做**「读到就一直用明文、
// 等下次保存再说」那种懒迁移：用户可能几个月都不点一次保存）。
func (c *quickCmdStored) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID      string          `json:"id"`
		Name    string          `json:"name"`
		Auto    bool            `json:"auto"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.ID, c.Name, c.Auto, c.Content, c.plain, c.legacy = raw.ID, raw.Name, raw.Auto, nil, "", false
	v := bytes.TrimSpace(raw.Content)
	switch {
	case len(v) == 0 || bytes.Equal(v, []byte("null")):
		return nil
	case v[0] == '"':
		c.legacy = true
		return json.Unmarshal(v, &c.plain)
	default:
		// 交给 json 解信封（v / ct 两个字段）；内容不是字符串也不是对象（数字、数组……）
		// 时在这里报错，而不是把命令静默变成空串。
		return json.Unmarshal(v, &c.Content)
	}
}

// quickCmdsFile 是磁盘上的整表信封（Version 2 = 命令内容加密）。
type quickCmdsFile struct {
	Version int `json:"version"`
	// Commands 用指针切片没意义（null 与 [] 在前端都当空处理），统一在读取时归一成 []。
	Commands []quickCmdStored `json:"commands"`
}

// maxQuickCmdsBody 是保存请求体的上限（整表提交，正常只有几 KB；没有上限时一个
// 客户端可以用超大 body 把进程内存打爆 —— 该进程以 root 运行且承载全部会话）。
const maxQuickCmdsBody = 1 << 20 // 1MB

// quickCmdsMu serializes concurrent reads/writes of the persisted file.
var quickCmdsMu sync.Mutex

func (m *AdminMux) handleGetQuickCmds(w http.ResponseWriter, r *http.Request) {
	cmds, err := loadQuickCmds(m.renv.QuickCmdsFile, m.crypt)
	if err != nil {
		writeErr(w, "failed to load quick commands: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "path": m.renv.QuickCmdsFile, "commands": cmds})
}

func (m *AdminMux) handleSaveQuickCmds(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxQuickCmdsBody)
	var req struct {
		Commands []QuickCmd `json:"commands"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		writeErr(w, "invalid request body", http.StatusBadRequest)
		return
	}
	cmds, err := sanitizeQuickCmds(req.Commands)
	if err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := saveQuickCmds(m.renv.QuickCmdsFile, m.crypt, cmds); err != nil {
		writeErr(w, "failed to save quick commands: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "path": m.renv.QuickCmdsFile, "commands": cmds})
}

// quickCmdAADField 是命令内容密文的 AAD 字段名：把**指令 id** 绑进去。
//
// secret.go 的 AAD 形态是 `terminal:v1:<field>:<scope>`：field 这一格放指令 id，scope 那一格
// 放表的作用域（本项目只有全局表 = quickCmdScopeGlobal）。于是「同一张表里两条指令的密文互换
// 位置」会在解密时直接失败，而不是悄悄把 A 的命令接到 B 的按钮上（按钮上写的是 B 的名字，
// 点下去跑的是 A 的内容）。
//
// id 只需要「同一张表内互不相同」：AAD 是整串比较、不做拆分，所以 id 里带冒号也不会与
// 作用域那一格产生歧义。
func quickCmdAADField(id string) string { return "quickcmd:" + id }

// sanitizeQuickCmds 校验并规整整张表：去重、trim 名称。
//
// 名称去空白，**命令内容原样保留**（只判「全是空白 = 空」）：内容里的前导/行尾空格与换行都是
// 命令的一部分（heredoc、多行脚本、`printf '  x'`），顺手 trim 就是把用户写下的东西改掉——
// 前端的 QuickCmdEditDialog 现在也只 trim 名称。两套口径不一致的症状是「保存时看到的和重新
// 打开后看到的不一样」，也会让「加密前后内容逐字节不变」这条承诺失去意义。
func sanitizeQuickCmds(in []QuickCmd) ([]QuickCmd, error) {
	out := make([]QuickCmd, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, c := range in {
		c.Name = strings.TrimSpace(c.Name)
		if c.ID == "" || c.Name == "" || strings.TrimSpace(c.Content) == "" {
			return nil, errors.New("quick command requires id, name and content")
		}
		if seen[c.ID] {
			// 同一 id 出现两次：保留先出现的那条（前端按 id 做列表 key，重复 id 会让渲染错乱）。
			continue
		}
		seen[c.ID] = true
		out = append(out, c)
	}
	return out, nil
}

// loadQuickCmds 读一张表并把命令内容解成明文（`GET /api/quickcmds` 用）。
func loadQuickCmds(path string, crypt *cryptor) ([]QuickCmd, error) {
	quickCmdsMu.Lock()
	defer quickCmdsMu.Unlock()
	f, err := loadQuickCmdsFile(path, crypt)
	if err != nil {
		return nil, err
	}
	out := make([]QuickCmd, 0, len(f.Commands))
	for _, e := range f.Commands {
		content, err := decryptQuickCmdContent(crypt, e)
		if err != nil {
			// 解密失败必须报错：静默给一个空内容，用户看到的是「我的指令内容没了」，而真正的
			// 原因（换了 master.key / 密文被搬到别的指令上）永远查不出来，而且他一点保存就会
			// 把服务端那份覆盖成空的。
			return nil, fmt.Errorf("decrypt command content of %q (%s) failed: %w", e.Name, e.ID, err)
		}
		out = append(out, QuickCmd{ID: e.ID, Name: e.Name, Content: content, Auto: e.Auto})
	}
	return out, nil
}

// decryptQuickCmdContent 解出一条指令的命令内容。
//
// 走到这里的内容**一定是密文**：明文条目在 migrateQuickCmds 里已经全部被加密重写（迁移不掉时
// loadQuickCmdsFile 直接报错，根本到不了这里）。`content` 是 null / 缺字段时按「没有内容」处理
// ——那是手工编辑过的文件，不是明文。
func decryptQuickCmdContent(crypt *cryptor, e quickCmdStored) (string, error) {
	if e.Content == nil {
		return "", nil
	}
	return crypt.decrypt(quickCmdAADField(e.ID), quickCmdScopeGlobal, e.Content)
}

// loadQuickCmdsFile 读一张表的磁盘形态（调用方持锁），并**顺手做迁移**：文件里只要有旧格式的
// 明文内容，就地加密重写（原子替换）。
//
// 迁移失败（加密不了、或重写不进去）时**报错**，而不是「先把明文内容返回给用户、下次再说」：
// 报告出来的失败是可查的，静默保留明文则违背这条改动的全部意义。文件读不到（还没有指令）
// 按空表处理——那是正常状态，不是错误。
func loadQuickCmdsFile(path string, crypt *cryptor) (quickCmdsFile, error) {
	var f quickCmdsFile
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return quickCmdsFile{}, nil
		}
		return f, err
	}
	// 版本 2 是整表信封；更早的版本还允许**裸数组**（`[{...}]`），一并兼容。
	if err := json.Unmarshal(data, &f); err != nil {
		var bare []quickCmdStored
		if err2 := json.Unmarshal(data, &bare); err2 != nil {
			return f, err
		}
		f = quickCmdsFile{Commands: bare}
	}
	if f.Commands == nil {
		f.Commands = []quickCmdStored{}
	}
	migrated, err := migrateQuickCmds(crypt, &f)
	if err != nil {
		return f, err
	}
	if !migrated {
		return f, nil
	}
	if err := writeQuickCmdsFile(path, f); err != nil {
		return f, fmt.Errorf("加密重写 %s 失败：%w", path, err)
	}
	return f, nil
}

// migrateQuickCmds 把表里**旧格式的明文内容**加密成版本 2 的信封，返回是否发生了迁移。
//
// 规则：
//   - 已经是信封的条目**原样保留**（不重新加密：没必要换密文，也就少一次「解不开」的机会）；
//   - 明文内容加密成功 → 换成信封，内容与格式逐字节不变；
//   - 明文内容为空（空内容存不下来，encrypt 返回 nil）或加密失败 → 换成**加密后的占位文案**
//     （quickCmdMigrationPlaceholder），并把 `auto` 关掉——一句中文提示不该被「点一下就自动
//     回车」当成命令跑到远端 shell 上；
//   - 连占位文案都加密不了（系统随机数不可用这种级别）→ 返回错误、**一个字节都不写**：
//     写不出密文，也不能为了「看起来迁移成功」而留下明文。
//   - 主密钥不可用（crypt == nil）→ 报错，**不返回那份明文**（见 decryptQuickCmdContent 的说明）；
//     磁盘上的文件一个字节都不动，修好主密钥后重试即可。
func migrateQuickCmds(crypt *cryptor, f *quickCmdsFile) (bool, error) {
	if crypt == nil {
		for i := range f.Commands {
			if f.Commands[i].legacy {
				return false, errors.New("主密钥不可用，无法把明文的命令内容加密落盘（磁盘上仍是明文，修好主密钥后重试）")
			}
		}
		return false, nil
	}
	changed := false
	for i := range f.Commands {
		e := &f.Commands[i]
		if !e.legacy {
			continue
		}
		changed = true
		sec, err := crypt.encrypt(quickCmdAADField(e.ID), quickCmdScopeGlobal, e.plain)
		if err != nil || sec == nil {
			// 这里是**唯一**会让用户丢内容的路径，所以原因必须可查（用户只看到占位文案）。
			if err == nil {
				// encrypt("") 返回 nil：空内容存不下来（正常也不会有——sanitize 就拒绝了空内容，
				// 这里只可能是手工编辑过的文件）。
				logger().Printf("快捷指令「%s」（%s）的明文内容为空，已换成占位文案", e.Name, e.ID)
			} else {
				logger().Printf("快捷指令「%s」（%s）的明文内容加密失败，已清空成占位文案：%v", e.Name, e.ID, err)
			}
			placeholder, perr := crypt.encrypt(quickCmdAADField(e.ID), quickCmdScopeGlobal, quickCmdMigrationPlaceholder)
			if perr != nil {
				return false, fmt.Errorf("加密占位内容失败：%w", perr)
			}
			if placeholder == nil {
				return false, errors.New("加密占位内容返回了空信封")
			}
			sec = placeholder
			e.Auto = false
		}
		e.Content, e.plain, e.legacy = sec, "", false
	}
	if changed {
		// 发生过迁移就一定按版本 2 落盘（哪怕这张表原本没写 version）。
		f.Version = 2
	}
	return changed, nil
}

// migrateQuickCmdsAtStartup 在启动时把磁盘上的旧明文内容加密一次。
//
// 为什么不能只在读取时迁移：前端只在**打开快捷指令弹窗**时才请求 `/api/quickcmds`，
// 用户几个月不点它，明文就一直躺在磁盘上。启动时主动迁一次才能兜住「数据目录里不再有明文」。
//
// 失败不拦住启动（返回错误，调用方只记日志）：终端就是这个 NAS 的带外访问入口，应用起不来
// 比一条指令还是明文更糟；而用户真的打开弹窗时（或下次启动）读路径会再迁移一次并报出原因。
func migrateQuickCmdsAtStartup(renv *RuntimeEnv, crypt *cryptor) error {
	if crypt == nil {
		return errors.New("主密钥不可用，跳过明文命令内容的迁移")
	}
	quickCmdsMu.Lock()
	defer quickCmdsMu.Unlock()
	// 和读取走同一条路（读 → 迁移 → 需要时原子重写），迁移规则只有一处实现。
	_, err := loadQuickCmdsFile(renv.QuickCmdsFile, crypt)
	return err
}

// saveQuickCmds 加密整张表并落盘（版本 2）。
//
// 每次保存都重新加密（GCM 每次的 nonce 都是新的）：整表重写本来就会换掉整个文件，没有
// 「保留原密文」的意义，而解密再加密的往返反而多一次失败机会。
func saveQuickCmds(path string, crypt *cryptor, cmds []QuickCmd) error {
	quickCmdsMu.Lock()
	defer quickCmdsMu.Unlock()
	stored := make([]quickCmdStored, 0, len(cmds))
	for _, c := range cmds {
		sec, err := crypt.encrypt(quickCmdAADField(c.ID), quickCmdScopeGlobal, c.Content)
		if err != nil {
			// crypt == nil（主密钥不可用）也会走到这里：**绝不**退化成写明文。
			return fmt.Errorf("加密快捷指令「%s」的命令内容失败：%w", c.Name, err)
		}
		if sec == nil {
			// encrypt 对空明文返回 nil，而空内容已经被 sanitize 拒掉；走到这里说明调用顺序被改坏了。
			return fmt.Errorf("快捷指令「%s」的命令内容为空，拒绝落盘", c.Name)
		}
		stored = append(stored, quickCmdStored{ID: c.ID, Name: c.Name, Auto: c.Auto, Content: sec})
	}
	return writeQuickCmdsFile(path, quickCmdsFile{Version: 2, Commands: stored})
}

// writeQuickCmdsFile 原子写整表（同目录临时文件 + rename）。
//
// 权限 0600：文件里虽然是密文，但名称/是否自动回车仍是明文，而且它就在主密钥旁边——
// 没有理由让同机其他用户读它。飞牛存储卷上 O_CREAT 的模式位不可靠，必须显式 chmod 一次。
func writeQuickCmdsFile(path string, f quickCmdsFile) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
