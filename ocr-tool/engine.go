package main

import (
	"image"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/getcharzp/go-ocr"
	"github.com/getcharzp/go-ocr/paddle"
	"github.com/up-zero/gotool/imageutil"
)

// ocrEngine 封装 go-ocr 的 PaddleOCR 引擎，懒加载且全局单例。
// 全部字段受 mu 保护：
//   - 防止 download goroutine 的 reset() 与 OCR goroutine 的 recognize() 并发踩
//   - 允许初始化失败后通过 reset() 清错、重新加载（修复：旧 sync.Once 失败永久卡死）
//   - recognize 整体加锁 → 同进程 OCR 串行（PaddleOCR 引擎非并发安全）
type ocrEngine struct {
	mu      sync.Mutex
	engine  *paddle.Engine
	initErr error
	loaded  bool
}

var eng = &ocrEngine{}

// loadLocked 真正加载引擎。调用方必须持有 mu。
// 幂等：loaded=true 时直接 nil；存在 initErr 时返回旧错（迫使调用方 reset 后再试）。
func (e *ocrEngine) loadLocked(modelsDir string) error {
	if e.loaded && e.engine != nil {
		return nil
	}
	if e.initErr != nil && !e.loaded {
		return e.initErr
	}
	cfg := paddle.Config{
		OnnxRuntimeLibPath: filepath.Join(modelsDir, libRel()),
		DetModelPath:       filepath.Join(modelsDir, "paddle_weights", "det.onnx"),
		RecModelPath:       filepath.Join(modelsDir, "paddle_weights", "rec.onnx"),
		DictPath:           filepath.Join(modelsDir, "paddle_weights", "dict.txt"),
		ThreadCount:        max(1, runtime.NumCPU()),
	}
	logf("loadLocked: 加载 PaddleOCR 引擎 modelDir=%s threads=%d", modelsDir, cfg.ThreadCount)
	e.engine, e.initErr = paddle.NewEngine(cfg)
	if e.initErr != nil {
		e.engine = nil
		e.loaded = false
		logf("loadLocked: 引擎加载失败: %v", e.initErr)
		return e.initErr
	}
	e.loaded = true
	e.initErr = nil
	logf("loadLocked: 引擎加载成功")
	return nil
}

func (e *ocrEngine) prepare(modelsDir string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loadLocked(modelsDir)
}

// reset 释放引擎并允许下次重新加载（如模型下载完成后重置 initErr）。
// 不再依赖 sync.Once → 初始化失败也允许重试。
func (e *ocrEngine) reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.engine != nil {
		e.engine.Destroy()
	}
	e.engine = nil
	e.initErr = nil
	e.loaded = false
}

func (e *ocrEngine) recognize(imgPath string) (OcrResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.loadLocked(modelsDirCache); err != nil {
		return OcrResult{}, err
	}
	img, err := imageutil.Open(imgPath)
	if err != nil {
		return OcrResult{}, err
	}
	res, err := e.engine.RunOCR(img)
	if err != nil {
		return OcrResult{}, err
	}
	return toOcrResult(res, img), nil
}

func toOcrResult(res []ocr.RecResult, img image.Image) OcrResult {
	lines := make([]OcrLine, 0, len(res))
	var b strings.Builder
	for i, r := range res {
		lines = append(lines, OcrLine{Text: r.Text, Box: r.Box, Score: r.Score})
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(r.Text)
	}
	bb := img.Bounds()
	return OcrResult{
		Text:      b.String(),
		Lines:     lines,
		Engine:    "paddle-ocr",
		Width:     bb.Dx(),
		Height:    bb.Dy(),
		LineCount: len(lines),
	}
}
