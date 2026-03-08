package version

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	json "github.com/goccy/go-json"
)

const (
	Version    = "2.0.0"
	Owner      = "Chocola-X"
	Repo       = "NekoIPinfo"
	APIBaseURL = "https://api.github.com/repos/" + Owner + "/" + Repo
)

type ReleaseInfo struct {
	TagName string        `json:"tag_name"`
	Name    string        `json:"name"`
	Body    string        `json:"body"`
	Assets  []ReleaseAsset `json:"assets"`
}

type ReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

func GetCurrent() string {
	return Version
}

func FetchLatestRelease() (*ReleaseInfo, error) {
	url := APIBaseURL + "/releases/latest"
	return fetchRelease(url)
}

func FetchRelease(tag string) (*ReleaseInfo, error) {
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	url := APIBaseURL + "/releases/tags/" + tag
	return fetchRelease(url)
}

func fetchRelease(url string) (*ReleaseInfo, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "NekoIPinfo/"+Version)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("未找到该版本")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API 返回 HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var info ReleaseInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func NormalizeTag(tag string) string {
	return strings.TrimPrefix(tag, "v")
}

func CompareVersions(current, remote string) int {
	c := parseVersionParts(NormalizeTag(current))
	r := parseVersionParts(NormalizeTag(remote))
	for i := 0; i < 3; i++ {
		ci, ri := 0, 0
		if i < len(c) {
			ci = c[i]
		}
		if i < len(r) {
			ri = r[i]
		}
		if ci < ri {
			return -1
		}
		if ci > ri {
			return 1
		}
	}
	return 0
}

func parseVersionParts(v string) []int {
	parts := strings.SplitN(v, ".", 3)
	result := make([]int, len(parts))
	for i, p := range parts {
		n := 0
		for _, ch := range p {
			if ch >= '0' && ch <= '9' {
				n = n*10 + int(ch-'0')
			} else {
				break
			}
		}
		result[i] = n
	}
	return result
}

func CheckUpdate() (*ReleaseInfo, bool, error) {
	info, err := FetchLatestRelease()
	if err != nil {
		return nil, false, err
	}
	cmp := CompareVersions(Version, info.TagName)
	return info, cmp < 0, nil
}

func SelectAsset(info *ReleaseInfo) *ReleaseAsset {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	archMap := map[string][]string{
		"amd64": {"x86_64", "amd64"},
		"386":   {"x86", "386", "i386"},
		"arm64": {"arm64", "aarch64"},
		"arm":   {"arm", "armv7"},
	}

	candidates := archMap[goarch]
	if len(candidates) == 0 {
		candidates = []string{goarch}
	}

	for _, asset := range info.Assets {
		lower := strings.ToLower(asset.Name)
		if !strings.Contains(lower, goos) {
			continue
		}
		for _, arch := range candidates {
			if strings.Contains(lower, strings.ToLower(arch)) {
				a := asset
				return &a
			}
		}
	}
	return nil
}

func DownloadAndReplace(asset *ReleaseAsset) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(asset.BrowserDownloadURL)
	if err != nil {
		return fmt.Errorf("下载失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取可执行文件路径失败: %v", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("解析符号链接失败: %v", err)
	}

	tmpPath := execPath + ".update.tmp"
	oldPath := execPath + ".old"

	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %v", err)
	}

	_, err = io.Copy(tmpFile, resp.Body)
	tmpFile.Close()
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("写入临时文件失败: %v", err)
	}

	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("设置权限失败: %v", err)
	}

	os.Remove(oldPath)
	if err := os.Rename(execPath, oldPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("备份旧文件失败: %v", err)
	}

	if err := os.Rename(tmpPath, execPath); err != nil {
		os.Rename(oldPath, execPath)
		return fmt.Errorf("替换文件失败: %v", err)
	}

	os.Remove(oldPath)
	return nil
}

func PrintVersion() {
	fmt.Printf("NekoIPinfo v%s\n", Version)
	fmt.Printf("  OS/Arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("  Go:      %s\n", runtime.Version())

	info, hasUpdate, err := CheckUpdate()
	if err != nil {
		fmt.Printf("  最新版本: 检查失败 (%v)\n", err)
		return
	}
	if hasUpdate {
		fmt.Printf("  最新版本: %s (有新版本可用！使用 -update 更新)\n", NormalizeTag(info.TagName))
	} else {
		fmt.Printf("  最新版本: %s (已是最新)\n", NormalizeTag(info.TagName))
	}
}

func DoUpdate(targetVersion string) {
	var info *ReleaseInfo
	var err error

	if targetVersion == "" {
		fmt.Println("正在检查更新...")
		var hasUpdate bool
		info, hasUpdate, err = CheckUpdate()
		if err != nil {
			fmt.Printf("检查更新失败: %v\n", err)
			os.Exit(1)
		}
		if !hasUpdate {
			fmt.Printf("已是最新版本 (v%s)\n", Version)
			os.Exit(0)
		}
		fmt.Printf("发现新版本: %s -> %s\n", Version, NormalizeTag(info.TagName))
	} else {
		fmt.Printf("正在获取版本 %s ...\n", targetVersion)
		info, err = FetchRelease(targetVersion)
		if err != nil {
			fmt.Printf("获取版本失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("目标版本: %s\n", NormalizeTag(info.TagName))
	}

	asset := SelectAsset(info)
	if asset == nil {
		fmt.Printf("未找到适用于 %s/%s 的构建产物\n", runtime.GOOS, runtime.GOARCH)
		fmt.Println("可用的构建产物:")
		for _, a := range info.Assets {
			fmt.Printf("  - %s\n", a.Name)
		}
		os.Exit(1)
	}

	fmt.Printf("正在下载: %s (%.2f MB)\n", asset.Name, float64(asset.Size)/1024/1024)
	if err := DownloadAndReplace(asset); err != nil {
		fmt.Printf("更新失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("更新成功！已更新到 %s\n", NormalizeTag(info.TagName))
	fmt.Println("请重新启动程序")
	os.Exit(0)
}