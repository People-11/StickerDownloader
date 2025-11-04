package utils

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"github.com/rroy233/StickerDownloader/config"
	"gopkg.in/rroy233/logger.v2"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

type ConvertTask struct {
	//Input file for converting
	InputFilePath string

	//Input file extension
	//support: webp,webm,tgs
	InputExtension string

	//Output file for converting
	OutputFilePath string

	//Optional: For TGS files, preserve the decompressed JSON to this path
	PreserveJsonPath string
}

func (task *ConvertTask) Run(ctx context.Context) error {
	var cmd *exec.Cmd
	if task.InputExtension == "tgs" {
		if config.Get().General.SupportTGSFile == false {
			return errors.New("SupportTGSFile is disabled")
		}
		//tgs gzip decode
		if err := task.tgsDecode(); err != nil {
			return err
		}
		//rename to xxx.tgs.json
		if err := os.Rename(task.InputFilePath, task.InputFilePath+".json"); err != nil {
			return err
		}
		task.InputFilePath = task.InputFilePath + ".json"

		// If PreserveJsonPath is set, copy the decompressed JSON before processing
		if task.PreserveJsonPath != "" {
			if err := CopyFile(task.InputFilePath, task.PreserveJsonPath); err != nil {
				logger.Warn.Printf("failed to preserve JSON to %s: %v", task.PreserveJsonPath, err)
			}
		}

		//handle it to rlottie
		cmd = exec.CommandContext(ctx, rlottieExcutablePath, strings.Split(fmt.Sprintf("%s 512x512", task.InputFilePath), " ")...)
		//remember to delete xxx.tgs.json
		defer func() {
			if err := os.Remove(task.InputFilePath); err != nil {
				logger.Warn.Println("failed to remove", task.InputFilePath)
			}
		}()
	} else {
		// 使用 fps 滤镜限制最大帧率为 40，低于 40 的保持原始帧率
		args := []string{
			"-y",
			"-i", task.InputFilePath,
			"-vf", "fps=fps='min(source_fps,40)',scale=-1:-1",
			task.OutputFilePath,
		}
		cmd = exec.CommandContext(ctx, ffmpegExecutablePath, args...)
	}

	//cmd.Stderr = logWriter{}

	err := cmd.Run()
	if err != nil {
		return err
	}

	//postprocessing
	if task.InputExtension == "tgs" {
		//mv to OutputFilePath
		err = os.Rename(task.InputFilePath+".gif", task.OutputFilePath)
		if err != nil {
			return err
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
