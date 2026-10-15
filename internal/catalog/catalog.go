// Package catalog 把后端实时模型目录渲染成「带费用标记」的模型信息。
//
// 后端的模型目录接口提供两样东西：计费倍率（credits，如 "x0.00 credits"）
// 和促销徽章（tags 里的 badge:<文案>:<颜色>）。它**不提供**促销截止日期，
// 也不提供思考档位的语义——那些只出现在 WorkBuddy 桌面端的界面上。
// 因此这些由本地的**模型覆盖表**提供，见 LoadOverrides。
package catalog

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/upstream"
)

// badgePrefix 是后端促销徽章在 tags 里的前缀。
const badgePrefix = "badge:"

// labelSeparator 是展示名里各段落的分隔符。用中点而不是连字符，
// 因为模型名本身就带连字符（如 GLM-5.3-Flash），连字符会分不清边界。
const labelSeparator = " · "

// Override 是用户对单个模型的手写覆盖项。
//
// 这些字段后端都不提供，只能人工维护；也正因为只有人能写，
// 它们才需要跟随用户走（见 docs/model-overrides-design.md 的冷同步）。
type Override struct {
	Alias            string             `json:"alias,omitempty"`              // 展示别名，替换展示名的第一段
	FreeUntil        string             `json:"free_until,omitempty"`         // 免费截止日期，形如 2026-10-31
	Note             string             `json:"note,omitempty"`               // 人看的备注
	Hidden           bool               `json:"hidden,omitempty"`             // 从列表里隐藏（请求仍然可用）
	ThinkingLevelMap map[string]*string `json:"thinking_level_map,omitempty"` // pi 的思考档位映射，nil 表示该档位不支持
	UpdatedAt        string             `json:"updated_at,omitempty"`         // 最后一次改动时间（RFC3339），三方合并用
	UpdatedBy        string             `json:"updated_by,omitempty"`         // 最后一次改动的机器（device id）
}

// content 去掉同步元数据，只留用户写的东西。
// 三方比对是**比内容**，不是比时间戳——跨机器比时钟不可靠。
func (o Override) content() Override {
	o.UpdatedAt = ""
	o.UpdatedBy = ""
	return o
}

// SameContent 只比用户写的内容，忽略 updated_at / updated_by 这类
// 同步元数据。三方合并的判据是「相对共同祖先改过没有」，
// 而同步元数据本身每次写入都会变，拿它比会把所有条目都判成改过。
func SameContent(left, right Override) bool {
	return reflect.DeepEqual(left.content(), right.content())
}

// Overrides 是按模型 ID 索引的覆盖表。
type Overrides struct {
	Revision int64               `json:"revision"`            // 写次数；网关侧单调递增，乐观并发的依据
	ChangeAt string              `json:"change_at,omitempty"` // 最后一次改动时间，只给人看
	Models   map[string]Override `json:"models"`              // 键是模型 ID
}

// NewOverrides 建一张空表。所有入口都该用它，别自己造 map——nil map 写不进去。
func NewOverrides() *Overrides {
	return &Overrides{Models: map[string]Override{}}
}

// LoadOverrides 读取覆盖表。文件不存在时返回空表而不是报错，
// 因为「没有任何覆盖项」是完全正常的状态。
func LoadOverrides(path string) (*Overrides, error) {
	table := NewOverrides()
	if path == "" {
		return table, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return table, nil
		}
		return nil, fmt.Errorf("读取模型覆盖表失败：%w", err)
	}
	parsed, err := LoadOverridesFromJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("模型覆盖表不是合法 JSON：%w", err)
	}
	return parsed, nil
}

