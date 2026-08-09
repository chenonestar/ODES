package service

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"odes/internal/anon"
	"odes/internal/crypto"
	"odes/internal/model"
	"odes/internal/wipe"
)

// BuildArchive 产出加密归档包（FR-SYS-030）。
//
// 口令由管理员单独设定，与登录口令不同——两者用途不同、保管方式不同：
// 登录口令随人走，归档口令随介质走。包内含完整性校验值。
//
// 注意：AES-GCM 每次用新的随机 nonce，Argon2id 每次用新的随机盐，
// 因此**同一项目两次 BuildArchive 产出的字节与 SHA 必然不同**。
// 归档确认流程要求管理员复述"已保存到介质的那一份"的校验值，所以
// 构建结果会被缓存下来（见 lastArchive），ArchiveAndWipe 比对并擦除的
// 都是同一份字节，而不是临时重新生成的另一份。
func (s *Service) BuildArchive(projectID anon.ID, pass string) ([]byte, string, error) {
	// FR-SYS-030：归档口令由管理员单独设定，**与登录密码不同**。
	// 两者保管方式不同——登录口令随人走，归档口令随介质走；用同一个
	// 就等于把归档包的安全性绑死在本机口令上，失去了单独设定的意义。
	if same, err := s.samePasswordAsLogin(pass); err != nil {
		return nil, "", err
	} else if same {
		return nil, "", errors.New(
			"归档口令不得与登录口令相同：两者分别保管才有意义（FR-SYS-030）")
	}
	p, err := s.DB.Project(s.Vault, projectID)
	if err != nil {
		return nil, "", err
	}
	f, err := s.DB.Form(s.Vault, p)
	if err != nil {
		return nil, "", err
	}
	recs, err := s.DB.LoadAnswers(projectID)
	if err != nil {
		return nil, "", err
	}

	// 归档包内的作答记录同样重排、同样用现场编号：归档件本身
	// 也不得携带顺序信息。
	type item struct {
		Label string            `json:"label"`
		Items []model.AnswerItem `json:"items"`
	}
	decoded := make([]item, 0, len(recs))
	for _, r := range recs {
		b, err := s.Vault.Open(r.Payload, r.ProjectID.Bytes())
		if err != nil {
			return nil, "", fmt.Errorf("解密作答记录失败: %w", err)
		}
		var a model.Answer
		if err := json.Unmarshal(b, &a); err != nil {
			return nil, "", err
		}
		decoded = append(decoded, item{Items: a.Items})
	}
	anon.Shuffle(decoded)
	for i := range decoded {
		decoded[i].Label = anon.Label(i)
	}

	res, err := s.Stats(projectID)
	if err != nil {
		return nil, "", err
	}

	bundle := map[string]any{
		"format":  "odes-archive/1",
		"project": map[string]any{
			"name":          p.Name,
			"expectedCount": p.ExpectedCount,
			"paperEntry":    p.PaperEntryCount,
			"gradeScores":   p.GradeScores,
			"startAt":       p.StartAt,
			"endAt":         p.EndAt,
		},
		"form":    f,
		"answers": decoded,
		"stats":   res,
	}
	plainJSON, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return nil, "", err
	}

	// 用归档口令独立派生一把钥匙，不复用 DEK：归档包要离开这台机器，
	// 它的安全性不应与本机主密钥绑定。
	av, meta, err := crypto.Init(pass)
	if err != nil {
		return nil, "", fmt.Errorf("归档口令不合要求: %w", err)
	}
	defer av.Close()
	sealed, err := av.Seal(plainJSON, []byte("odes-archive"))
	if err != nil {
		return nil, "", err
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name string, b []byte) error {
		fw, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = fw.Write(b)
		return err
	}
	if err := write("payload.enc", sealed); err != nil {
		return nil, "", err
	}
	km, _ := json.Marshal(map[string]any{
		"kekSalt": meta.KEKSalt, "wrappedDek": meta.WrappedDEK, "pwdHash": meta.PwdHash,
	})
	if err := write("keymeta.json", km); err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(sealed)
	if err := write("SHA256", []byte(hex.EncodeToString(sum[:]))); err != nil {
		return nil, "", err
	}
	if err := write("README.txt", []byte(
		"线上民主测评系统 · 加密归档包\n\n"+
			"payload.enc 为 AES-256-GCM 密文，口令由归档时的管理员单独设定。\n"+
			"口令遗失则内容不可恢复，无后门、无找回。\n"+
			"SHA256 为 payload.enc 的校验值，用于核对介质完整性。\n")); err != nil {
		return nil, "", err
	}
	if err := zw.Close(); err != nil {
		return nil, "", err
	}

	blob, digest := buf.Bytes(), hex.EncodeToString(sum[:])
	// 记住这一份，供随后的归档确认与擦除使用
	s.mu.Lock()
	s.lastArchive[projectID] = archiveBlob{data: blob, sha: digest}
	s.mu.Unlock()
	return blob, digest, nil
}

