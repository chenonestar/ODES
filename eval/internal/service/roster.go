package service

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"

	"odes/internal/anon"
)

// ── 名单导入（FR-TKN-010 / FR-TKN-011 / FR-TKN-012）─────────────────
//
// 名单只用来确定令牌数量与核对应参加人数。
//
// **名单表与令牌表之间不存在任何字段关联**（FR-TKN-011），这一点由
// schema 保证：roster 只有 id / project_id / label_enc 三列。这里再写一遍
// 是因为它极易被"顺手加个 token_id 方便核对"破坏——而那一列一旦存在，
// 整套匿名性当场作废：谁拿到哪个令牌就成了库里的明账。
//
// 导入**只增加行数**，不与令牌产生任何对应；令牌是另行随机生成并重排的。

// MaxRoster 是名单条数上限。取值与令牌生成的实际约束一致，
// 防止误传一份几万行的表把库撑爆。
const MaxRoster = 2000

var ErrRosterEmpty = errors.New("未从文件中解析出任何姓名或编号：请确认第一列是姓名或编号，且不是整表空白")

// ParseRoster 从上传的文件解析名单，返回去重后的标签列表。
//
// **只解析、不落库**：FR-TKN-010 要求导入前提供预览确认。名单是人工
// 誊抄来的，列错位、把表头当数据、多贴了一整列职务，这些都很常见，
// 而一旦直接入库再发现，应参加人数就已经错了——它是全部比率的分母。
func ParseRoster(filename string, data []byte) ([]string, error) {
	var rows []string
	var err error
	switch {
	case strings.HasSuffix(strings.ToLower(filename), ".xlsx"),
		strings.HasSuffix(strings.ToLower(filename), ".xlsm"):
		rows, err = parseRosterXLSX(data)
	default:
		rows, err = parseRosterCSV(data)
	}
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		r = strings.TrimSpace(r)
		if r == "" || isHeaderish(r) {
			continue
		}
		if seen[r] {
			// 重名在真实名单里是存在的，但同一行重复导入多半是误操作。
			// 这里去重并在返回的条数上体现出来，由管理员在预览页判断。
			continue
		}
		seen[r] = true
		out = append(out, r)
		if len(out) >= MaxRoster {
			break
		}
	}
	if len(out) == 0 {
		return nil, ErrRosterEmpty
	}
	return out, nil
}

// isHeaderish 识别常见表头，避免把"姓名"这一行当成一个人。
func isHeaderish(s string) bool {
	switch strings.TrimSpace(s) {
	case "姓名", "名字", "人员", "编号", "工号", "序号", "name", "Name", "NAME":
		return true
	}
	return false
}

func parseRosterXLSX(data []byte) ([]string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("无法读取 Excel 文件：%w", err)
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, errors.New("Excel 文件里没有任何工作表")
	}
	// 只读第一张表：多表时该用哪张是人的判断，猜错比报错更糟
	rows, err := f.GetRows(sheets[0])
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rows {
		if len(r) > 0 {
			out = append(out, r[0])
		}
	}
	return out, nil
}

func parseRosterCSV(data []byte) ([]string, error) {
	// 去 BOM：从 Excel 另存的 CSV 带 BOM，不去掉会让第一个人的名字
	// 前面多出一个不可见字符，之后怎么核对都对不上
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if !utf8.Valid(data) {
		return nil, errors.New(
			"文件不是 UTF-8 编码：请在 Excel 里另存为「CSV UTF-8」，或直接上传 .xlsx")
	}
	rd := csv.NewReader(bufio.NewReader(bytes.NewReader(data)))
	rd.FieldsPerRecord = -1 // 每行列数可以不一致
	var out []string
	for {
		rec, err := rd.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// 不是合法 CSV 时退回按行读：很多"名单"其实就是一列纯文本
			return parseRosterLines(data), nil
		}
		if len(rec) > 0 {
			out = append(out, rec[0])
		}
	}
	return out, nil
}

func parseRosterLines(data []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		out = append(out, strings.TrimSpace(sc.Text()))
	}
	return out
}

// ImportRoster 把预览确认过的名单写入。整表替换，不做增量合并——
// 增量合并会让"导错了再导一次"变成两份名单叠加，而人数是比率分母。
func (s *Service) ImportRoster(ctx context.Context, projectID anon.ID, labels []string) error {
	if _, err := s.requireDraft(projectID); err != nil {
		return err
	}
	if len(labels) == 0 {
		return ErrRosterEmpty
	}
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.DB.DeleteRosterTx(tx, projectID); err != nil {
			return err
		}
		for _, l := range labels {
			if err := s.DB.InsertRosterTx(tx, s.Vault, projectID, l); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// 只记条数，不记任何姓名——操作日志会随项目长期保留
	_ = s.DB.Log(&projectID, "import_roster", "ok",
		fmt.Sprintf("导入名单 %d 条（名单仅用于确定令牌数量，与令牌无任何关联）", len(labels)))
	return nil
}

// ClearRoster 删除名单（FR-TKN-012）。
//
// 名单在令牌打印分发完成后就没有用处了，删掉可以缩小失窃时的暴露面：
// 它是这个库里唯一一份"参评人员是谁"的记录。删除不影响系统运行与统计。
func (s *Service) ClearRoster(ctx context.Context, projectID anon.ID) error {
	if err := s.DB.DeleteRoster(projectID); err != nil {
		return err
	}
	_ = s.DB.Log(&projectID, "clear_roster", "ok",
		"删除名单（不影响令牌、作答与统计）")
	return nil
}
