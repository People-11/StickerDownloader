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
	"strings"
)

type ConvertTask struct {
	//Input file for converting
	InputFilePath string

	//Input file extension
	//support: webp,webm,mp4,tgs
	InputExtension string

	//Output file for converting
	OutputFilePath string

	//Optional: For TGS files, preserve the decompressed JSON to this path
	PreserveJsonPath string
}

const (
	// Palette filter for GIF transparency: split stream, generate palette with transparency slot, apply palette
	paletteFilter = "split[s0][s1];[s0]palettegen=reserve_transparent=1[p];[s1][p]paletteuse"
)

func (task *ConvertTask) Run(ctx context.Context) error {
	var cmd *exec.Cmd
	if task.InputExtension == "tgs" {
		if config.Get().General.SupportTGSFile == false {
			return errors.New("SupportTGSFile is disabled")
		}
		if err := task.tgsDecode(); err != nil {
			return err
		}
		if err := os.Rename(task.InputFilePath, task.InputFilePath+".json"); err != nil {
			return err
		}
		task.InputFilePath = task.InputFilePath + ".json"

		if task.PreserveJsonPath != "" {
			if err := CopyFile(task.InputFilePath, task.PreserveJsonPath); err != nil {
				logger.Warn.Printf("failed to preserve JSON to %s: %v", task.PreserveJsonPath, err)
			}
		}

		cmd = exec.CommandContext(ctx, rlottieExcutablePath, strings.Split(fmt.Sprintf("%s 512x512", task.InputFilePath), " ")...)
		defer func() {
			if err := os.Remove(task.InputFilePath); err != nil {
				logger.Warn.Println("failed to remove", task.InputFilePath)
			}
		}()
	} else {
		args := []string{"-y"}
		var vfilter string

		if task.InputExtension == "webm" {
			args = append(args, "-vcodec", "libvpx-vp9")
			// Detect transparency and choose appropriate filter
			if task.detectWebmAlpha(ctx) {
				vfilter = "fps=fps='min(source_fps,40)'," + paletteFilter
			} else {
				vfilter = "fps=fps='min(source_fps,40)'"
			}
		} else {
			vfilter = "fps=fps='min(source_fps,40)'"
		}

		args = append(args, "-i", task.InputFilePath,
			"-vf", vfilter,
			task.OutputFilePath)
		cmd = exec.CommandContext(ctx, ffmpegExecutablePath, args...)
	}

	//cmd.Stderr = logWriter{}
	err := cmd.Run()
	if err != nil {
		return err
	}

	if task.InputExtension == "tgs" {
		err = os.Rename(task.InputFilePath+".gif", task.OutputFilePath)
		if err != nil {
			return err
		}
	} else if task.InputExtension == "webp" {
		if err := trimTransparentEdges(task.OutputFilePath); err != nil {
			logger.Warn.Printf("failed to trim transparent edges: %v", err)
		}
	}

	return err
}

type logWriter struct{}

func (w logWriter) Write(p []byte) (n int, err error) {
	logger.Error.Println("[ConvertTask]" + string(p))
	return len(p), nil
}

func getRlottieFilename() string {
	exeSuffix := ""
	if runtime.GOOS == "windows" {
		exeSuffix = ".exe"
	}
	return fmt.Sprintf("lottie2gif" + exeSuffix)
}

func getFfmpegFilename(simplify bool) string {
	exeSuffix := ""

	if simplify == false {
		exeSuffix += fmt.Sprintf("-%s-%s", runtime.GOOS, runtime.GOARCH)
	}

	//windows
	if runtime.GOOS == "windows" {
		exeSuffix += ".exe"
	}

	return "ffmpeg" + exeSuffix
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

	buff := bytes.Buffer{}
	if _, err = buff.ReadFrom(r); err != nil {
		return err
	}

	_ = file.Truncate(0)
	file.Seek(0, 0)
	if _, err = file.Write(buff.Bytes()); err != nil {
		return err
	}

	return nil
}

// TgsToJson extracts the JSON content from a TGS file (gzip-compressed JSON)
// and saves it to the specified output path
func TgsToJson(tgsFilePath, jsonOutputPath string) error {
	// Open TGS file for reading
	tgsFile, err := os.Open(tgsFilePath)
	if err != nil {
		return err
	}
	defer tgsFile.Close()

	// Decompress gzip
	r, err := gzip.NewReader(tgsFile)
	if err != nil {
		return err
	}
	defer r.Close()

	// Read decompressed JSON content
	buff := bytes.Buffer{}
	if _, err = buff.ReadFrom(r); err != nil {
		return err
	}

	// Write JSON to output file
	return os.WriteFile(jsonOutputPath, buff.Bytes(), 0644)
}

func (task *ConvertTask) detectWebmAlpha(ctx context.Context) bool {
	tempPNG := task.InputFilePath + "_alpha_check.png"
	defer os.Remove(tempPNG)

	cmd := exec.CommandContext(ctx, ffmpegExecutablePath,
		"-vcodec", "libvpx-vp9",
		"-i", task.InputFilePath,
		"-frames:v", "1",
		"-compression_level", "0",
		"-y", tempPNG)

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
	width := bounds.Dx()
	height := bounds.Dy()

	checkPixel := func(x, y int) bool {
		_, _, _, a := img.At(x, y).RGBA()
		return a < 65535
	}

	for x := 0; x < width; x += 8 {
		if checkPixel(x, 0) || checkPixel(x, height-1) {
			return true
		}
	}

	for y := 0; y < height; y += 8 {
		if checkPixel(0, y) || checkPixel(width-1, y) {
			return true
		}
	}

	for y := 16; y < height-16; y += 32 {
		for x := 16; x < width-16; x += 32 {
			if checkPixel(x, y) {
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
	width := bounds.Dx()
	height := bounds.Dy()
	minX, maxX := width, 0
	minY, maxY := height, 0

	isOpaque := func(x, y int) bool {
		_, _, _, a := img.At(x, y).RGBA()
		return a > 0
	}

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if isOpaque(x, y) {
				if y < minY {
					minY = y
				}
				break
			}
		}
		if minY < height {
			break
		}
	}

	for y := height - 1; y >= minY; y-- {
		for x := 0; x < width; x++ {
			if isOpaque(x, y) {
				maxY = y
				break
			}
		}
		if maxY > 0 {
			break
		}
	}

	for x := 0; x < width; x++ {
		for y := minY; y <= maxY; y++ {
			if isOpaque(x, y) {
				minX = x
				break
			}
		}
		if minX < width {
			break
		}
	}

	for x := width - 1; x >= minX; x-- {
		for y := minY; y <= maxY; y++ {
			if isOpaque(x, y) {
				maxX = x
				break
			}
		}
		if maxX > 0 {
			break
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
