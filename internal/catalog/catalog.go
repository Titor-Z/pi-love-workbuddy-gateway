// Package catalog 把后端实时模型目录渲染成「带费用标记」的模型信息。
//
// 后端的模型目录接口提供两样东西：计费倍率（credits，如 "x0.00 credits"）
// 和促销徽章（tags 里的 badge:<文案>:<颜色>）。它**不提供**促销截止日期——
// 那个只出现在 WorkBuddy 桌面端的界面上，接口里没有。
// 因此截止日期由本地配置文件提供，见 LoadPromotions。
package catalog

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/upstream"
)

// badgePrefix 是后端促销徽章在 tags 里的前缀。
const badgePrefix = "badge:"

// labelSeparator 是展示名里各段落的分隔符。用中点而不是连字符，
// 因为模型名本身就带连字符（如 GLM-5.3-Flash），连字符会分不清边界。
const labelSeparator = " · "

// Promotion 是本地维护的促销信息，补上后端不提供的截止日期与备注。
type Promotion struct {
	FreeUntil string `json:"free_until"` // 免费截止日期，形如 2026-10-31
	Note      string `json:"note"`       // 人看的备注，如「每晚 23:00 后免费」
}

// Promotions 是按模型 ID 索引的促销信息表。
type Promotions struct {
	items map[string]Promotion
}

// LoadPromotions 读取促销配置文件。文件不存在时返回空表而不是报错，
// 因为「没有本地促销配置」是完全正常的状态。
func LoadPromotions(path string) (*Promotions, error) {
	table := &Promotions{items: map[string]Promotion{}}
	if path == "" {
		return table, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return table, nil
		}
		return nil, fmt.Errorf("读取促销配置失败：%w", err)
	}
	if err := json.Unmarshal(raw, &table.items); err != nil {
		return nil, fmt.Errorf("促销配置不是合法 JSON：%w", err)
	}
	return table, nil
}

// Lookup 取某个模型的促销信息。
func (p *Promotions) Lookup(id string) (Promotion, bool) {
	if p == nil {
		return Promotion{}, false
	}
	item, ok := p.items[id]
	return item, ok
}

// Entry 是一个模型连同它的费用标记。
type Entry struct {
	Model upstream.ModelInfo // 后端给的原始信息
	Label Label              // 合成出来的费用标记
}

// Label 是模型的费用标记。
type Label struct {
	Free      bool   // 是否免费，即倍率为 0
	Credits   string // 归一化后的倍率，形如 "0.29x"；免费时为空
	Badge     string // 后端促销徽章文案，如「夜间免费」
	FreeUntil string // 免费截止日期，来自本地配置
	Note      string // 本地备注
}

// Build 把后端模型信息与本地促销信息合成一条列表项。
func Build(models []upstream.ModelInfo, promotions *Promotions) []Entry {
	entries := make([]Entry, 0, len(models))
	for _, model := range models {
		promotion, _ := promotions.Lookup(model.ID)
		entries = append(entries, Entry{Model: model, Label: labelOf(model, promotion)})
	}
	return entries
}

// DisplayName 把标记拼进展示名，让 pi 的模型选择器一眼看到费用与活动。
func (e Entry) DisplayName() string {
	parts := []string{e.Model.Name}
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

// labelOf 由倍率、徽章与本地配置合成费用标记。
func labelOf(model upstream.ModelInfo, promotion Promotion) Label {
	value, parsed := creditsValue(model.Credits)
	label := Label{
		Free:      parsed && value == 0,
		Badge:     badgeOf(model.Tags),
		FreeUntil: promotion.FreeUntil,
		Note:      promotion.Note,
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