// SaveOverrides 原子写入覆盖表：先写临时文件再改名，
// 避免写一半被中断时留下一个读不出来的文件。
func SaveOverrides(path string, table *Overrides) error {
	if table.Models == nil {
		table.Models = map[string]Override{}
	}
	// 不用 json.Marshal：它会把 < > & 转义成 \u003c 之类，而这些字符在
	// 别名与备注里会出现，转义后 grep 与 jq 都难用。
	var builder strings.Builder
	encoder := json.NewEncoder(&builder)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(table); err != nil {
		return fmt.Errorf("编码模型覆盖表失败：%w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建配置目录失败：%w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(builder.String()), 0o600); err != nil {
		return fmt.Errorf("写入模型覆盖表失败：%w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("保存模型覆盖表失败：%w", err)
	}
	return nil
}

// Touch 记一次写入：revision 递增、change_at 更新，
// 并给内容真的变了的条目打上 updated_at / updated_by。
//
// 为什么必须逐条打标：三方合并的判据是「这一条相对共同祖先改过没有」，
// 时间戳只是展示。整表一个时间戳没法定位到具体是哪条被改了。
func Touch(previous, next *Overrides, by string, at time.Time) {
	stamp := at.Format(time.RFC3339)
	if previous == nil {
		previous = NewOverrides()
	}
	for id, entry := range next.Models {
		if old, ok := previous.Models[id]; ok && reflect.DeepEqual(old.content(), entry.content()) {
			// 内容没变：沿用原来的时间与作者，别把「谁改的」冲掉。
			// 这里是从 previous 拷回来而不是依赖调用方回传：同步元数据是
			// 本表的内部状态，不该要求每个写入口都记得原样带回来。
			entry.UpdatedAt = old.UpdatedAt
			entry.UpdatedBy = old.UpdatedBy
			next.Models[id] = entry
			continue
		}
		entry.UpdatedAt = stamp
		entry.UpdatedBy = by
		next.Models[id] = entry
	}
	next.ChangeAt = stamp
	next.Revision = previous.Revision + 1
}

// Lookup 取某个模型的覆盖项。
func (o *Overrides) Lookup(id string) (Override, bool) {
	if o == nil {
		return Override{}, false
	}
	item, ok := o.Models[id]
	return item, ok
}

// MigratePromotions 把旧版的 promotions.json 升级成覆盖表。
//
// 旧文件的 {free_until, note} 就是覆盖表的一个子集。两个写入口必然漂移，
// 所以旧文件退役：**只有导入成功才**把它改名 .migrated，
// 失败时源文件原样不动（宁可不迁移，也不丢用户写的东西）。
// 返回是否真的迁移了。
func MigratePromotions(promotionsPath, overridesPath string) (bool, error) {
	if promotionsPath == "" {
		return false, nil
	}
	if _, err := os.Stat(overridesPath); err == nil {
		// 覆盖表已经存在，说明迁移过了或用户已经在用新表，不动它。
		return false, nil
	}
	raw, err := os.ReadFile(promotionsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("读取旧促销配置失败：%w", err)
	}
	var legacy map[string]Override
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return false, fmt.Errorf("旧促销配置不是合法 JSON：%w", err)
	}
	table := NewOverrides()
	now := time.Now()
	for id, entry := range legacy {
		entry.UpdatedAt = now.Format(time.RFC3339)
		table.Models[id] = entry
	}
	table.Revision = 1
	table.ChangeAt = now.Format(time.RFC3339)
	if err := SaveOverrides(overridesPath, table); err != nil {
		return false, err
	}
	if err := os.Rename(promotionsPath, promotionsPath+".migrated"); err != nil {
		return true, fmt.Errorf("覆盖表已写入，但旧文件改名失败（下次不会重复迁移）：%w", err)
	}
	return true, nil
}

// Entry 是一个模型连同它的费用标记与用户覆盖项。
type Entry struct {
	Model     upstream.ModelInfo // 后端给的原始信息
	Label     Label              // 合成出来的费用标记
	Alias     string             // 用户设的展示别名
	Hidden    bool               // 用户要求从列表里隐藏
	Thinking  map[string]*string // 用户设的思考档位映射（pi 的形状）
	UpdatedAt string             // 覆盖项最后改动时间
	UpdatedBy string             // 覆盖项最后改动的机器
}

// Label 是模型的费用标记。
type Label struct {
	Free      bool   // 是否免费，即倍率为 0
	Credits   string // 归一化后的倍率，形如 "0.29x"；免费时为空
	Badge     string // 后端促销徽章文案，如「夜间免费」
	FreeUntil string // 免费截止日期，来自覆盖表
	Note      string // 覆盖表里的备注
}

// Build 把后端模型信息与本地覆盖表合成一条列表项。
//
// 不过滤 Hidden——列表是展示层的事，而编辑页必须看得见被隐藏的模型，
// 否则用户没法把它放出来。调用方按需过滤。
func Build(models []upstream.ModelInfo, overrides *Overrides) []Entry {
	entries := make([]Entry, 0, len(models))
	for _, model := range models {
		override, _ := overrides.Lookup(model.ID)
		entries = append(entries, Entry{
			Model:     model,
			Label:     labelOf(model, override),
			Alias:     override.Alias,
			Hidden:    override.Hidden,
			Thinking:  override.ThinkingLevelMap,
			UpdatedAt: override.UpdatedAt,
			UpdatedBy: override.UpdatedBy,
		})
	}
	return entries
}

// Visible 只保留没有被打上隐藏标记的条目，供对外列表与 pi 配置使用。
func Visible(entries []Entry) []Entry {
	kept := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.Hidden {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

// DisplayName 把标记拼进展示名，让 pi 的模型选择器一眼看到费用与活动。
// 用户设了别名就用别名打头，其余照旧。
func (e Entry) DisplayName() string {
	head := e.Model.Name
	if e.Alias != "" {
		head = e.Alias
	}
	parts := []string{head}
	if e.Label.Free {
		parts = append(parts, "免费")
	} else if e.Label.Credits != "" {
		parts = append(parts, e.Label.Credits)
	}
	if e.Label.Badge != "" {
		parts = append(parts, e.Label.Badge)
	}
	if e.Label.FreeUntil != "" {
		parts = append(parts, "至"+e.Label.FreeUntil)
	}
	return strings.Join(parts, labelSeparator)
}

// HasImageInput 表示该模型接受图片输入。
func (e Entry) HasImageInput() bool { return e.Model.SupportsImages }

// labelOf 由倍率、徽章与覆盖表合成费用标记。
func labelOf(model upstream.ModelInfo, override Override) Label {
	value, parsed := creditsValue(model.Credits)
	label := Label{
		Free:      parsed && value == 0,
		Badge:     badgeOf(model.Tags),
		FreeUntil: override.FreeUntil,
		Note:      override.Note,
	}
	// 免费模型的倍率没有信息量，不展示，避免出现「免费 · 0x」这种重复。
	if parsed && !label.Free {
		label.Credits = formatCredits(value)
	}
	return label
}

// creditsValue 把 "x0.29 credits" 这类原文解析成数值。
func creditsValue(raw string) (float64, bool) {
	cleaned := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "credits"))
	cleaned = strings.TrimPrefix(cleaned, "x")
	if cleaned == "" {
		return 0, false
	}
	value, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// formatCredits 把倍率渲染成 "0.29x"，去掉无意义的小数位。
func formatCredits(value float64) string {
	rounded := math.Round(value*100) / 100
	return strconv.FormatFloat(rounded, 'f', -1, 64) + "x"
}

// badgeOf 从 tags 里取出促销徽章文案。
func badgeOf(tags []string) string {
	for _, tag := range tags {
		if !strings.HasPrefix(tag, badgePrefix) {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(tag, badgePrefix), ":")
		if len(parts) > 0 && parts[0] != "" {
			return parts[0]
		}
	}
	return ""
}

// thinkingLevels 是 pi 认的思考档位键。
var thinkingLevels = map[string]bool{
	"off": true, "minimal": true, "low": true, "medium": true,
	"high": true, "xhigh": true, "max": true,
}

// Validate 检查覆盖表是否合法。写入前必须过这一关：
// 一个拼错的档位键不会让任何东西报错，只会静默失效——
// 用户在 pi 里选了 high，实际却按默认档位跑。
func (o *Overrides) Validate() error {
	for id, entry := range o.Models {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("模型 id 不能为空")
		}
		for level := range entry.ThinkingLevelMap {
			if !thinkingLevels[level] {
				return fmt.Errorf("模型 %s 的思考档位 %q 不是 pi 认的档位（可用：off/minimal/low/medium/high/xhigh/max）", id, level)
			}
		}
	}
	return nil
}