// ArchiveAndWipe 执行归档 + 安全擦除（FR-SYS-031/032）。
//
// 顺序不可颠倒，且每一步失败都必须中止：擦除是不可逆的，
// 在归档包尚未确认写出之前动数据，等于把测评结果直接销毁。
func (s *Service) ArchiveAndWipe(ctx context.Context, projectID anon.ID,
	pass, confirmSHA8 string) error {

	p, err := s.DB.Project(s.Vault, projectID)
	if err != nil {
		return err
	}
	if p.Status != model.StatusClosed {
		return fmt.Errorf("只有已截止的项目可以归档（当前：%s）", p.Status.Label())
	}

	// 必须比对**已下载的那一份**。每次构建都会产生不同的密文与校验值，
	// 若此处重新构建再比对，管理员手里那份的校验值永远对不上，
	// 而且擦除掉的将是一份从未落到介质上的数据。
	s.mu.Lock()
	built, ok := s.lastArchive[projectID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("请先下载加密归档包并保存至指定介质，再执行归档擦除")
	}
	// 复述校验值前 8 位：确保管理员确实拿到了包，而不是点了个勾。
	if !strings.EqualFold(confirmSHA8, built.sha[:8]) {
		return fmt.Errorf("校验值不匹配：请填写你已保存的归档包 SHA-256 的前 8 位")
	}
	sum := built.sha

	submitted, _ := s.DB.CountAnswers(projectID)
	verify := strings.ToUpper(projectID.Hex()[:8])

	// 擦除前留一份特征串，用于擦除后的残留校验。
	sample, err := s.answerSample(projectID)
	if err != nil {
		return err
	}

	if err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		return s.DB.PurgeProject(tx, projectID)
	}); err != nil {
		return fmt.Errorf("删除项目数据失败: %w", err)
	}

	// VACUUM 重建 + 覆写空闲空间 + 残留校验。任一步失败都是硬失败：
	// 不得在残留未清除的情况下把项目标记为已归档。
	if err := wipe.Run(s.DB.DB, s.DB.Path(), sample); err != nil {
		_ = s.DB.Log(&projectID, "wipe", "fail", err.Error())
		return fmt.Errorf("安全擦除失败，项目未标记为已归档: %w", err)
	}

	if err := s.DB.WriteArchiveMeta(projectID, p.Name, p.ExpectedCount,
		submitted, sum, verify); err != nil {
		return err
	}
	s.invalidateStats(projectID)
	return s.DB.Log(nil, "archive", "ok",
		fmt.Sprintf("项目「%s」已归档并擦除，核验编号 %s，包哈希 %s…",
			p.Name, verify, sum[:16]))
}

// samePasswordAsLogin 判断给定口令是否就是登录口令。
//
// 用登录口令哈希比对，不需要也没有明文口令——meta 里存的是 Argon2id
// 派生值，本来就无法反推。
func (s *Service) samePasswordAsLogin(pass string) (bool, error) {
	var m crypto.Meta
	var err error
	if m.KEKSalt, err = s.DB.GetMeta("kek_salt"); err != nil {
		return false, err
	}
	if m.PwdHash, err = s.DB.GetMeta("admin_pwd_hash"); err != nil {
		return false, err
	}
	if len(m.KEKSalt) == 0 || len(m.PwdHash) == 0 {
		return false, nil
	}
	return crypto.VerifyPassword(pass, m), nil
}

// answerSample 取若干作答记录的密文片段作为残留检查的特征串。
func (s *Service) answerSample(projectID anon.ID) ([][]byte, error) {
	recs, err := s.DB.LoadAnswers(projectID)
	if err != nil {
		return nil, err
	}
	var out [][]byte
	for i, r := range recs {
		if i >= 3 {
			break
		}
		if len(r.Payload) > 32 {
			out = append(out, append([]byte(nil), r.Payload[12:44]...))
		}
	}
	return out, nil
}
