package handler

import (
	"context"
	"fmt"
	tgbotapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/rroy233/StickerDownloader/config"
	"github.com/rroy233/StickerDownloader/db"
	"github.com/rroy233/StickerDownloader/languages"
	"github.com/rroy233/StickerDownloader/statistics"
	"github.com/rroy233/StickerDownloader/utils"
	"gopkg.in/rroy233/logger.v2"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const MB = 1 << 20
const Hour = int64(3600)

type downloadTask struct {
	finished      int32
	failed        int32
	total         int32
	folderName    string
	batchManager  *batchManager
	update        *tgbotapi.Update
	msgID         int
	uploadWg      sync.WaitGroup
}

type batchManager struct {
	sync.Mutex
	currentBatchSize  int64
	currentBatchFiles []string
	batchIndex        int
	maxBatchSize      int64
	uploadedParts     int
}

func DownloadStickerSetQuery(update tgbotapi.Update) {
	userInfo := utils.GetLogPrefixCallbackQuery(&update)

	if update.CallbackQuery.Message.ReplyToMessage == nil {
		logger.Error.Println(userInfo+"DownloadStickerSetQuery-failed to GetStickerSet:", "Msg deleted")
		utils.CallBackWithAlert(update.CallbackQuery.ID, languages.Get(&update).BotMsg.ErrFailedToDownload)
		return
	}

	stickerSet, err := utils.BotGetStickerSet(tgbotapi.GetStickerSetConfig{
		Name: update.CallbackQuery.Message.ReplyToMessage.Sticker.SetName,
	})
	if err != nil {
		logger.Error.Println(userInfo+"DownloadStickerSetQuery-failed to GetStickerSet:", err)
		utils.CallBackWithAlert(update.CallbackQuery.ID, languages.Get(&update).BotMsg.ErrFailedToDownload)
		return
	}

	if len(stickerSet.Stickers) > config.Get().General.MaxAmountPerReq {
		logger.Info.Println(userInfo + "DownloadStickerSetQuery- amount > max_amount_per_req")
		utils.CallBackWithAlert(update.CallbackQuery.ID, languages.Get(&update).BotMsg.ErrStickerSetAmountReachLimit)
		return
	}

	if time.Now().Unix()-int64(update.CallbackQuery.Message.Date) < 48*Hour {
		utils.DeleteMsg(update.CallbackQuery.Message.Chat.ID, update.CallbackQuery.Message.MessageID)
	} else {
		utils.EditMsgText(update.CallbackQuery.Message.Chat.ID, update.CallbackQuery.Message.MessageID, update.CallbackQuery.Message.Text)
	}

	utils.CallBack(update.CallbackQuery.ID, "ok")

	oMsg := tgbotapi.NewMessage(update.CallbackQuery.Message.Chat.ID, languages.Get(&update).BotMsg.Processing)
	oMsg.ReplyParameters.MessageID = update.CallbackQuery.Message.ReplyToMessage.MessageID
	msg, err := utils.BotSend(oMsg)
	if err != nil {
		logger.Error.Println(userInfo+"DownloadStickerSetQuery-failed to send <processing> msg:", err)
		utils.SendPlainText(&update, languages.Get(&update).BotMsg.ErrSysFailureOccurred)
		return
	}

	qItem, quit := enqueue(&update, &msg)
	if quit {
		return
	}

	folderPath := fmt.Sprintf("./storage/tmp/stickers_%d", time.Now().UnixMicro())
	err = os.Mkdir(folderPath, 0777)
	if err != nil || !utils.IsExist(folderPath) {
		logger.Error.Println(userInfo+"DownloadStickerSetQuery-create folder failed:", err)
		utils.EditMsgText(update.CallbackQuery.Message.Chat.ID, msg.MessageID, languages.Get(&update).BotMsg.ErrFailed+"-1001")
		return
	}
	defer func() {
		err = os.RemoveAll(folderPath)
		if err != nil {
			logger.Error.Println(userInfo+"DownloadStickerSetQuery-delete temp folder failed:", folderPath, err)
		}
	}()

	cancelCtx, cancel := context.WithCancel(context.Background())
	queue := make(chan tgbotapi.Sticker, 10)
	task := &downloadTask{
		total:        int32(len(stickerSet.Stickers)),
		folderName:   folderPath,
		batchManager: newBatchManager(),
		update:       &update,
		msgID:        msg.MessageID,
	}
	for i := 0; i < config.Get().General.DownloadWorkerNum; i++ {
		go downloadWorker(cancelCtx, queue, task)
	}
	timeStart := time.Now()
	go func() {
		for _, sticker := range stickerSet.Stickers {
			queue <- sticker
		}
	}()

	var progressWg sync.WaitGroup
	progressWg.Add(1)
	go func() {
		defer progressWg.Done()
		text := ""
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-cancelCtx.Done():
				return
			case <-ticker.C:
				newText := fmt.Sprintf(languages.Get(&update).BotMsg.DownloadingWithProgress, task.finished+task.failed, task.total)
				if text != newText {
					utils.EditMsgText(update.CallbackQuery.Message.Chat.ID, msg.MessageID, newText)
					text = newText
				}
			}
		}
	}()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	success := true
