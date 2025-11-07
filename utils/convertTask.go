package utils

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"github.com/rroy233/StickerDownloader/config"
	"gopkg.in/rroy233/logger.v2"
	"image"
	"image/png"
	"os"
	"os/exec"
	"runtime"
)

type ConvertTask struct {
	InputFilePath    string
	InputExtension   string
	OutputFilePath   string
	PreserveJsonPath string
}

const paletteFilter = "split[s0][s1];[s0]fps=5,palettegen=reserve_transparent=1[p];[s1][p]paletteuse=dither=none"

func (task *ConvertTask) Run(ctx context.Context) error {
	var cmd *exec.Cmd
	if task.InputExtension == "tgs" {
		if !config.Get().General.SupportTGSFile {
			return errors.New("SupportTGSFile is disabled")
		}
		if err := task.tgsDecode(); err != nil {
			return err
		}
		if err := os.Rename(task.InputFilePath, task.InputFilePath+".json"); err != nil {
			return err
		}
		task.InputFilePath += ".json"

		if task.PreserveJsonPath != "" {
			if err := CopyFile(task.InputFilePath, task.PreserveJsonPath); err != nil {
				logger.Warn.Printf("failed to preserve JSON to %s: %v", task.PreserveJsonPath, err)
			}
		}

		cmd = exec.CommandContext(ctx, rlottieExcutablePath, task.InputFilePath, "512x512")
		defer os.Remove(task.InputFilePath)
	} else {
		args := []string{"-y"}
		vfilter := "fps=fps='min(source_fps,40)'"
		if task.InputExtension == "webm" {
			args = append(args, "-vcodec", "libvpx-vp9")
			if task.detectWebmAlpha(ctx) {
				vfilter += "," + paletteFilter
			}
		}
		args = append(args, "-i", task.InputFilePath, "-vf", vfilter, task.OutputFilePath)
		cmd = exec.CommandContext(ctx, ffmpegExecutablePath, args...)
	}

	if err := cmd.Run(); err != nil {
		return err
	}

	if task.InputExtension == "tgs" {
		return os.Rename(task.InputFilePath+".gif", task.OutputFilePath)
	}
	if task.InputExtension == "webp" {
		if err := trimTransparentEdges(task.OutputFilePath); err != nil {
			logger.Warn.Printf("failed to trim transparent edges: %v", err)
		}
	}
	return nil
}

func getRlottieFilename() string {
	if runtime.GOOS == "windows" {
		return "lottie2gif.exe"
	}
	return "lottie2gif"
}

func getFfmpegFilename(simplify bool) string {
	name := "ffmpeg"
	if !simplify {
		name += fmt.Sprintf("-%s-%s", runtime.GOOS, runtime.GOARCH)
	}
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func (task *ConvertTask) tgsDecode() error {
	file, err := os.OpenFile(task.InputFilePath, os.O_RDWR, 0755)
	if err != nil {
		return err
	}
	defer file.Close()

	r, err := gzip.NewReader(file)
	if err != nil {
		return err
	}

	var buff bytes.Buffer
	if _, err = buff.ReadFrom(r); err != nil {
		return err
	}

	file.Truncate(0)
	file.Seek(0, 0)
	_, err = file.Write(buff.Bytes())
	return err
}

func TgsToJson(tgsFilePath, jsonOutputPath string) error {
	tgsFile, err := os.Open(tgsFilePath)
	if err != nil {
		return err
	}
	defer tgsFile.Close()

	r, err := gzip.NewReader(tgsFile)
	if err != nil {
		return err
	}
	defer r.Close()

	var buff bytes.Buffer
	if _, err = buff.ReadFrom(r); err != nil {
		return err
	}

	return os.WriteFile(jsonOutputPath, buff.Bytes(), 0644)
}

func (task *ConvertTask) detectWebmAlpha(ctx context.Context) bool {
	tempPNG := task.InputFilePath + "_alpha_check.png"
	defer os.Remove(tempPNG)

	cmd := exec.CommandContext(ctx, ffmpegExecutablePath,
		"-vcodec", "libvpx-vp9", "-i", task.InputFilePath,
		"-frames:v", "1", "-compression_level", "0", "-y", tempPNG)

	if cmd.Run() != nil {
		return true
	}

	file, err := os.Open(tempPNG)
	if err != nil {
		return true
	}
	defer file.Close()

	img, err := png.Decode(file)
	if err != nil {
		return true
	}

	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	hasAlpha := func(x, y int) bool {
		_, _, _, a := img.At(x, y).RGBA()
		return a < 65535
	}

	for x := 0; x < width; x += 8 {
		if hasAlpha(x, 0) || hasAlpha(x, height-1) {
			return true
		}
	}

	for y := 0; y < height; y += 8 {
		if hasAlpha(0, y) || hasAlpha(width-1, y) {
			return true
		}
	}

	for y := 16; y < height-16; y += 32 {
		for x := 16; x < width-16; x += 32 {
			if hasAlpha(x, y) {
				return true
			}
		}
	}

	return false
}

func trimTransparentEdges(imagePath string) error {
	file, err := os.Open(imagePath)
	if err != nil {
		return err
	}
	defer file.Close()

	img, err := png.Decode(file)
	if err != nil {
		return err
	}

	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	minX, maxX, minY, maxY := width, 0, height, 0

	isOpaque := func(x, y int) bool {
		_, _, _, a := img.At(x, y).RGBA()
		return a > 0
	}

	for y := 0; y < height && minY >= height; y++ {
		for x := 0; x < width; x++ {
			if isOpaque(x, y) {
				minY = y
				break
			}
		}
	}

	for y := height - 1; y >= minY && maxY == 0; y-- {
		for x := 0; x < width; x++ {
			if isOpaque(x, y) {
				maxY = y
				break
			}
		}
	}

	for x := 0; x < width && minX >= width; x++ {
		for y := minY; y <= maxY; y++ {
			if isOpaque(x, y) {
				minX = x
				break
			}
		}
	}

	for x := width - 1; x >= minX && maxX == 0; x-- {
		for y := minY; y <= maxY; y++ {
			if isOpaque(x, y) {
				maxX = x
				break
			}
		}
	}

	if minX >= maxX || minY >= maxY || (minX == 0 && minY == 0 && maxX == width-1 && maxY == height-1) {
		return nil
	}

	croppedImg := image.NewNRGBA(image.Rect(0, 0, maxX-minX+1, maxY-minY+1))
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			croppedImg.Set(x-minX, y-minY, img.At(x, y))
		}
	}

	outFile, err := os.Create(imagePath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	return png.Encode(outFile, croppedImg)
}
