// Package overridessync 实现模型覆盖表的冷同步。
//
// 为什么覆盖表要冷同步，而模型目录不用：目录是派生数据，客户端切到
// gateway 模式后本机有凭证，直接从后端实时拉；覆盖表（别名、免费截止、
// 思考档位、隐藏）是**只有用户能写**的源数据，离开了那台机器就没有了。
//
// 形态是「网关权威 + 客户端 dirty + 显式回推 + 三方合并」：
//
//   - 网关那份是权威，但客户端离线期间的改动**不丢**；
//   - 回到局域网后由用户点一下推回（不自动合并，冲突必须由人决定）；
//   - 判据是**内容比较**（相对共同祖先改过没有），不是时间戳——
//     跨机器比时钟不可靠，两台机器的时钟也不构成次序关系。
//
// 与凭证同步的区别只有一处，但很关键：凭证**永远不接受客户端推送**
// （它是账号令牌，客户端一旦能写就会退化成互相踢），覆盖表不是秘密，
// 必须能推回。详见 docs/model-overrides-design.md。
package overridessync

import (
	"sort"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/catalog"
)

// Prefer 决定冲突时以哪一边为准。
//
// 空值表示**不替用户决定**——直接报出冲突。自动合并永远有边界情况，
// 而这里的边界情况就是「两边都改过同一条」，谁对谁错只有用户知道。
type Prefer string

const (
	// PreferNone 遇到冲突就停下，交给人。
	PreferNone Prefer = ""
	// PreferLocal 冲突时以本机为准。
	PreferLocal Prefer = "local"
	// PreferRemote 冲突时以网关为准。
	PreferRemote Prefer = "remote"
)

// Conflict 是一条两边都改过、内容又不同的条目。
type Conflict struct {
	Model  string
	Base   catalog.Override
	Local  catalog.Override
	Remote catalog.Override
}

// Merge 做三方合并（冲突不替用户决定）。
func Merge(base, local, remote *catalog.Overrides) (map[string]catalog.Override, []Conflict) {
	return MergePreferring(base, local, remote, PreferNone)
}

// MergePreferring 做三方合并。
//
// base 是共同祖先（上次成功同步时网关那一份），local 是本机编辑，
// remote 是网关当前那一份。按条目判定：
//
//	都没改 / 只有一边改 / 两边改成一样 → 直接得到结果
//	两边都改且不同                  → 冲突：prefer 为空就报出来，否则按它选边
//
// 返回合并后的映射与冲突清单。prefer 为空且出现冲突时**不要推送**。
func MergePreferring(base, local, remote *catalog.Overrides, prefer Prefer) (map[string]catalog.Override, []Conflict) {
	merged := map[string]catalog.Override{}
	conflicts := []Conflict{}

	for _, model := range union(local, remote) {
		baseEntry, inBase := lookup(base, model)
		localEntry, inLocal := lookup(local, model)
		remoteEntry, inRemote := lookup(remote, model)

		localChanged := changed(*localEntry, value(baseEntry), inBase, inLocal)
		remoteChanged := changed(*remoteEntry, value(baseEntry), inBase, inRemote)

		switch {
		case !localChanged && !remoteChanged:
			if inBase {
				merged[model] = *baseEntry
			}
		case localChanged && !remoteChanged:
			if inLocal {
				merged[model] = *localEntry
			}
		case !localChanged && remoteChanged:
			if inRemote {
				merged[model] = *remoteEntry
			}
		case catalog.SameContent(*localEntry, *remoteEntry):
			// 两边都改过，但改成了同一个结果，不算冲突。
			merged[model] = *localEntry
		case prefer == PreferLocal:
			merged[model] = *localEntry
		case prefer == PreferRemote:
			merged[model] = *remoteEntry
		default:
			conflicts = append(conflicts, Conflict{
				Model: model, Base: value(baseEntry), Local: *localEntry, Remote: *remoteEntry,
			})
		}
	}
	return merged, conflicts
}

// changed 判断某一侧相对共同祖先是否改动过。
//
// 存在性本身也算改动：祖先里没有而现在有，就是新增；祖先里有而现在没了，
// 就是删除。只比内容会把新增/删除看漏。
func changed(entry, base catalog.Override, inBase, inSide bool) bool {
	if inBase != inSide {
		return true
	}
	if !inSide {
		return false
	}
	return !catalog.SameContent(entry, base)
}

// union 收集两边出现过的全部模型 id。用 map 去重，顺序由调用方保证稳定。
func union(local, remote *catalog.Overrides) []string {
	seen := map[string]bool{}
	ids := []string{}
	for _, table := range []*catalog.Overrides{local, remote} {
		if table == nil {
			continue
		}
		for id := range table.Models {
			if seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// lookup 取条目，并对 nil 表做保护。
func lookup(table *catalog.Overrides, model string) (*catalog.Override, bool) {
	if table == nil {
		return &catalog.Override{}, false
	}
	entry, ok := table.Models[model]
	if !ok {
		return &catalog.Override{}, false
	}
	return &entry, true
}

func value(entry *catalog.Override) catalog.Override {
	if entry == nil {
		return catalog.Override{}
	}
	return *entry
}
