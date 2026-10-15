// Package piplugin 把 pi 扩展内嵌进二进制，并按需导出到用户的 pi 扩展目录。
//
// 为什么内嵌而不是发 npm 包：扩展与网关是一体的——网关的 `/v1/models` 多一个
// 字段，插件就得跟着认；分成两个独立发布的东西，版本迟早对不上。
// 内嵌之后「装网关」与「装插件」是同一次交付，也不会在 npm 上公开这份东西。
//
// 注意这与「不替用户写 ~/.pi/agent/models.json」那条红线不冲突：
// 红线说的是**工具自作主张去改别的工具的配置**。这里是用户显式执行
// `package --export`，目标目录在命令里写着、也可以自己指定，改不改由他决定。
package piplugin

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Files 是随二进制一起交付的扩展文件。
//
//go:embed index.ts README.md
var Files embed.FS

// DirName 是扩展在 pi 扩展目录里的子目录名，也是导出时的默认目录名。
const DirName = "pi-workbuddy"

// exported 是要写出去的文件清单：内嵌名 → 目标文件名。
// 刻意逐个列出而不是遍历 FS：多一个文件没写出去是静默的，列出来就不会漏。
var exported = []string{"index.ts", "README.md"}

// DefaultDir 返回本机 pi 的扩展目录。
//
// pi 的配置目录默认是 ~/.pi/agent，可用 PI_CODING_AGENT_DIR 覆盖，
// 所以这里也要认那个变量，否则用户改了 pi 的目录我们就写到别处去了。
func DefaultDir() string {
	agentDir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR"))
	if agentDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		agentDir = filepath.Join(home, ".pi", "agent")
	}
	return filepath.Join(agentDir, "extensions", DirName)
}

// Result 是一次导出的结果，用来给用户一句准确的话。
type Result struct {
	Dir      string   // 实际写到哪
	Files    []string // 写出去的文件名
	Created  bool     // 目录是这次新建的
	Replaced bool     // 覆盖了此前已有的扩展
}

// Export 把内嵌的扩展写到 dir。dir 为空时用 DefaultDir()。
func Export(dir string) (Result, error) {
	if dir == "" {
		dir = DefaultDir()
	}
	if dir == "" {
		return Result{}, fmt.Errorf("找不到用户主目录，请显式指定导出目录")
	}
	// 展开 ~：用户在命令行里写 ~/xxx 时 shell 通常会展开，
	// 但写成参数给别的东西时不一定，这里兜一下。
	if strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return Result{}, fmt.Errorf("展开 ~ 失败：%w", err)
		}
		dir = filepath.Join(home, dir[2:])
	}

	result := Result{Dir: dir}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		result.Created = true
	} else if _, err := os.Stat(filepath.Join(dir, "index.ts")); err == nil {
		result.Replaced = true
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("创建目录失败：%w", err)
	}
	for _, name := range exported {
		content, err := Files.ReadFile(name)
		if err != nil {
			return Result{}, fmt.Errorf("读取内嵌的 %s 失败：%w", name, err)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return Result{}, fmt.Errorf("写入 %s 失败：%w", path, err)
		}
		result.Files = append(result.Files, name)
	}
	return result, nil
}
