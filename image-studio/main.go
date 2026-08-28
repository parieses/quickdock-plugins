// Image Studio - 图片工坊（仿 Squoosh 实时对比体验）
// JSON-RPC 2.0 over stdin/stdout (native 插件协议)
// 命令：
//   preview 原图信息 + 缩略
//   process 组合操作（缩放/旋转/翻转/亮度对比饱和/格式/质量），save=false 实时预览不落盘，save=true 写盘

package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// ---- 图像基础 ----

func decodeImage(path string) (image.Image, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	img, format, err := image.Decode(f)
	if err != nil {
		return nil, "", fmt.Errorf("不支持的图片格式或文件损坏")
	}
	return img, format, nil
}

func toRGBA(img image.Image) *image.RGBA {
	if r, ok := img.(*image.RGBA); ok {
		return r
	}
	b := img.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, img, b.Min, draw.Src)
	return dst
}

func clamp(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// ---- 调整：亮度 / 对比度 / 饱和度（-100 ~ 100）----

func adjust(img image.Image, brightness, contrast, saturation int) image.Image {
	if brightness == 0 && contrast == 0 && saturation == 0 {
		return img
	}
	src := toRGBA(img)
	b := src.Bounds()
	dst := image.NewRGBA(b)
	bf := float64(brightness) / 100.0 * 255.0
	cf := 1.0 + float64(contrast)/100.0
	sf := 1.0 + float64(saturation)/100.0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := src.At(x, y).RGBA()
			rf, gf, bf32 := float64(r>>8), float64(g>>8), float64(bl>>8)
			rf += bf
			gf += bf
			bf32 += bf
			rf = (rf-128)*cf + 128
			gf = (gf-128)*cf + 128
			bf32 = (bf32-128)*cf + 128
			gray := 0.299*rf + 0.587*gf + 0.114*bf32
			rf = gray + (rf-gray)*sf
			gf = gray + (gf-gray)*sf
			bf32 = gray + (bf32-gray)*sf
			dst.SetRGBA(x, y, color.RGBA{clamp(rf), clamp(gf), clamp(bf32), uint8(a >> 8)})
		}
	}
	return dst
}

// ---- 旋转 / 翻转 ----

func rotate90(img image.Image) *image.RGBA {
	src := img.Bounds()
	w, h := src.Dx(), src.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(h-1-y, x, img.At(src.Min.X+x, src.Min.Y+y))
		}
	}
	return dst
}

func rotate180(img image.Image) *image.RGBA {
	src := img.Bounds()
	w, h := src.Dx(), src.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(w-1-x, h-1-y, img.At(src.Min.X+x, src.Min.Y+y))
		}
	}
	return dst
}

func rotate270(img image.Image) *image.RGBA {
	src := img.Bounds()
	w, h := src.Dx(), src.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(y, w-1-x, img.At(src.Min.X+x, src.Min.Y+y))
		}
	}
	return dst
}

func flipH(img image.Image) *image.RGBA {
	src := img.Bounds()
	w, h := src.Dx(), src.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(w-1-x, y, img.At(src.Min.X+x, src.Min.Y+y))
		}
	}
	return dst
}

func flipV(img image.Image) *image.RGBA {
	src := img.Bounds()
	w, h := src.Dx(), src.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(x, h-1-y, img.At(src.Min.X+x, src.Min.Y+y))
		}
	}
	return dst
}

// ---- 缩放 / 裁剪 ----

func scale(img image.Image, w, h int) image.Image {
	src := img.Bounds()
	if w <= 0 && h <= 0 {
		return img
	}
	if w <= 0 {
		w = src.Dx() * h / src.Dy()
	}
	if h <= 0 {
		h = src.Dy() * w / src.Dx()
	}
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, src.Bounds(), draw.Over, nil)
	return dst
}

func cropCenter(img image.Image, w, h int) image.Image {
	src := img.Bounds()
	if w <= 0 {
		w = src.Dx()
	}
	if h <= 0 {
		h = src.Dy()
	}
	if w > src.Dx() {
		w = src.Dx()
	}
	if h > src.Dy() {
		h = src.Dy()
	}
	x0 := (src.Dx() - w) / 2
	y0 := (src.Dy() - h) / 2
	rect := image.Rect(x0, y0, x0+w, y0+h)
	switch im := img.(type) {
	case *image.RGBA:
		return im.SubImage(rect)
	case *image.YCbCr:
		return im.SubImage(rect)
	case *image.NRGBA:
		return im.SubImage(rect)
	default:
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(dst, dst.Bounds(), img, rect.Min, draw.Src)
		return dst
	}
}

// ---- 编码 / 预览 ----

func encode(img image.Image, format string, quality int) ([]byte, error) {
	var buf bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&buf, img)
	case "jpeg", "jpg":
		if quality < 1 {
			quality = 80
		}
		if quality > 100 {
			quality = 100
		}
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality})
	default:
		return nil, fmt.Errorf("不支持的输出格式: %s", format)
	}
	return buf.Bytes(), err
}

