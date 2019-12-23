package main

import (
	"archive/zip"
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/NightMan-1/go-dropbox"
	"github.com/NightMan-1/go-dropy"
	"github.com/djherbis/times"
	"github.com/dustin/go-humanize"
	lediscfg "github.com/siddontang/ledisdb/config"
	"github.com/siddontang/ledisdb/ledis"
)

type configGlobalStruct struct {
	Sources                 []string
	dbFile, archivePrefix   string
	dropboxToken            string
	timeStart               time.Time
	scheduleKeep            int64
	scheduleFullArchiveDays map[int64]int64
	execDir                 string
	backupType              string
}

var configGlobal (configGlobalStruct)
var dropboxConnection *dropy.Client
var fileListDB *ledis.DB

var zipFile *os.File
var zipArchive *zip.Writer
var zipSourceSize int64 = 2000000000 //2Gb
//var zipSourceSize int64 =  256000000 //256Mb
//var zipSourceSize int64 =  10000000 //10Mb
var sizeNewFiles, sizeUpdatedFiles, sizeUnchangedFiles, sizeCurrent int64
var zipArchivePart, zipArchiveSizeTotal int64

type FileInfoStruct struct {
	Name                               string
	Size, ModTime, ChangeTime, AddTime int64
}

var UnchangedFilesCount int64 = 0
var AllFilesCount int64 = 0
var NewFilesCount int64 = 0
var UpdatedFilesCount int64 = 0
var DeletedFilesCount int64 = 0

//return program head
func headText() string {
	hostname, _ := os.Hostname()
	startTime := fmt.Sprintf("%d-%02d-%02d %02d:%02d:%02d", configGlobal.timeStart.Year(), configGlobal.timeStart.Month(), configGlobal.timeStart.Day(), configGlobal.timeStart.Hour(), configGlobal.timeStart.Minute(), configGlobal.timeStart.Second())

	headText := "###############################################################################\n"
	headText += "GoBackup to Dropbox version 2.1\n"
	headText += "Server Name - " + hostname + "\n"
	if configGlobal.backupType != "" {
		headText += "Backup type: " + configGlobal.backupType + "\n"
	}
	headText += "Backup start at " + startTime + "\n"
	headText += "###############################################################################\n"

	return headText

}

