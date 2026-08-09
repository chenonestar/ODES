// Package model 定义领域实体。
//
// 一条纪律（HLD 1.2、ADR-006）：Token 与 AnswerRecord 两个结构体中
// **不得定义任何时间字段**，也不得定义指向对方的字段。这不是"约定不写入"，
// 而是让"没有时间字段"成为编译期事实——想写也没有地方可写。
// 数据库侧的同一约束由 store 包的建表断言强制（LLD 2.5）。
package model

import (
	"time"

	"odes/internal/anon"
)

// ── 项目状态机（LLD 7.1）────────────────────────────────────────────

type Status string

const (
	StatusDraft     Status = "draft"
	StatusPublished Status = "published"
	StatusRunning   Status = "running"
	StatusClosed    Status = "closed"
	StatusArchived  Status = "archived"
)

func (s Status) Label() string {
	switch s {
	case StatusDraft:
		return "草稿"
	case StatusPublished:
		return "已发布"
	case StatusRunning:
		return "进行中"
	case StatusClosed:
		return "已截止"
	case StatusArchived:
		return "已归档"
	}
	return string(s)
}

// AllowedTransition 是显式迁移表。任何未列出的迁移一律拒绝，
// 校验集中在此处，不散落到各处理器（LLD 7.1）。
var AllowedTransition = map[Status][]Status{
	StatusDraft:     {StatusPublished},
	StatusPublished: {StatusDraft, StatusRunning, StatusClosed},
	StatusRunning:   {StatusClosed},
	StatusClosed:    {StatusArchived},
	StatusArchived:  {}, // 终态，不可回退
}

func CanTransition(from, to Status) bool {
	for _, s := range AllowedTransition[from] {
		if s == to {
			return true
		}
	}
	return false
}

// ── 项目 ────────────────────────────────────────────────────────────

type Project struct {
	ID              anon.ID
	Name            string
	Intro           string // 明文视图；落库时加密为 intro_enc
	Status          Status
	StartAt         time.Time
	EndAt           time.Time
	ResultOpenAt    time.Time
	ExpectedCount   int // 全部比率指标的分母（FR-PRJ-015）
	PaperEntryCount int // 纸质补录份数，只记数量不标记具体记录（LLD 1.2 修正二）
	APCount         int
	GradeScores     []int // 档位赋分，默认 [100,80,60,0]
	PrintedAt       *time.Time
	TokenDomain     string // 生成令牌时的 domain.primary 快照（LLD 12.3）
	CreatedAt       time.Time
	PublishedAt     *time.Time
	ClosedAt        *time.Time
}

func (p *Project) AcceptingSubmit(now time.Time) bool {
	if p.Status != StatusRunning && p.Status != StatusPublished {
		return false
	}
	return !now.Before(p.StartAt) && now.Before(p.EndAt)
}

// ── 题目结构 ────────────────────────────────────────────────────────

type QuestionKind string

const (
	KindGrade  QuestionKind = "grade"  // 等级题，默认主力题型
	KindScore  QuestionKind = "score"  // 打分题
	KindSingle QuestionKind = "single" // 单选
	KindMulti  QuestionKind = "multi"  // 多选
	KindText   QuestionKind = "text"   // 开放评语
)

type QuestionGroup struct {
	ID        anon.ID
	ProjectID anon.ID
	Title     string
	Intro     string
	SortNo    int
	Questions []Question
}

type Question struct {
	ID       anon.ID
	GroupID  anon.ID
	Kind     QuestionKind
	Title    string
	Hint     string
	Required bool
	SortNo   int
	Config   QuestionConfig
	// SubjectIDs 是本题关联的测评对象。作答时按此展开为矩阵（FR-FRM-031）。
	SubjectIDs []anon.ID
	Options    []Option
}

