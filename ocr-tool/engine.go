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
// paddle.NewEngine 内部再初始化 ONNX Runtime（全局 once），首次识别时真正完成加载。
type ocrEngine struct {
	once    sync.Once
	engine  *paddle.Engine
	initErr error
	loaded  bool
}

var eng = &ocrEngine{}

// prepare 在给定模型目录下初始化引擎（幂等：仅第一次真正加载）。
func (e *ocrEngine) prepare(modelsDir string) error {
	e.once.Do(func() {
		cfg := paddle.Config{
			OnnxRuntimeLibPath: filepath.Join(modelsDir, libRel()),
			DetModelPath:       filepath.Join(modelsDir, "paddle_weights", "det.onnx"),
			RecModelPath:       filepath.Join(modelsDir, "paddle_weights", "rec.onnx"),
			DictPath:           filepath.Join(modelsDir, "paddle_weights", "dict.txt"),
			ThreadCount:        max(1, runtime.NumCPU()),
		}
		logf("prepare: 加载 PaddleOCR 引擎 modelDir=%s threads=%d", modelsDir, cfg.ThreadCount)
		e.engine, e.initErr = paddle.NewEngine(cfg)
		e.loaded = e.initErr == nil
		if e.initErr != nil {
			logf("prepare: 引擎加载失败: %v", e.initErr)
		} else {
			logf("prepare: 引擎加载成功")
		}
	})
	return e.initErr
}

// reset 释放引擎并允许下次重新加载（如模型下载完成后）。
func (e *ocrEngine) reset() {
	if e.engine != nil {
		e.engine.Destroy()
	}
	e.engine = nil
	e.initErr = nil
	e.loaded = false
	e.once = sync.Once{}
}

// recognize 对图片做完整 OCR，返回组装后的结果。
// 引擎懒加载，首次调用会真正加载模型；后续调用直接命中已加载实例。
func (e *ocrEngine) recognize(imgPath string) (OcrResult, error) {
	if err := e.prepare(modelsDirCache); err != nil {
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

// toOcrResult 把 go-ocr 的识别结果组装为对前端友好的结构（含每行 box/score）。
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