loop:
	for {
		select {
		case <-ticker.C:
			if int(time.Now().Sub(timeStart).Seconds()) > ProcessTimeout {
				success = false
				logger.Error.Println(userInfo+"DownloadStickerSetQuery-Task Timeout:", task)
				break loop
			}
			if task.finished+task.failed == task.total {
				break loop
			}
		}
	}
	cancel()
	task.uploadWg.Wait()
	progressWg.Wait()

	if !success {
		utils.EditMsgText(update.CallbackQuery.Message.Chat.ID, msg.MessageID, fmt.Sprintf(languages.Get(&update).BotMsg.ErrTimeout))
		return
	}

	dequeue(qItem)

	task.batchManager.Lock()
	finalFiles := make([]string, len(task.batchManager.currentBatchFiles))
	copy(finalFiles, task.batchManager.currentBatchFiles)
	finalIndex := task.batchManager.batchIndex
	task.batchManager.currentBatchFiles = nil
	task.batchManager.currentBatchSize = 0
	task.batchManager.Unlock()

	if len(finalFiles) > 0 {
		if task.batchManager.uploadBatchFiles(task, finalFiles, finalIndex) {
			task.batchManager.Lock()
			task.batchManager.uploadedParts++
			task.batchManager.Unlock()
		}
	}

	text := fmt.Sprintf("上传成功！！\n表情包名:%s\n已上传 %d 个文件包", stickerSet.Name, task.batchManager.uploadedParts)
	utils.EditMsgText(update.CallbackQuery.Message.Chat.ID, msg.MessageID, text, utils.EntityBold(text, stickerSet.Name))
	logger.Info.Printf("%sDownloadStickerSetQuery-streaming upload completed successfully (%d parts)", userInfo, task.batchManager.uploadedParts)

	if err = db.ConsumeLimit(&update); err != nil {
		logger.Error.Println(userInfo + "DownloadStickerSetQuery - " + err.Error())
	}
}

func downloadWorker(ctx context.Context, queue chan tgbotapi.Sticker, task *downloadTask) {
	var sticker tgbotapi.Sticker
	for {
		select {
		case <-ctx.Done():
			return
		case sticker = <-queue:
			i := task.finished + task.failed
			sum := task.total
			stickerInfo := utils.JsonEncode(sticker)
			var outputFilePath string
			var fileExt string

			cacheTmpFile, err := db.FindStickerCache(sticker.FileUniqueID)
			if err == nil {
				statistics.Statistics.Record("CacheHit", 1)
				fileExt = utils.GetFileExtName(cacheTmpFile)
				outputFilePath = fmt.Sprintf("%s/%s.%s", task.folderName, sticker.FileUniqueID, fileExt)
				err := utils.CopyFile(cacheTmpFile, outputFilePath)
				utils.RemoveFile(cacheTmpFile)
				if err != nil {
					logger.Error.Printf("DownloadStickerSetQuery[%d/%d]-failed to copy：%s,%s", i, sum, err.Error(), stickerInfo)
					atomic.AddInt32(&task.failed, 1)
					continue
				}
			} else {
				statistics.Statistics.Record("CacheMiss", 1)
				remoteFile, err := utils.BotGetFile(tgbotapi.FileConfig{
					FileID: sticker.FileID,
				})
				if err != nil {
					logger.Error.Printf("DownloadStickerSetQuery[%d/%d]-failed to get file:%s,%s", i, sum, err.Error(), stickerInfo)
					atomic.AddInt32(&task.failed, 1)
					continue
				}

				tempFilePath, err := utils.DownloadFile(remoteFile.Link(config.Get().General.BotToken))
				if err != nil {
					logger.Error.Printf("DownloadStickerSetQuery[%d/%d]-failed to download:%s,%s", i, sum, err.Error(), stickerInfo)
					atomic.AddInt32(&task.failed, 1)
					continue
				}

				fileExt = "gif"
				if utils.GetFileExtName(tempFilePath) == "webp" {
					fileExt = "png"
				}

				outputFilePath = fmt.Sprintf("%s/%s.%s", task.folderName, sticker.FileUniqueID, fileExt)

				convertTask := utils.ConvertTask{
					InputFilePath:  tempFilePath,
					InputExtension: utils.GetFileExtName(tempFilePath),
					OutputFilePath: outputFilePath,
				}

				if utils.GetFileExtName(tempFilePath) == "tgs" && config.Get().General.SupportTGSFile {
					convertTask.PreserveJsonPath = fmt.Sprintf("%s/%s.json", task.folderName, sticker.FileUniqueID)
				}

				err = convertTask.Run(ctx)
				utils.RemoveFile(tempFilePath)
				if err != nil {
					logger.Error.Printf("DownloadStickerSetQuery[%d/%d]-failed to convert：%s,%s\n", i, sum, err.Error(), stickerInfo)
					atomic.AddInt32(&task.failed, 1)
					continue
				}
				if config.Get().Cache.Enabled == true {
					if _, err := db.CacheSticker(sticker, convertTask.OutputFilePath); err != nil {
						logger.Error.Printf("DownloadStickerSetQuery[%d/%d]-failed to Save Cache:%s,%s", i, sum, err.Error(), stickerInfo)
					}
				}
			}

			if task.batchManager != nil {
				if fileInfo, err := os.Stat(outputFilePath); err == nil {
					for {
						if !task.batchManager.addFileAndCheckAtomic(outputFilePath, fileInfo.Size()) {
							break
						}
						task.uploadWg.Add(1)
						task.batchManager.uploadBatch(task)
						task.uploadWg.Done()
					}
				}
			}

			atomic.AddInt32(&task.finished, 1)
		}
	}
}

