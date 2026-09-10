package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"
	"time"
)

// modelRepoBase ModelScope 仓库「文件直链」端点（resolve 会 302 跳转到带签名 key 的 CDN）。
// HuggingFace 在国内不可达（实测 502），统一用 ModelScope 镜像作为唯一下载源。
const modelRepoBase = "https://www.modelscope.cn/models/getcharzp/go-ocr/resolve/master/"

// assetFile 描述一个需下载的模型资产：仓库内相对路径 = 本地存储相对路径。
type assetFile struct {
	repoRel string // 仓库内路径，同时作为本地存储路径
	size    int64  // 预期大小（0 表示未知，仅用于展示）
}

// libRel 返回当前平台所需的 onnxruntime 动态库相对路径。
func libRel() string {
	switch runtime.GOOS {
	case "windows":
		return "lib/onnxruntime.dll"
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return "lib/onnxruntime_arm64.dylib"
		}
		return "lib/onnxruntime_amd64.dylib"
	case "linux":
		if runtime.GOARCH == "arm64" {
			return "lib/onnxruntime_arm64.so"
		}
		return "lib/onnxruntime_amd64.so"
	}
	return "lib/onnxruntime_amd64.so"
}

// requiredAssets 返回 PaddleOCR 所需的全部资产清单（det/rec 模型 + 字典 + 平台动态库）。
func requiredAssets() []assetFile {
	return []assetFile{
		{"paddle_weights/det.onnx", 0},
		{"paddle_weights/rec.onnx", 0},
		{"paddle_weights/dict.txt", 0},
		{libRel(), 0},
	}
}

// assetProgress 单个文件的下载进度（供前端展示）。
type assetProgress struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Done int64  `json:"done"`
	OK   bool   `json:"ok"`
}

// downloadState 全局下载状态。startDownload 后台运行，前端通过 ocr-status 轮询快照。
type downloadState struct {
	mu      sync.Mutex
	active  bool
	current int // 当前正在下载第几个（1-based）
	total   int
	files   []assetProgress
	errMsg  string
}

var dl = &downloadState{}

func (d *downloadState) snapshot() map[string]interface{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	files := make([]assetProgress, len(d.files))
	copy(files, d.files)
	return map[string]interface{}{
		"active":  d.active,
		"current": d.current,
		"total":   d.total,
		"files":   files,
		"errMsg":  d.errMsg,
	}
}

// modelsDir 返回模型存储目录（由 initialize 解析 pluginDir 后设置，兜底用 exe 同目录）。
func modelsDir() string {
	if modelsDirCache != "" {
		return modelsDirCache
	}
	if e, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(e), "models")
	}
	return filepath.Join(os.TempDir(), "ocr-tool-models")
}

// allAssetsPresent 检查所有必需资产是否已下载且非空。
func allAssetsPresent(dir string) bool {
	for _, a := range requiredAssets() {
		p := filepath.Join(dir, a.repoRel)
		fi, err := os.Stat(p)
		if err != nil || fi.Size() == 0 {
			return false
		}
	}
	return true
}

// isDownloading 返回后台下载是否进行中。
func isDownloading() bool {
	dl.mu.Lock()
	defer dl.mu.Unlock()
	return dl.active
}

// startDownload 在后台下载所有缺失资产（幂等：已在进行中则直接返回）。
func startDownload() {
	dl.mu.Lock()
	if dl.active {
		dl.mu.Unlock()
		return
	}
	dl.active = true
	dl.errMsg = ""
	assets := requiredAssets()
	dl.total = len(assets)
	dl.current = 0
	dl.files = make([]assetProgress, len(assets))
	for i, a := range assets {
		dl.files[i] = assetProgress{Name: filepath.Base(a.repoRel)}
	}
	dl.mu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logf("startDownload panic: %v\n%s", r, debug.Stack())
				fmt.Fprintf(os.Stderr, "ocr-tool startDownload panic: %v\n", r)
				dl.mu.Lock()
				dl.active = false
				dl.errMsg = fmt.Sprintf("下载线程崩溃: %v", r)
				dl.mu.Unlock()
				hostLog("error", "模型下载线程崩溃: %v", r)
			}
		}()
		defer func() {
			dl.mu.Lock()
			dl.active = false
			dl.mu.Unlock()
		}()

		dir := modelsDir()
		for i, a := range assets {
			dl.mu.Lock()
			dl.current = i + 1
			dl.mu.Unlock()
			if err := downloadAsset(dir, a, i); err != nil {
				dl.mu.Lock()
				dl.errMsg = fmt.Sprintf("下载 %s 失败: %v", a.repoRel, err)
				dl.mu.Unlock()
				hostLog("error", "模型下载失败 %s: %v", a.repoRel, err)
				return
			}
			dl.mu.Lock()
			dl.files[i].OK = true
			dl.mu.Unlock()
		}
		// 下载完成后重置引擎，使其下次识别时重新加载新模型。
		eng.reset()
		hostLog("info", "模型下载完成，目录: %s", dir)
	}()
}

// downloadAsset 下载单个资产：先下到 .part，完成后原子 rename。边下边更新进度。
// 30 分钟总墙钟 + 2 分钟静默无进展即 abort（用 context + stallTimer 实现）。
func downloadAsset(dir string, a assetFile, idx int) error {
	url := modelRepoBase + a.repoRel
	dest := filepath.Join(dir, a.repoRel)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	part := dest + ".part"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// 静默超时：每读到一个 chunk 就 Reset；超过 stallThreshold 没有新数据就 cancel 整个请求
	stallThreshold := 2 * time.Minute
	stallTimer := time.AfterFunc(stallThreshold, func() {
		defer func() { _ = recover() }()
		cancel()
	})
	defer stallTimer.Stop()

	f, err := os.Create(part)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 30 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		f.Close()
		os.Remove(part)
		return err
	}
	req.Header.Set("User-Agent", "QuickDock-OCR-Plugin/2.0")
	resp, err := client.Do(req)
	if err != nil {
		f.Close()
		os.Remove(part)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 早返回前排空 body，让 keepalive socket 能复用
		_, _ = io.Copy(io.Discard, resp.Body)
		f.Close()
		os.Remove(part)
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	dl.mu.Lock()
	dl.files[idx].Size = resp.ContentLength
	dl.mu.Unlock()

	stallTimer.Stop() // 收到首字节后再启动定时器
	stallTimer = time.AfterFunc(stallThreshold, func() {
		defer func() { _ = recover() }()
		cancel()
	})
	defer stallTimer.Stop()

	buf := make([]byte, 64*1024)
	var done int64
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			stallTimer.Reset(stallThreshold)
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				os.Remove(part)
				return werr
			}
			done += int64(n)
			dl.mu.Lock()
			dl.files[idx].Done = done
			dl.mu.Unlock()
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(part)
			return rerr
		}
		if ctx.Err() != nil {
			f.Close()
			os.Remove(part)
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("下载停滞超过 %s，已中止", stallThreshold)
			}
			return ctx.Err()
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(part)
		return err
	}
	if err := os.Rename(part, dest); err != nil {
		os.Remove(part)
		return err
	}
	return nil
}