//initialization
func initSystem() {

	//read configuration
	configDataRaw, err := ioutil.ReadFile("config.ini")
	if err != nil {
		ex, err := os.Executable()
		if err != nil {
			panic(err)
		}
		configGlobal.execDir = path.Dir(ex) + "/"
		configDataRaw, err = ioutil.ReadFile(configGlobal.execDir + "config.ini")
		check(err, "Can not read config.ini")
	} else {
		configFilePath, _ := filepath.Abs("config.ini")
		configGlobal.execDir = path.Dir(configFilePath) + "/"

	}
	configDataStr := string(configDataRaw)
	configDataArray := strings.Split(configDataStr, "\n")
	rS, _ := regexp.Compile(`\[sources\]`)
	rE, _ := regexp.Compile(`\[`)
	rComment, _ := regexp.Compile(`#`)
	rarchivePrefix, _ := regexp.Compile(`.*archive_prefix.*=\W*(.+)$`)
	rdropboxToken, _ := regexp.Compile(`.*dropbox_token.*=\W*(.+)$`)

	rdScheduleFullArchiveDays, _ := regexp.Compile(`.*full_archive.*=\W*(.+)$`)
	rdScheduleKeepDays, _ := regexp.Compile(`.*keep_days.*=\W*(.+)$`)

	configGlobal.scheduleFullArchiveDays = make(map[int64]int64)

	sourcesCheck := false
	for i := range configDataArray {
		str := strings.TrimSpace(configDataArray[i])
		//skip comments
		if rComment.MatchString(str) {
			continue
		}

		//find the beginning of the file list
		if rS.MatchString(str) {
			sourcesCheck = true
			continue
		}
		if sourcesCheck && !rE.MatchString(str) && len(str) > 0 {
			configGlobal.Sources = append(configGlobal.Sources, str)
			//turn off at the end of the list
		} else {
			sourcesCheck = false
		}

		checkArchivePrefix := rarchivePrefix.FindStringSubmatch(str)
		if len(checkArchivePrefix) == 2 {
			configGlobal.archivePrefix = checkArchivePrefix[1]
		}

		//dropbox
		checkDropboxToken := rdropboxToken.FindStringSubmatch(str)
		if len(checkDropboxToken) == 2 {
			configGlobal.dropboxToken = checkDropboxToken[1]
		}

		//schedule
		checkScheduleFullArchiveDays := rdScheduleFullArchiveDays.FindStringSubmatch(str)
		if len(checkScheduleFullArchiveDays) == 2 {
			//try split multiple days
			days := strings.Split(checkScheduleFullArchiveDays[1], ",")
			if len(days) > 1 {
				for _, v := range days {
					i := StrToInit64(v)
					configGlobal.scheduleFullArchiveDays[i] = i
				}
			} else {
				i := StrToInit64(checkScheduleFullArchiveDays[1])
				configGlobal.scheduleFullArchiveDays[i] = i
			}

		}
		checkScheduleKeepDays := rdScheduleKeepDays.FindStringSubmatch(str)
		if len(checkScheduleKeepDays) == 2 {
			configGlobal.scheduleKeep = StrToInit64(checkScheduleKeepDays[1])
		}
	}

	if configGlobal.dropboxToken == "" {
		fmt.Println("Dropbox Token is not set in config.ini")
		os.Exit(1)
	}

	//read old file list
	if _, err := os.Stat(configGlobal.execDir + "data"); os.IsNotExist(err) {
		//first start
		configGlobal.backupType = "full"
		fmt.Print("processing full backup...")
	} else {
		//check command line parameters
		re := regexp.MustCompile(`full$`)
		if len(os.Args) > 1 && re.MatchString(os.Args[1]) {
			configGlobal.backupType = "full"
			fmt.Print("processing full backup...")
		} else if configGlobal.scheduleFullArchiveDays[int64(configGlobal.timeStart.Day())] == 0 {
			configGlobal.backupType = "incremental"
			fmt.Print("processing incremental backup...")
		} else {
			configGlobal.backupType = "full"
			fmt.Print("processing full backup...")
		}
	}

	//open database
	cfg := lediscfg.NewConfigDefault()
	cfg.DataDir, _ = filepath.Abs(configGlobal.execDir + "data")
	//fmt.Println(cfg.DataDir)
	l, _ := ledis.Open(cfg)
	fileListDB, err = l.Select(0)
	if err != nil {
		os.Remove(cfg.DataDir)
		fmt.Println("Error open data file")
		panic(err)
	}

	if configGlobal.backupType == "full" {
		fileListDB.FlushAll()
		l.CompactStore()
	}

	//connect to Dropbox
	dropboxConnection = dropy.New(dropbox.New(dropbox.NewConfig(configGlobal.dropboxToken)))

}

func GetMD5Hash(text string) string {
	hasher := md5.New()
	hasher.Write([]byte(text))
	return hex.EncodeToString(hasher.Sum(nil))
}

func check(e error, message string) {
	if e != nil {
		fmt.Println(message)
		panic(e)
	}
}

func StrToInit64(s string) int64 {
	RI, _ := strconv.ParseInt(strings.Trim(s, " "), 10, 64)
	return RI
}

func DropboxCLean() {
	fmt.Println("Check old archives...")

	files, err := dropboxConnection.ListFiles("/")
	check(err, "Error 5342")

	archiveKeepDate := configGlobal.timeStart.AddDate(0, 0, -int(configGlobal.scheduleKeep)).Unix()

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		rFileTime, _ := regexp.Compile(`^` + configGlobal.archivePrefix + `_(\d\d\d\d_\d\d_\d\d_\d\d-\d\d-\d\d).+?`)
		FileTimeArray := rFileTime.FindStringSubmatch(file.Name())
		if len(FileTimeArray) == 2 {
			archiveDate, err := time.Parse("2006_01_02_15-04-05", FileTimeArray[1])
			if err != nil {
				continue
			}
			if archiveDate.Unix() < archiveKeepDate {
				fmt.Println("\t deleting /" + file.Name())
				dropboxConnection.Delete("/" + file.Name())
			}
		}
	}

}

