// Photo Library - 本地照片库浏览（原生 JSON-RPC 子进程）
//
// 架构：native runtime，Go 子进程直接用 os 读目录/读文件，绕过宿主 host.fs.* 的
// 白名单（照片库要能打开任意用户目录）。宿主对 plugin.execute 有 20s 超时、ping 5s，
// 故每个请求独立 goroutine，host.ping 永远秒回（pdf-toolkit v0.1.6 根治的整类死法）。
//
// 命令：
//   scan    列出某目录下的图片文件与子目录（仅元数据，不读像素）
//   thumb   批量解码并缩放生成缩略图（base64 dataURL，≤12 张/次，防 1MB 截断）
//   preview 生成单张大图预览（≤900px，base64 dataURL）
//   exif    解析单张图片的 EXIF 元数据（JPEG，依赖 goexif）
package main

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rwcarlsen/goexif/exif"
	"github.com/rwcarlsen/goexif/tiff"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// ---- JSON-RPC 结构 ----

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type executeParams struct {
	Command string                 `json:"command"`
	Input   map[string]interface{} `json:"input"`
}

// ---- 运行时 ----

var (
	writeMu sync.Mutex
	stdout  = bufio.NewWriter(os.Stdout)
)

// 支持的图片扩展名（缩略图仅对 jpeg/png/gif 真正解码，其它格式前端显示占位）
var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".bmp": true,
	".webp": true, ".tif": true, ".tiff": true,
}

const (
	thumbMax   = 220
	previewMax = 900
	scanLimit  = 3000
)

func main() {
	reader := bufio.NewReader(os.Stdin)
	var wg sync.WaitGroup
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		data := strings.TrimSpace(line)
		if data == "" {
			continue
		}
		wg.Add(1)
		go func(raw string) {
			defer wg.Done()
			dispatchWithRecover(raw)
		}(data)
	}
	wg.Wait()
}

func dispatchWithRecover(raw string) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "[panic] photo-library: %v\n%s\n", r, debug.Stack())
			var pre struct {
				ID int64 `json:"id"`
			}
			_ = json.Unmarshal([]byte(raw), &pre)
			respondError(pre.ID, -32603, "internal error")
		}
	}()
	dispatch(raw)
}

func dispatch(data string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(data), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Photo Library"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		handleExecute(req)
	default:
		respondError(req.ID, -32601, "unknown method: "+req.Method)
	}
}

func handleExecute(req rpcRequest) {
	var params executeParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			respondError(req.ID, -32602, "invalid params: "+err.Error())
			return
		}
	}
	cmd := strings.ToLower(strings.TrimSpace(params.Command))
	switch cmd {
	case "scan":
		handleScan(req.ID, params.Input)
	case "thumb":
		handleThumb(req.ID, params.Input)
	case "preview":
		handlePreview(req.ID, params.Input)
	case "exif":
		handleExif(req.ID, params.Input)
	case "phash":
		handlePhash(req.ID, params.Input)
	case "fav":
		handleFav(req.ID, params.Input)
	case "library":
		handleLibrary(req.ID, params.Input)
	case "edit":
		handleEdit(req.ID, params.Input)
	case "saveedit":
		handleSaveEdit(req.ID, params.Input)
	case "batch":
		handleBatch(req.ID, params.Input)
	case "session":
		handleSession(req.ID, params.Input)
	case "config":
		handleConfig(req.ID, params.Input)
	default:
		respondError(req.ID, -32601, "unknown command: "+params.Command)
	}
}

// ---- scan：列出目录内图片文件与子目录（仅元数据） ----

type fileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Ext   string `json:"ext"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
	IsDir bool   `json:"isDir"`
}

func handleScan(id int64, input map[string]interface{}) {
	if isLibMode(input) {
		respond(id, scanLibrary())
		return
	}
	root := strings.TrimSpace(strFrom(input, "path"))
	if root == "" {
		respondError(id, -32602, "缺少 path 参数")
		return
	}
	info, err := os.Stat(root)
	if err != nil {
		respondError(id, -32603, "无法访问目录: "+err.Error())
		return
	}
	if !info.IsDir() {
		respondError(id, -32603, "路径不是目录: "+root)
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		respondError(id, -32603, "读取目录失败: "+err.Error())
		return
	}
	var files, folders []fileEntry
	truncated := false
	for _, e := range entries {
		if len(files)+len(folders) >= scanLimit {
			truncated = true
			break
		}
		fp := filepath.Join(root, e.Name())
		if e.IsDir() {
			folders = append(folders, fileEntry{Name: e.Name(), Path: fp, IsDir: true})
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if !imageExts[ext] {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileEntry{
			Name:  e.Name(),
			Path:  fp,
			Ext:   ext,
			Size:  fi.Size(),
			MTime: fi.ModTime().Unix(),
		})
	}
	respond(id, map[string]interface{}{
		"ok":        true,
		"path":      root,
		"files":     files,
		"folders":   folders,
		"total":     len(files),
		"truncated": truncated,
	})
	saveSession("folder", root)
}

// ---- thumb：批量生成缩略图（base64 dataURL） ----

func handleThumb(id int64, input map[string]interface{}) {
	paths := strSlice(input, "paths")
	if len(paths) == 0 {
		respondError(id, -32602, "缺少 paths 参数")
		return
	}
	if len(paths) > 12 {
		paths = paths[:12]
	}
	items := make([]map[string]interface{}, 0, len(paths))
	for _, p := range paths {
		item := map[string]interface{}{"path": p}
		dataURL, w, h, err := makeThumb(p, thumbMax)
		if err != nil {
			item["ok"] = false
			if strings.Contains(err.Error(), "unsupported") {
				item["kind"] = "unsupported"
			} else {
				item["kind"] = "error"
			}
		} else {
			item["ok"] = true
			item["dataUrl"] = dataURL
			item["width"] = w
			item["height"] = h
		}
		items = append(items, item)
	}
	respond(id, map[string]interface{}{"ok": true, "items": items})
}

func handlePreview(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	if path == "" {
		respondError(id, -32602, "缺少 path 参数")
		return
	}
	dataURL, w, h, err := makeThumb(path, previewMax)
	if err != nil {
		respondError(id, -32603, "生成预览失败: "+err.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true, "dataUrl": dataURL, "width": w, "height": h})
}

// makeThumb 解码图片 → 等比缩放到 maxDim 以内 → 重新编码为 JPEG dataURL。
// 返回 dataURL、缩放后宽高。非 jpeg/png/gif 格式返回 unsupported 错误。
func makeThumb(path string, maxDim int) (string, int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", 0, 0, err
	}
	ck := thumbCachePath(path, fi.ModTime().Unix(), maxDim)
	if b, ok := readCache(ck); ok {
		if cfg, _, e := image.DecodeConfig(bytes.NewReader(b)); e == nil {
			return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b), cfg.Width, cfg.Height, nil
		}
	}
	img, format, err := image.Decode(f)
	if err != nil {
		return "", 0, 0, fmt.Errorf("unsupported format: %w", err)
	}
	resized := resizeBox(img, maxDim)
	if format != "jpeg" {
		// 非 JPEG（PNG 透明 / WebP / TIFF 等）合成到白底，避免 jpeg 编码后变黑块
		resized = flattenOnWhite(resized)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, resized, &jpeg.Options{Quality: 82}); err != nil {
		return "", 0, 0, err
	}
	b := buf.Bytes()
	writeCache(ck, b)
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b),
		resized.Bounds().Dx(), resized.Bounds().Dy(), nil
}

func flattenOnWhite(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(dst, b, src, b.Min, draw.Over)
	return dst
}

// resizeBox 朴素箱式降采样：把 src 等比缩放到最长边 ≤ maxDim。
// 纯 stdlib，无额外依赖；对缩略图质量足够。
func resizeBox(src image.Image, maxDim int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || (w <= maxDim && h <= maxDim) {
		return src
	}
	var nw, nh int
	if w >= h {
		nw = maxDim
		nh = int(float64(h)*float64(maxDim)/float64(w) + 0.5)
	} else {
		nh = maxDim
		nw = int(float64(w)*float64(maxDim)/float64(h) + 0.5)
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	sw := float64(w) / float64(nw)
	sh := float64(h) / float64(nh)
	for y := 0; y < nh; y++ {
		y0 := int(float64(y) * sh)
		y1 := int(float64(y+1) * sh)
		if y1 <= y0 {
			y1 = y0 + 1
		}
		if y1 > h {
			y1 = h
		}
		for x := 0; x < nw; x++ {
			x0 := int(float64(x) * sw)
			x1 := int(float64(x+1) * sw)
			if x1 <= x0 {
				x1 = x0 + 1
			}
			if x1 > w {
				x1 = w
			}
			var r, g, bl, a, cnt uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					pr, pg, pb, pa := src.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
					r += uint64(pr)
					g += uint64(pg)
					bl += uint64(pb)
					a += uint64(pa)
					cnt++
				}
			}
			ar := uint16(r / cnt)
			ag := uint16(g / cnt)
			ab := uint16(bl / cnt)
			aa := uint16(a / cnt)
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8(ar >> 8),
				G: uint8(ag >> 8),
				B: uint8(ab >> 8),
				A: uint8(aa >> 8),
			})
		}
	}
	return dst
}

// ---- exif：解析单张图片 EXIF 元数据 ----

type visitor struct {
	tags map[string]interface{}
}

func (v *visitor) Walk(name exif.FieldName, tag *tiff.Tag) error {
	v.tags[string(name)] = tag.String()
	return nil
}

func handleExif(id int64, input map[string]interface{}) {
	path := strings.TrimSpace(strFrom(input, "path"))
	if path == "" {
		respondError(id, -32602, "缺少 path 参数")
		return
	}
	width, height, _ := imageDimensions(path)
	out := map[string]interface{}{
		"ok":     true,
		"path":   path,
		"width":  width,
		"height": height,
	}

	f, err := os.Open(path)
	if err != nil {
		respond(id, out)
		return
	}
	defer f.Close()

	head := make([]byte, 3)
	if _, err := io.ReadFull(f, head); err != nil {
		respond(id, out)
		return
	}
	if !bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}) { // 非 JPEG
		out["exifAvailable"] = false
		out["note"] = "该格式（非 JPEG）通常不含标准 EXIF 信息"
		respond(id, out)
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		respond(id, out)
		return
	}
	x, err := exif.Decode(f)
	if err != nil {
		out["exifAvailable"] = false
		out["note"] = "该图片未包含 EXIF 元数据"
		respond(id, out)
		return
	}
	out["exifAvailable"] = true

	if tm, e := x.DateTime(); e == nil {
		out["datetime"] = tm.Format("2006-01-02 15:04:05")
	}
	if lat, lon, e := x.LatLong(); e == nil {
		out["gps"] = map[string]interface{}{"lat": lat, "lng": lon}
	}

	pick := func(name exif.FieldName) string {
		if v, err := x.Get(name); err == nil {
			return strings.TrimSpace(v.String())
		}
		return ""
	}
	fields := map[string]exif.FieldName{
		"make":            exif.Make,
		"model":           exif.Model,
		"lensMake":        exif.LensMake,
		"lensModel":       exif.LensModel,
		"fNumber":         exif.FNumber,
		"exposureTime":    exif.ExposureTime,
		"iso":             exif.ISOSpeedRatings,
		"focalLength":     exif.FocalLength,
		"exposureProgram": exif.ExposureProgram,
		"meteringMode":    exif.MeteringMode,
		"flash":           exif.Flash,
		"whiteBalance":    exif.WhiteBalance,
		"orientation":     exif.Orientation,
		"colorSpace":      exif.ColorSpace,
		"software":        exif.Software,
		"dateOrig":        exif.DateTimeOriginal,
	}
	curated := map[string]string{}
	for k, fn := range fields {
		if s := pick(fn); s != "" {
			curated[k] = s
		}
	}
	out["fields"] = curated

	vis := &visitor{tags: map[string]interface{}{}}
	_ = x.Walk(vis)
	out["tags"] = vis.tags

	respond(id, out)
}

func imageDimensions(path string) (int, int, string) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, ""
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, ""
	}
	return cfg.Width, cfg.Height, format
}

// ---- 资料库 / 收藏：持久化到用户缓存目录（不污染照片目录） ----
func getCacheDir() string {
	cacheDirOnce.Do(func() {
		base, err := os.UserCacheDir()
		if err != nil || base == "" {
			base = os.TempDir()
		}
		cacheDirVal = filepath.Join(base, "quickdock-photo-library")
		_ = os.MkdirAll(cacheDirVal, 0o755)
	})
	return cacheDirVal
}

var cacheDirOnce sync.Once
var cacheDirVal string

func thumbCachePath(path string, mtime int64, maxDim int) string {
	h := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d", path, mtime, maxDim)))
	return filepath.Join(getCacheDir(), fmt.Sprintf("t_%x.jpg", h))
}

func readCache(p string) ([]byte, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	return b, true
}

func writeCache(p string, b []byte) {
	pruneCache()
	_ = os.WriteFile(p, b, 0o644)
}

// pruneCache 限制缩略图缓存目录体积：仅保留最近 maxCache 个文件，删除最旧的
func pruneCache() {
	const maxCache = 2000
	dir := getCacheDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	if len(entries) <= maxCache {
		return
	}
	type fe struct {
		name string
		mod  time.Time
	}
	files := make([]fe, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fe{e.Name(), fi.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	for i := maxCache; i < len(files); i++ {
		_ = os.Remove(filepath.Join(dir, files[i].name))
	}
}

func loadJSON(path string, v interface{}) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func saveJSON(path string, v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func favPath() string { return filepath.Join(getCacheDir(), "favorites.json") }
func libPath() string { return filepath.Join(getCacheDir(), "library.json") }

func loadFavs() map[string]bool {
	m := map[string]bool{}
	_ = loadJSON(favPath(), &m)
	return m
}

func loadLib() []string {
	var list []string
	_ = loadJSON(libPath(), &list)
	return list
}

func isLibMode(input map[string]interface{}) bool {
	if v, ok := input["library"]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
		if s, ok := v.(string); ok && s == "1" {
			return true
		}
	}
	return false
}

func scanLibrary() map[string]interface{} {
	roots := loadLib()
	var files []fileEntry
	truncated := false
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if truncated {
				return
			}
			fp := filepath.Join(dir, e.Name())
			if e.IsDir() {
				walk(fp)
				continue
			}
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if !imageExts[ext] {
				continue
			}
			if len(files) >= scanLimit {
				truncated = true
				return
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			files = append(files, fileEntry{
				Name: e.Name(), Path: fp, Ext: ext, Size: fi.Size(), MTime: fi.ModTime().Unix(),
			})
		}
	}
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		walk(root)
	}
	saveSession("library", "")
	return map[string]interface{}{
		"ok": true, "path": "(library)", "files": files, "folders": []fileEntry{},
		"total": len(files), "truncated": truncated, "library": true,
	}
}

func handleFav(id int64, input map[string]interface{}) {
	action := strFrom(input, "action")
	path := strFrom(input, "path")
	m := loadFavs()
	if action == "toggle" && path != "" {
		if m[path] {
			delete(m, path)
		} else {
			m[path] = true
		}
		_ = saveJSON(favPath(), m)
	}
	list := make([]string, 0, len(m))
	for k := range m {
		list = append(list, k)
	}
	respond(id, map[string]interface{}{"ok": true, "favorites": list})
}

func handleLibrary(id int64, input map[string]interface{}) {
	action := strFrom(input, "action")
	path := strFrom(input, "path")
	list := loadLib()
	if action == "add" && path != "" {
		if !containsStr(list, path) {
			list = append(list, path)
		}
		_ = saveJSON(libPath(), list)
	} else if action == "remove" && path != "" {
		list = removeStr(list, path)
		_ = saveJSON(libPath(), list)
	}
	respond(id, map[string]interface{}{"ok": true, "folders": list})
}

func containsStr(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}

func removeStr(a []string, s string) []string {
	out := a[:0]
	for _, v := range a {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

// ---- phash：平均哈希（aHash），用于重复/相似图片检测 ----
func aHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return "", fmt.Errorf("unsupported format: %w", err)
	}
	small := resizeBox(img, 8)
	b := small.Bounds()
	gs := make([]uint8, 64)
	var sum uint64
	i := 0
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			r, g, bl, _ := small.At(b.Min.X+x, b.Min.Y+y).RGBA()
			lum := uint8(((r >> 8) + (g >> 8) + (bl >> 8)) / 3)
			gs[i] = lum
			sum += uint64(lum)
			i++
		}
	}
	avg := uint8(sum / 64)
	var bits uint64
	for i := 0; i < 64; i++ {
		if gs[i] >= avg {
			bits |= 1 << uint(i)
		}
	}
	return fmt.Sprintf("%016x", bits), nil
}

func handlePhash(id int64, input map[string]interface{}) {
	paths := strSlice(input, "paths")
	if len(paths) == 0 {
		respondError(id, -32602, "缺少 paths 参数")
		return
	}
	if len(paths) > 12 {
		paths = paths[:12]
	}
	items := make([]map[string]interface{}, 0, len(paths))
	for _, p := range paths {
		it := map[string]interface{}{"path": p}
		if h, err := aHash(p); err != nil {
			it["ok"] = false
		} else {
			it["ok"] = true
			it["hash"] = h
		}
		items = append(items, it)
	}
	respond(id, map[string]interface{}{"ok": true, "items": items})
}

// ---- 编辑 / 批量 / 会话：纯 Go，无 CGO ----

// 编辑操作：对原图（全分辨率）依次应用 裁剪(归一化坐标) → 旋转(90倍数) → 亮度/对比度/滤镜。
// 全部返回 *image.RGBA，便于前端拿到确定结果。
func handleEdit(id int64, input map[string]interface{}) {
	path := strings.TrimSpace(strFrom(input, "path"))
	if path == "" {
		respondError(id, -32602, "缺少 path 参数")
		return
	}
	ops, _ := input["ops"].(map[string]interface{})
	if ops == nil {
		ops = map[string]interface{}{}
	}
	img, _, err := decodeImage(path)
	if err != nil {
		respondError(id, -32603, "无法解码图片: "+err.Error())
		return
	}
	out, err := applyOps(img, ops)
	if err != nil {
		respondError(id, -32603, "编辑失败: "+err.Error())
		return
	}
	preview := resizeBox(out, 1200)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, preview, &jpeg.Options{Quality: 82}); err != nil {
		respondError(id, -32603, "编码预览失败: "+err.Error())
		return
	}
	b := preview.Bounds()
	respond(id, map[string]interface{}{
		"ok": true,
		"dataUrl": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()),
		"width":   b.Dx(), "height": b.Dy(),
	})
}

func handleSaveEdit(id int64, input map[string]interface{}) {
	path := strings.TrimSpace(strFrom(input, "path"))
	if path == "" {
		respondError(id, -32602, "缺少 path 参数")
		return
	}
	ops, _ := input["ops"].(map[string]interface{})
	if ops == nil {
		ops = map[string]interface{}{}
	}
	img, _, err := decodeImage(path)
	if err != nil {
		respondError(id, -32603, "无法解码图片: "+err.Error())
		return
	}
	out, err := applyOps(img, ops)
	if err != nil {
		respondError(id, -32603, "编辑失败: "+err.Error())
		return
	}
	base := filepath.Base(path)
	ext := strings.ToLower(filepath.Ext(base))
	if ext == "" {
		ext = ".jpg"
	}
	name := strings.TrimSuffix(base, ext) + "_edited" + ext
	newPath := uniquePath(filepath.Join(filepath.Dir(path), name))
	if err := saveImage(out, newPath); err != nil {
		respondError(id, -32603, "保存失败: "+err.Error())
		return
	}
	// 缩略图缓存失效：改名后旧缓存不影响，新文件用新 key
	respond(id, map[string]interface{}{"ok": true, "path": newPath})
}

func handleBatch(id int64, input map[string]interface{}) {
	action := strings.ToLower(strings.TrimSpace(strFrom(input, "action")))
	paths := strSlice(input, "paths")
	failed := []string{}
	switch action {
	case "rotate":
		angle := int(num(input["angle"]))
		count := 0
		for _, p := range paths {
			img, _, err := decodeImage(p)
			if err != nil {
				failed = append(failed, p)
				continue
			}
			out := rotateImage(ensureRGBA(img), angle)
			if err := saveImageOverwrite(out, p); err != nil {
				failed = append(failed, p)
				continue
			}
			count++
		}
		respond(id, map[string]interface{}{"ok": true, "action": "rotate", "count": count, "failed": failed})
	case "rename":
		pattern := strFrom(input, "pattern")
		if pattern == "" {
			pattern = "photo"
		}
		renamed := map[string]string{}
		idx := 1
		for _, p := range paths {
			base := filepath.Base(p)
			ext := strings.ToLower(filepath.Ext(base))
			for {
				num := fmt.Sprintf("%03d", idx)
				target := filepath.Join(filepath.Dir(p), pattern+"_"+num+ext)
				idx++
				if target == p {
					renamed[p] = p
					break
				}
				if _, err := os.Stat(target); err != nil {
					if err := os.Rename(p, target); err != nil {
						failed = append(failed, p)
						break
					}
					renamed[p] = target
					break
				}
			}
		}
		respond(id, map[string]interface{}{"ok": true, "action": "rename", "renamed": renamed, "failed": failed})
	case "export":
		dest := strings.TrimSpace(strFrom(input, "dest"))
		if dest == "" {
			respondError(id, -32602, "缺少 dest 参数")
			return
		}
		if err := os.MkdirAll(dest, 0o755); err != nil {
			respondError(id, -32603, "无法创建目标目录: "+err.Error())
			return
		}
		count := 0
		for _, p := range paths {
			srcB, err := os.ReadFile(p)
			if err != nil {
				failed = append(failed, p)
				continue
			}
			target := uniquePath(filepath.Join(dest, filepath.Base(p)))
			if err := os.WriteFile(target, srcB, 0o644); err != nil {
				failed = append(failed, p)
				continue
			}
			count++
		}
		respond(id, map[string]interface{}{"ok": true, "action": "export", "count": count, "failed": failed, "dest": dest})
	default:
		respondError(id, -32601, "unknown batch action: "+action)
	}
}

func handleSession(id int64, input map[string]interface{}) {
	action := strings.ToLower(strings.TrimSpace(strFrom(input, "action")))
	if action == "set" {
		saveSession(strFrom(input, "mode"), strFrom(input, "path"))
		respond(id, map[string]interface{}{"ok": true})
		return
	}
	mode, path := loadSession()
	respond(id, map[string]interface{}{"ok": true, "mode": mode, "path": path})
}

// ---- 地图 / 插件配置：持久化到用户缓存目录 config.json ----

type pluginConfig struct {
	MapProvider  string `json:"mapProvider"`  // osm | tianditu
	TiandituKey  string `json:"tiandituKey"`  // 天地图 tk（仅在 tianditu 时必填）
}

func configPath() string { return filepath.Join(getCacheDir(), "config.json") }

func loadConfig() pluginConfig {
	var c pluginConfig
	_ = loadJSON(configPath(), &c)
	if c.MapProvider == "" {
		c.MapProvider = "osm"
	}
	return c
}

func handleConfig(id int64, input map[string]interface{}) {
	action := strings.ToLower(strings.TrimSpace(strFrom(input, "action")))
	if action == "set" {
		c := loadConfig()
		if p := strFrom(input, "mapProvider"); p != "" {
			c.MapProvider = p
		}
		// tiandituKey 即便为空也要可清空：仅当 input 里显式带该键时才覆盖
		if _, ok := input["tiandituKey"]; ok {
			c.TiandituKey = strFrom(input, "tiandituKey")
		}
		_ = saveJSON(configPath(), c)
		respond(id, map[string]interface{}{"ok": true, "config": c})
		return
	}
	respond(id, map[string]interface{}{"ok": true, "config": loadConfig()})
}

// ---- 编辑相关工具 ----

func decodeImage(path string) (image.Image, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	img, format, err := image.Decode(f)
	if err != nil {
		return nil, "", fmt.Errorf("unsupported format: %w", err)
	}
	return img, format, nil
}

func ensureRGBA(src image.Image) *image.RGBA {
	if r, ok := src.(*image.RGBA); ok {
		return r
	}
	b := src.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, src, b.Min, draw.Src)
	return dst
}

func subImage(src image.Image, r image.Rectangle) image.Image {
	if si, ok := src.(interface{ SubImage(image.Rectangle) image.Image }); ok {
		return si.SubImage(r)
	}
	b := r
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, src, r.Min, draw.Src)
	return dst
}

// rotateImage 仅支持 90 的倍数（顺时针），非 90 倍数原样返回。
func rotateImage(src *image.RGBA, deg int) *image.RGBA {
	deg = ((deg % 360) + 360) % 360
	if deg == 0 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	switch deg {
	case 90:
		dst := image.NewRGBA(image.Rect(0, 0, h, w))
		for sy := 0; sy < h; sy++ {
			for sx := 0; sx < w; sx++ {
				dst.Set(sy, w-1-sx, src.At(b.Min.X+sx, b.Min.Y+sy))
			}
		}
		return dst
	case 180:
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		for sy := 0; sy < h; sy++ {
			for sx := 0; sx < w; sx++ {
				dst.Set(w-1-sx, h-1-sy, src.At(b.Min.X+sx, b.Min.Y+sy))
			}
		}
		return dst
	case 270:
		dst := image.NewRGBA(image.Rect(0, 0, h, w))
		for sy := 0; sy < h; sy++ {
			for sx := 0; sx < w; sx++ {
				dst.Set(h-1-sy, sx, src.At(b.Min.X+sx, b.Min.Y+sy))
			}
		}
		return dst
	}
	return src
}

// adjustImage 对 *image.RGBA 做 亮度/对比度/滤镜（单遍像素）。
// brightness/contrast 取值 -100..100；filter ∈ none|grayscale|sepia|warm|cool。
func adjustImage(src *image.RGBA, brightness, contrast int, filter string) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	c := float64(contrast) * 2.55
	factor := (259.0*(c+255.0)) / (255.0*(259.0-c))
	bv := float64(brightness) * 2.55
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			i := dst.PixOffset(x, y)
			r := float64(src.Pix[i])
			g := float64(src.Pix[i+1])
			bl := float64(src.Pix[i+2])
			a := src.Pix[i+3]
			if a == 0 {
				dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = src.Pix[i], src.Pix[i+1], src.Pix[i+2], a
				continue
			}
			r += bv
			g += bv
			bl += bv
			r = factor*(r-128) + 128
			g = factor*(g-128) + 128
			bl = factor*(bl-128) + 128
			switch filter {
			case "grayscale":
				lum := 0.299*r + 0.587*g + 0.114*bl
				r, g, bl = lum, lum, lum
			case "sepia":
				nr := 0.393*r + 0.769*g + 0.189*bl
				ng := 0.349*r + 0.686*g + 0.168*bl
				nb := 0.272*r + 0.534*g + 0.131*bl
				r, g, bl = nr, ng, nb
			case "warm":
				r += 22
				bl -= 22
			case "cool":
				r -= 22
				bl += 22
			}
			dst.Pix[i] = clamp8(r)
			dst.Pix[i+1] = clamp8(g)
			dst.Pix[i+2] = clamp8(bl)
			dst.Pix[i+3] = a
		}
	}
	return dst
}

func applyOps(src image.Image, ops map[string]interface{}) (image.Image, error) {
	img := src
	if c, ok := ops["crop"].(map[string]interface{}); ok {
		x, y, ww, hh := num(c["x"]), num(c["y"]), num(c["w"]), num(c["h"])
		if ww > 0 && hh > 0 {
			b := img.Bounds()
			cx := int(clampf(x*float64(b.Dx()), 0, float64(b.Dx())))
			cy := int(clampf(y*float64(b.Dy()), 0, float64(b.Dy())))
			cw := int(clampf(ww*float64(b.Dx()), 1, float64(b.Dx())-float64(cx)))
			ch := int(clampf(hh*float64(b.Dy()), 1, float64(b.Dy())-float64(cy)))
			rect := image.Rect(b.Min.X+cx, b.Min.Y+cy, b.Min.X+cx+cw, b.Min.Y+cy+ch)
			img = subImage(img, rect)
		}
	}
	if d := int(num(ops["rotate"])); d%360 != 0 {
		img = rotateImage(ensureRGBA(img), d)
	}
	bright := int(num(ops["brightness"]))
	contrast := int(num(ops["contrast"]))
	filter := strOr(ops["filter"], "none")
	return adjustImage(ensureRGBA(img), bright, contrast, filter), nil
}

// saveImage 按扩展名选择编码器写入；先写临时文件再重命名，避免半截文件。
func saveImage(img image.Image, path string) error {
	ext := strings.ToLower(filepath.Ext(path))
	var buf bytes.Buffer
	switch ext {
	case ".png":
		if err := png.Encode(&buf, img); err != nil {
			return err
		}
	default:
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 92}); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func saveImageOverwrite(img image.Image, path string) error {
	return saveImage(img, path)
}

func uniquePath(path string) string {
	if _, err := os.Stat(path); err != nil {
		return path
	}
	ext := strings.ToLower(filepath.Ext(path))
	stem := strings.TrimSuffix(path, ext)
	for i := 2; i < 10000; i++ {
		cand := fmt.Sprintf("%s_%d%s", stem, i, ext)
		if _, err := os.Stat(cand); err != nil {
			return cand
		}
	}
	return path
}

// ---- 会话持久化（记住上次浏览位置） ----

func sessionPath() string { return filepath.Join(getCacheDir(), "session.json") }

func saveSession(mode, path string) {
	if mode == "" {
		return
	}
	_ = saveJSON(sessionPath(), map[string]string{"mode": mode, "path": path})
}

func loadSession() (string, string) {
	var m map[string]string
	if err := loadJSON(sessionPath(), &m); err != nil || m == nil {
		return "", ""
	}
	return m["mode"], m["path"]
}

// ---- 数值/字符串 小工具 ----

func num(v interface{}) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	}
	return 0
}

func strOr(v interface{}, def string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}

func clampf(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clamp8(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v + 0.5)
}

// ---- 工具 ----

func strFrom(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func strSlice(m map[string]interface{}, key string) []string {
	if v, ok := m[key].([]interface{}); ok {
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func respond(id int64, result interface{}) {
	payload, err := json.Marshal(result)
	if err != nil {
		respondError(id, -32603, "marshal result failed: "+err.Error())
		return
	}
	writeResponse(rpcResponse{JSONRPC: "2.0", ID: id, Result: payload})
}

func respondError(id int64, code int, msg string) {
	writeResponse(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func writeResponse(resp rpcResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	stdout.Write(data)
	stdout.WriteByte('\n')
	stdout.Flush()
}