type QuestionConfig struct {
	Grades   []string `json:"grades,omitempty"`   // 等级题档位名，nil 则用项目默认
	ScoreMin int      `json:"scoreMin,omitempty"` // 打分题
	ScoreMax int      `json:"scoreMax,omitempty"`
	MinPick  int      `json:"minPick,omitempty"` // 多选
	MaxPick  int      `json:"maxPick,omitempty"`
	MaxChars int      `json:"maxChars,omitempty"` // 评语
}

type Option struct {
	ID         anon.ID
	QuestionID anon.ID
	Label      string
	SortNo     int
}

type Subject struct {
	ID        anon.ID
	ProjectID anon.ID
	Name      string // 明文视图；落库加密
	Duty      string // 明文视图；落库加密
	Tag       string // 分组标签，不加密（用于统计筛选）
	SortNo    int
}

// Form 是一个项目的完整题目结构，作答端与统计共用。
type Form struct {
	Grades   []string
	Subjects []Subject
	Groups   []QuestionGroup
}

// ItemCount 是矩阵展开后的作答项总数，用于容量校验（FR-FRM-034 / PC-08）。
func (f *Form) ItemCount() int {
	n := 0
	for _, g := range f.Groups {
		for _, q := range g.Questions {
			n += len(q.SubjectIDs)
		}
	}
	return n
}

func (f *Form) Question(id anon.ID) *Question {
	for gi := range f.Groups {
		for qi := range f.Groups[gi].Questions {
			if f.Groups[gi].Questions[qi].ID == id {
				return &f.Groups[gi].Questions[qi]
			}
		}
	}
	return nil
}

func (f *Form) Subject(id anon.ID) *Subject {
	for i := range f.Subjects {
		if f.Subjects[i].ID == id {
			return &f.Subjects[i]
		}
	}
	return nil
}

// ── 匿名隔离区 ──────────────────────────────────────────────────────
//
// 以下两个结构体之间不存在任何关联路径。改动它们前请先读 LLD 2.2。

// Token 对应 token 表。
//
// ✗ 禁止新增：UsedAt、CreatedAt、Seq、RosterID、IP、UserAgent
// 任何"顺手加个时间戳方便排查"的字段都会重新打开 NFR-ANO-020 的时序通道。
type Token struct {
	ID        anon.ID
	ProjectID anon.ID
	Value     string
	ShortCode string
	BatchNo   int
	APIndex   int // 印在令牌单上的 SSID 序号
	IsSpare   bool
	Used      bool
}

// AnswerRecord 对应 answer_record 表。
//
// 整份作答序列化为 JSON 后整体加密存入 Payload（LLD 1.2 修正一）：
// 逐项建表会让"某人跳过了哪些题、按什么顺序作答"变得可观察。
//
// ✗ 禁止新增：TokenID、RosterID、SubmittedAt、Seq、IP、IsPaperEntry
// 其中 IsPaperEntry 尤其危险——不在场者名单是管理员知道的，
// 给补录记录打标记等于给这几份作答贴上身份标签（LLD 1.2 修正二）。
type AnswerRecord struct {
	ID        anon.ID
	ProjectID anon.ID
	BatchNo   int
	Payload   []byte
}

// ── 作答载荷（LLD 3.1）──────────────────────────────────────────────

// GradeAbstain 是显式弃权。与"漏填"区分：弃权是有效表达，
// 相当于纸质测评的"未表态"，单列统计并计入分母（SRS 6.3）。
const GradeAbstain = -1

type Answer struct {
	V     int          `json:"v"`
	Items []AnswerItem `json:"items"`
}

type AnswerItem struct {
	Q     string   `json:"q"`               // 题目 ID（hex）
	S     string   `json:"s"`               // 对象 ID（hex）
	Grade *int     `json:"grade,omitempty"` // 等级题：0..3 或 -1 弃权
	Score *int     `json:"score,omitempty"` // 打分题
	Opt   string   `json:"opt,omitempty"`   // 单选
	Opts  []string `json:"opts,omitempty"`  // 多选
	Text  string   `json:"text,omitempty"`  // 评语
}