func SecToTime(seconds int64) string {
	minutes := seconds / 60
	hours := minutes / 60
	m_diff := minutes - (hours * 60)
	s_diff := seconds - (minutes * 60)

	return fmt.Sprintf("%02d:%02d:%02d", hours, m_diff, s_diff)
}

func addSymLinks(simlinksList []string, zipArchive *zip.Writer) {
	simlinksExecutable := "simlinks" + fmt.Sprintf("_%d_%02d_%02d_%02d-%02d-%02d", configGlobal.timeStart.Year(), configGlobal.timeStart.Month(), configGlobal.timeStart.Day(), configGlobal.timeStart.Hour(), configGlobal.timeStart.Minute(), configGlobal.timeStart.Second()) + ".sh"
	simlinksExecutableFileName := configGlobal.execDir + simlinksExecutable
	simlinksExecutableFile, err := os.Create(simlinksExecutableFileName)
	simlinksExecutableFileBuffer := bufio.NewWriter(simlinksExecutableFile)
	check(err, "Can not create simlinks restore file")
	fmt.Fprint(simlinksExecutableFileBuffer, "#!/bin/sh\n\n")
	for _, element := range simlinksList {
		pathLink, _ := os.Readlink(element)
		fmt.Fprintln(simlinksExecutableFileBuffer, "ln -s \""+pathLink+"\" \""+element+"\"")
	}
	simlinksExecutableFileBuffer.Flush()
	simlinksExecutableFile.Close()
	info, _ := os.Stat(simlinksExecutableFileName)

	header, _ := zip.FileInfoHeader(info)
	header.Name = "/" + simlinksExecutable
	header.Method = zip.Deflate
	writer, _ := zipArchive.CreateHeader(header)
	file, _ := os.Open(configGlobal.execDir + simlinksExecutable)
	_, _ = io.CopyN(writer, file, info.Size())
	file.Close()
	os.Remove(simlinksExecutableFileName)
}

func addToArchive(path string, zipArchive *zip.Writer, info os.FileInfo) error {
	header, err := zip.FileInfoHeader(info)
	check(err, "error getting header "+path)
	header.Name = path

	if info.IsDir() {
		header.Name += "/"
		header.Method = zip.Store
	} else {
		header.Method = zip.Deflate
	}

	writer, err := zipArchive.CreateHeader(header)
	check(err, "Error 847")

	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%s: opening: %v", path, err)
	}
	_, err = io.CopyN(writer, file, info.Size())
	if err != nil && err != io.EOF {
		return fmt.Errorf("%s: copying contents: %v", path, err)
	}
	file.Close()
	return nil

}

func archiveUpload(archiveFile string) error {
	archiveFileName := "/" + path.Base(archiveFile)

	zipfile, err := os.Open(archiveFile)
	check(err, "Error: can not open "+archiveFile)

	info, _ := zipfile.Stat()
	zipArchiveSizeTotal += info.Size()

	err = dropboxConnection.UploadSession(archiveFileName, zipfile)
	zipfile.Close()
	return err

}

func checkFile(file FileInfoStruct) int {
	var MD5FileName string = GetMD5Hash(file.Name)
	var fileCheck FileInfoStruct
	//0 = error, 1 = new, 2 = updated, 3 = not modified
	fileType := 0

	v, _ := fileListDB.Get([]byte(MD5FileName))
	if len(v) > 0 {
		err := json.Unmarshal(v, &fileCheck)
		if err != nil {
			return 0
		}
		//check file type
		if fileCheck.Name == file.Name && fileCheck.Size == file.Size && fileCheck.ChangeTime == file.ChangeTime && fileCheck.ModTime == file.ModTime {
			//not modified
			fileType = 3
		} else {
			//updated file
			fileType = 2
		}
	} else {
		//new file
		fileType = 1
	}
	enc, err := json.Marshal(file)
	check(err, "Something wrong:")

	err = fileListDB.Set([]byte(MD5FileName), enc)
	check(err, "Something wrong:")

	return fileType
}