func newBatchManager() *batchManager {
	return &batchManager{
		currentBatchFiles: []string{},
		maxBatchSize:      50 * MB,
	}
}

func (bm *batchManager) addFileAndCheckAtomic(filePath string, size int64) bool {
	bm.Lock()
	defer bm.Unlock()
	willExceed := (bm.currentBatchSize + size) >= bm.maxBatchSize
	isNonEmpty := len(bm.currentBatchFiles) > 0

	if willExceed && isNonEmpty {
		return true
	}

	bm.currentBatchSize += size
	bm.currentBatchFiles = append(bm.currentBatchFiles, filePath)
	return false
}


func (bm *batchManager) uploadBatch(task *downloadTask) {
	bm.Lock()
	filesToUpload := make([]string, len(bm.currentBatchFiles))
	copy(filesToUpload, bm.currentBatchFiles)
	bm.currentBatchFiles = []string{}
	bm.currentBatchSize = 0
	currentIndex := bm.batchIndex
	bm.batchIndex++
	bm.Unlock()

	if bm.uploadBatchFiles(task, filesToUpload, currentIndex) {
		bm.Lock()
		bm.uploadedParts++
		bm.Unlock()
		utils.EditMsgText(task.update.CallbackQuery.Message.Chat.ID, task.msgID,
			fmt.Sprintf(languages.Get(task.update).BotMsg.DownloadingWithProgress, task.finished+task.failed, task.total))
	}
}

func (bm *batchManager) uploadBatchFiles(task *downloadTask, filePaths []string, batchIndex int) bool {
	userInfo := utils.GetLogPrefixCallbackQuery(task.update)
	batchFolder := fmt.Sprintf("%s_batch_%d", task.folderName, batchIndex)

	if err := os.Mkdir(batchFolder, 0777); err != nil {
		logger.Error.Printf("%sFailed to create batch folder: %v", userInfo, err)
		return false
	}
	defer os.RemoveAll(batchFolder)

	actualSize := int64(0)
	for _, srcPath := range filePaths {
		fileInfo, err := os.Stat(srcPath)
		if err != nil {
			continue
		}
		dstPath := filepath.Join(batchFolder, filepath.Base(srcPath))
		if err = os.Rename(srcPath, dstPath); err != nil {
			if copyErr := utils.CopyFile(srcPath, dstPath); copyErr != nil {
				logger.Error.Printf("%sFailed to move file to batch: %v", userInfo, copyErr)
				continue
			}
			os.Remove(srcPath)
		}
		actualSize += fileInfo.Size()
	}

	if len(filePaths) == 0 || actualSize == 0 {
		logger.Info.Printf("%sBatch %d has no files to upload, skipping.", userInfo, batchIndex)
		return false
	}
	logger.Info.Printf("%sUploading batch %d (%.2f MB, %d files)", userInfo, batchIndex, float64(actualSize)/(1024*1024), len(filePaths))

	zipFilePath := fmt.Sprintf("%s_part-%d.zip", task.folderName, batchIndex)
	if err := utils.Compress(batchFolder, zipFilePath); err != nil {
		logger.Error.Printf("%sFailed to compress batch: %v", userInfo, err)
		return false
	}
	defer utils.RemoveFile(zipFilePath)

	utils.SendAction(task.update.CallbackQuery.Message.Chat.ID, utils.ChatActionSendDocument)
	if _, err := utils.SendFileByPath(task.update, zipFilePath); err != nil {
		logger.Error.Printf("%sFailed to upload batch: %v", userInfo, err)
		return false
	}

	logger.Info.Printf("%sBatch %d uploaded successfully", userInfo, batchIndex)
	return true
}