func toPreview(img image.Image) string {
	scaled := img
	if img.Bounds().Dx() > 400 {
		scaled = scale(img, 400, 0)
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: 82})
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// ---- JSON-RPC ----

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type executeParams struct {
	Command string                 `json:"command"`
	Input   map[string]interface{} `json:"input"`
}

func strFrom(input map[string]interface{}, key string) string {
	if v, ok := input[key].(string); ok {
		return v
	}
	return ""
}

func intFrom(input map[string]interface{}, key string) int {
	if v, ok := input[key].(float64); ok {
		return int(v)
	}
	return 0
}

func boolFrom(input map[string]interface{}, key string) bool {
	if v, ok := input[key].(bool); ok {
		return v
	}
	return false
}

func respond(id int64, result interface{}) {
	out, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Println(string(out))
}

func respondError(id int64, code int, msg string) {
	out, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]interface{}{"code": code, "message": msg},
	})
	fmt.Println(string(out))
}

func handlePreview(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	img, format, err := decodeImage(path)
	if err != nil {
		respondError(id, -1, err.Error())
		return
	}
	st, _ := os.Stat(path)
	size := int64(0)
	if st != nil {
		size = st.Size()
	}
	respond(id, map[string]interface{}{
		"format": format,
		"width":  img.Bounds().Dx(),
		"height": img.Bounds().Dy(),
		"size":   size,
		"preview": toPreview(img),
	})
}

// applyOps 按顺序应用操作：调整 → 旋转 → 翻转 → 缩放/裁剪
func applyOps(img image.Image, p map[string]interface{}) image.Image {
	out := img
	out = adjust(out, intFrom(p, "brightness"), intFrom(p, "contrast"), intFrom(p, "saturation"))
	switch intFrom(p, "rotate") {
	case 90:
		out = rotate90(out)
	case 180:
		out = rotate180(out)
	case 270:
		out = rotate270(out)
	}
	if boolFrom(p, "flipH") {
		out = flipH(out)
	}
	if boolFrom(p, "flipV") {
		out = flipV(out)
	}
	if intFrom(p, "cropW") > 0 || intFrom(p, "cropH") > 0 {
		out = cropCenter(out, intFrom(p, "cropW"), intFrom(p, "cropH"))
	} else if intFrom(p, "percent") > 0 {
		out = scale(out, out.Bounds().Dx()*intFrom(p, "percent")/100, out.Bounds().Dy()*intFrom(p, "percent")/100)
	} else if intFrom(p, "width") > 0 || intFrom(p, "height") > 0 {
		out = scale(out, intFrom(p, "width"), intFrom(p, "height"))
	}
	return out
}

func handleProcess(id int64, input map[string]interface{}) {
	path := strFrom(input, "path")
	format := strFrom(input, "format")
	quality := intFrom(input, "quality")
	save := boolFrom(input, "save")

	img, srcFormat, err := decodeImage(path)
	if err != nil {
		respondError(id, -1, err.Error())
		return
	}
	if format == "" {
		format = srcFormat
	}
	if format == "jpg" {
		format = "jpeg"
	}

	out := applyOps(img, input)
	data, err := encode(out, format, quality)
	if err != nil {
		respondError(id, -1, err.Error())
		return
	}

	resp := map[string]interface{}{
		"width":     out.Bounds().Dx(),
		"height":    out.Bounds().Dy(),
		"size":      len(data),
		"srcFormat": srcFormat,
		"outFormat": format,
		"preview":   toPreview(out),
	}

	if save {
		ext := ".jpg"
		if format == "png" {
			ext = ".png"
		}
		base := strings.TrimSuffix(path, filepath.Ext(path))
		outPath := base + "_edited" + ext
		if werr := os.WriteFile(outPath, data, 0644); werr != nil {
			respondError(id, -1, "保存失败: "+werr.Error())
			return
		}
		resp["outPath"] = outPath
	}

	respond(id, resp)
}

func dispatch(raw string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		respondError(0, -32700, "parse error: "+err.Error())
		return
	}
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{"status": "ready", "name": "QuickDock Image Studio"})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		var params executeParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				respondError(req.ID, -32602, "invalid params: "+err.Error())
				return
			}
		}
		switch strings.ToLower(strings.TrimSpace(params.Command)) {
		case "preview":
			handlePreview(req.ID, params.Input)
		case "process":
			handleProcess(req.ID, params.Input)
		default:
			respondError(req.ID, -32601, "unknown command: "+params.Command)
		}
	default:
		respondError(req.ID, -32601, "unknown method: "+req.Method)
	}
}

func main() {
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		data := strings.TrimSpace(line)
		if data == "" {
			continue
		}
		dispatch(data)
	}
}