func debugInfo() {
	fmt.Printf("\n%s\n", SecToTime(int64(time.Now().Sub(configGlobal.timeStart).Seconds())))
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Printf("\nAlloc = %v\nTotalAlloc = %v\nSys = %v\nNumGC = %v\n\n", m.Alloc/1024, m.TotalAlloc/1024, m.Sys/1024, m.NumGC)
}

func main() {
	//TODO debug
	defer os.Exit(0)
	//defer debugInfo()

	configGlobal.timeStart = time.Now()
	fmt.Print(headText())
	fmt.Print("Init system...")
	initSystem()

	//temp files for new/updated files list
	tmpNewFiles, err := ioutil.TempFile("", "tmp")
	check(err, "Something wrong:")
	defer os.Remove(tmpNewFiles.Name()) // clean up
	tmpUpdateFiles, err := ioutil.TempFile("", "tmp")
	check(err, "Something wrong:")
	defer os.Remove(tmpUpdateFiles.Name()) // clean up
	tmpDeletedFiles, err := ioutil.TempFile("", "tmp")
	check(err, "Something wrong:")
	defer os.Remove(tmpDeletedFiles.Name()) // clean up

	fmt.Print("\n")

	fmt.Print("Archive files...\n")

	archiveName := configGlobal.archivePrefix + fmt.Sprintf("_%d_%02d_%02d_%02d-%02d-%02d", configGlobal.timeStart.Year(), configGlobal.timeStart.Month(), configGlobal.timeStart.Day(), configGlobal.timeStart.Hour(), configGlobal.timeStart.Minute(), configGlobal.timeStart.Second())

	var fileName string = ""
	var simlinksList []string
	for _, folder := range configGlobal.Sources {
		if _, err := os.Stat(folder); err == nil {
			fmt.Println("\tprocessing " + folder)
			filepath.Walk(folder, func(path string, info os.FileInfo, err error) error {
				//new archive
				if zipFile == nil {
					zipArchivePart = 1
					fileName = archiveName + fmt.Sprintf("_part%d", zipArchivePart) + ".zip"
					fmt.Println("\tcreate " + fileName)
					zipFile, err = os.Create(configGlobal.execDir + fileName)
					check(err, "Error 845")
					zipArchive = zip.NewWriter(zipFile)
					//new PART of archive
				} else if zipFile != nil && (sizeCurrent > zipSourceSize || (sizeCurrent+info.Size() > zipSourceSize && sizeCurrent/2 > zipSourceSize)) {
					if len(simlinksList) > 0 {
						addSymLinks(simlinksList, zipArchive)
					}
					zipArchive.Close()
					zipInfo, _ := zipFile.Stat()
					zipFile.Close()
					//upload to dropbox
					fmt.Printf("\tupload %s (%s)\n", fileName, humanize.Bytes(uint64(zipInfo.Size())))
					err = archiveUpload(configGlobal.execDir + fileName)
					check(err, "Upload error")
					os.Remove(configGlobal.execDir + fileName)
					simlinksList = nil

					zipArchivePart += 1
					fileName = archiveName + fmt.Sprintf("_part%d", zipArchivePart) + ".zip"
					fmt.Println("\tcreate " + fileName)
					zipFile, err = os.Create(configGlobal.execDir + fileName)
					check(err, "Error 846")
					zipArchive = zip.NewWriter(zipFile)
				}

				//не архивировать свои текущие архивы
				r, _ := regexp.Compile(filepath.Clean(zipFile.Name()) + "$")
				if len(r.FindStringSubmatch(filepath.Clean(path))) > 0 {
					return nil
				}

				if info.IsDir() {
					return nil
				}

				if info.Mode().IsRegular() {
					//var MD5FileName string = GetMD5Hash(path)
					var ctime int64 = 0
					fi, _ := times.Stat(path)
					ctime = fi.ChangeTime().Unix()

					currentFile := FileInfoStruct{path, info.Size(), info.ModTime().Unix(), ctime, configGlobal.timeStart.Unix()}

					//0 = error, 1 = new, 2 = updated, 3 = not modified
					fileType := checkFile(currentFile)
					switch fileType {
					case 3:
						//unchanged files
						sizeUnchangedFiles += info.Size()
						UnchangedFilesCount += 1
					case 2:
						//updated files
						err = addToArchive(path, zipArchive, info)
						if err != nil {
							fmt.Println(err)
						}
						sizeUpdatedFiles += info.Size()
						fmt.Fprintf(tmpUpdateFiles, "\t(archive %d) %s\n", zipArchivePart, path)
						UpdatedFilesCount += 1

					case 1:
						//new files
						err = addToArchive(path, zipArchive, info)
						if err != nil {
							fmt.Println(err)
						}
						sizeNewFiles += info.Size()
						fmt.Fprintf(tmpNewFiles, "\t(archive %d) %s\n", zipArchivePart, path)
						NewFilesCount += 1

					default:
						fmt.Println("File type error: ", currentFile.Name)
					}
					AllFilesCount += 1

				} else if (info.Mode() & os.ModeType) == os.ModeSymlink {
					simlinksList = append(simlinksList, path)
				}

				zipInfo, _ := zipFile.Stat()
				sizeCurrent = zipInfo.Size()

				return nil
			})
		}
	}

	//find deleted files
	cursor := []byte(nil)
	for {
		allDBData, err := fileListDB.Scan(ledis.KV, cursor, 0, false, "")
		if err != nil || len(allDBData) == 0 {
			break
		}
		for _, elementID := range allDBData {
			cursor = elementID
			data, _ := fileListDB.Get(elementID)
			var fileData FileInfoStruct
			err := json.Unmarshal(data, &fileData)
			check(err, "Something wrong:")
			if fileData.AddTime != configGlobal.timeStart.Unix() {
				DeletedFilesCount += 1
				fmt.Fprintf(tmpDeletedFiles, "\t%s\n", fileData.Name)
				fileListDB.Del(elementID)
			}
		}
	}

	if len(simlinksList) > 0 {
		addSymLinks(simlinksList, zipArchive)
	}

	if zipFile != nil {
		zipArchive.Close()
		zipInfo, _ := zipFile.Stat()
		zipFile.Close()

		//nothing found
		totalArchivedFiles := NewFilesCount + UpdatedFilesCount
		if totalArchivedFiles == 0 {
			os.Remove(configGlobal.execDir + fileName)
			fmt.Println("New/updated files not found.")
			fmt.Printf("\nAll work done! (take %s)\n", SecToTime(int64(time.Now().Sub(configGlobal.timeStart).Seconds())))
			runtime.Goexit()

		}

		//upload to dropbox
		fmt.Printf("\tupload %s (%s)\n", fileName, humanize.Bytes(uint64(zipInfo.Size())))
		err = archiveUpload(configGlobal.execDir + fileName)
		check(err, "Upload error")
		os.Remove(configGlobal.execDir + fileName)
	}

	reportFileName := archiveName + ".txt"
	logfile, err := os.Create(configGlobal.execDir + reportFileName)
	logBuffer := bufio.NewWriter(logfile)
	check(err, "Can not create log file")

	//prepare log file
	fmt.Fprintf(logBuffer, "%s", headText())
	fmt.Fprint(logBuffer, "Statistic:\n")
	fmt.Fprintf(logBuffer, "\t found %d new files (%s)\n", NewFilesCount, humanize.Bytes(uint64(sizeNewFiles)))
	fmt.Fprintf(logBuffer, "\t found %d updated files (%s)\n", UpdatedFilesCount, humanize.Bytes(uint64(sizeUpdatedFiles)))
	fmt.Fprintf(logBuffer, "\t found %d deleted files\n", DeletedFilesCount)
	fmt.Fprintf(logBuffer, "\t found %d unchanged files (%s)\n", UnchangedFilesCount, humanize.Bytes(uint64(sizeUnchangedFiles)))
	fmt.Fprintf(logBuffer, "\t total %d files found (%s)\n", AllFilesCount, humanize.Bytes(uint64(sizeNewFiles+sizeUnchangedFiles+sizeUpdatedFiles)))
	fmt.Fprintf(logBuffer, "\t %d archives created (%s)\n", zipArchivePart, humanize.Bytes(uint64(zipArchiveSizeTotal)))
	fmt.Fprint(logBuffer, "===============================================================================\n")
	if NewFilesCount > 0 {
		fmt.Fprintf(logBuffer, "New files (%d):\n", NewFilesCount)
		tmpNewFiles.Sync()
		file, err := os.OpenFile(tmpNewFiles.Name(), os.O_RDWR, 0644)
		check(err, "Something wrong:")
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fmt.Fprintln(logBuffer, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			check(err, "Something wrong:")
		}
		file.Close()

		fmt.Fprint(logBuffer, "===============================================================================\n")
	}
	if UpdatedFilesCount > 0 {
		fmt.Fprintf(logBuffer, "Updated files (%d):\n", UpdatedFilesCount)
		tmpUpdateFiles.Sync()
		file, err := os.OpenFile(tmpUpdateFiles.Name(), os.O_RDWR, 0644)
		check(err, "Something wrong:")
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fmt.Fprintln(logBuffer, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			check(err, "Something wrong:")
		}
		file.Close()
		fmt.Fprint(logBuffer, "===============================================================================\n")
	}
	if DeletedFilesCount > 0 {
		fmt.Fprintf(logBuffer, "Deleted files (%d):\n", DeletedFilesCount)
		tmpDeletedFiles.Sync()
		file, err := os.OpenFile(tmpUpdateFiles.Name(), os.O_RDWR, 0644)
		check(err, "Something wrong:")
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fmt.Fprintln(logBuffer, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			check(err, "Something wrong:")
		}
		file.Close()
		fmt.Fprint(logBuffer, "===============================================================================\n")
	}

	logBuffer.Flush()
	logfile.Close()
	fmt.Println("Upload log file ...")
	err = archiveUpload(configGlobal.execDir + reportFileName)
	check(err, "Can not upload log file")
	os.Remove(configGlobal.execDir + reportFileName)

	//dropbox clean
	DropboxCLean()

	fmt.Println("Result:")
	fmt.Printf("\t found %d new files (%s)\n", NewFilesCount, humanize.Bytes(uint64(sizeNewFiles)))
	fmt.Printf("\t found %d updated files (%s)\n", UpdatedFilesCount, humanize.Bytes(uint64(sizeUpdatedFiles)))
	fmt.Printf("\t found %d deleted files\n", DeletedFilesCount)
	fmt.Printf("\t found %d unchanged files (%s)\n", UnchangedFilesCount, humanize.Bytes(uint64(sizeUnchangedFiles)))
	fmt.Printf("\t total %d files found (%s)\n", AllFilesCount, humanize.Bytes(uint64(sizeNewFiles+sizeUnchangedFiles+sizeUpdatedFiles)))
	fmt.Printf("\t %d archives created (%s)\n", zipArchivePart, humanize.Bytes(uint64(zipArchiveSizeTotal)))

	//exit
	os.Remove(tmpNewFiles.Name())
	os.Remove(tmpUpdateFiles.Name())
	os.Remove(tmpDeletedFiles.Name())
	fmt.Printf("\nAll work done! (take %s)\n", SecToTime(int64(time.Now().Sub(configGlobal.timeStart).Seconds())))

}
