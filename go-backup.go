package main

import (
	"fmt"
	"io/ioutil"
	"strings"
	"regexp"
	"os"
	"path/filepath"
	"encoding/gob"
	"crypto/md5"
	"encoding/hex"
	"archive/zip"
	"io"
	"github.com/NightMan-1/go-dropbox"
	"github.com/NightMan-1/go-dropy"
	"time"
	"bufio"
	"strconv"
	"path"
	"github.com/dustin/go-humanize"
	"github.com/djherbis/times"
	"reflect"
)

type configGlobalStruct struct {
	Sources []string
	dbFile, archivePrefix string
	dropboxToken string
	timeStart time.Time
	scheduleKeep int64
	scheduleFullArchiveDays map[int64]int64
	execDir string
}
var configGlobal (configGlobalStruct)
var dropboxConnection *dropy.Client

var zipFile *os.File
var err error
var zipArchive *zip.Writer
var zipSourceSize int64 =  2000000000 //2Gb
//var zipSourceSize int64 =  256000000 //256Mb
//var zipSourceSize int64 =  10000000 //10Mb
var sizeNewFiles, sizeUpdatedFiles, sizeUnchangedFiles, sizeCurrent int64
var zipArchivePart, zipArchiveSizeTotal int64
var backupType string


type FileInfoStruct struct {
	Name string
	//Size, BirthTime, AccessTime, ModTime, ChangeTime int64
	Size, ModTime, ChangeTime int64
}
type FileInfoStructSmall struct {
	Name string
	ArchivePart int64
}

var OldFilesList = make(map[string]FileInfoStruct)
var DeletedFilesList = make(map[string]FileInfoStruct)
var AllFilesList = make(map[string]FileInfoStruct)
var NewFilesList = make(map[string]FileInfoStructSmall)
var UpdatedFilesList = make(map[string]FileInfoStructSmall)
var UnchangedFilesList = make(map[string]FileInfoStructSmall)

//return program head
func headText() string{
	hostname,_ := os.Hostname()
	startTime := fmt.Sprintf("%d-%02d-%02d %02d:%02d:%02d",configGlobal.timeStart.Year(), configGlobal.timeStart.Month(), configGlobal.timeStart.Day(), configGlobal.timeStart.Hour(), configGlobal.timeStart.Minute(), configGlobal.timeStart.Second())

	headText := "###############################################################################\n"
	headText += "GoBackup to Dropbox version 1.2\n"
	headText += "Server Name - " + hostname + "\n"
	if (backupType != "") {
		headText += "Backup type: " + backupType + "\n"
	}
	headText += "Backup start at " + startTime  + "\n"
	headText += "###############################################################################\n"

	return headText

}


//инициализация
func initSystem()  {

	//читаем настройки
	configDataRaw, err := ioutil.ReadFile("config.ini")
	if (err != nil){
		ex, err := os.Executable()
		if err != nil {
			panic(err)
		}
		configGlobal.execDir = path.Dir(ex) + "/"
		configDataRaw, err = ioutil.ReadFile(configGlobal.execDir + "config.ini")
		check(err, "Can not read config.ini")
	}else{
		configGlobal.execDir = "./"
	}
	configDataStr := string(configDataRaw)
	configDataArray := strings.Split(configDataStr, "\n")
	rS, _ := regexp.Compile(`\[sources\]`)
	rE, _ := regexp.Compile(`\[`)
	rComment, _ := regexp.Compile(`#`)
	rDBFile, _ := regexp.Compile(`.*db_file.*=\W*(.+)$`)
	rarchivePrefix, _ := regexp.Compile(`.*archive_prefix.*=\W*(.+)$`)
	rdropboxToken, _ := regexp.Compile(`.*dropbox_token.*=\W*(.+)$`)

	rdScheduleFullArchiveDays, _ := regexp.Compile(`.*full_archive.*=\W*(.+)$`)
	rdScheduleKeepDays, _ := regexp.Compile(`.*keep_days.*=\W*(.+)$`)

	configGlobal.scheduleFullArchiveDays = make(map[int64]int64)

	sourcesCheck := false
	for i := range configDataArray {
		str := strings.TrimSpace(configDataArray[i])
		//skip comments
		if (rComment.MatchString(str)){ continue }

		//находим начало списка файлов
		if (rS.MatchString(str)){
			sourcesCheck = true
			continue
		}
		if (sourcesCheck && ! rE.MatchString(str) && len(str) > 0){
			configGlobal.Sources = append(configGlobal.Sources, str)
			//отключаем в конце списка
		}else{
			sourcesCheck = false
		}

		checkDBConf := rDBFile.FindStringSubmatch(str)
		if (len(checkDBConf) == 2){configGlobal.dbFile = checkDBConf[1] }

		checkArchivePrefix := rarchivePrefix.FindStringSubmatch(str)
		if (len(checkArchivePrefix) == 2){configGlobal.archivePrefix = checkArchivePrefix[1] }

		//dropbox
		checkDropboxToken := rdropboxToken.FindStringSubmatch(str)
		if (len(checkDropboxToken) == 2){ configGlobal.dropboxToken = checkDropboxToken[1] }

		//schedule
		checkScheduleFullArchiveDays := rdScheduleFullArchiveDays.FindStringSubmatch(str)
		if (len(checkScheduleFullArchiveDays) == 2){
			//try split multiple days
			days := strings.Split(checkScheduleFullArchiveDays[1], ",")
			if (len(days)>1){
				for _, v := range days {
					i := StrToInit64(v)
					configGlobal.scheduleFullArchiveDays[i] = i
				}
			}else{
				i := StrToInit64(checkScheduleFullArchiveDays[1])
				configGlobal.scheduleFullArchiveDays[i] = i
			}

		}
		checkScheduleKeepDays := rdScheduleKeepDays.FindStringSubmatch(str)
		if (len(checkScheduleKeepDays) == 2 ){
			configGlobal.scheduleKeep = StrToInit64(checkScheduleKeepDays[1])
		}
	}

	if (configGlobal.dbFile == ""){ fmt.Println("DB file is not set in config.ini"); os.Exit(1); }

	if (configGlobal.dropboxToken == ""){ fmt.Println("Dropbox Token is not set in config.ini"); os.Exit(1); }

	//read old file list
	if _, err := os.Stat(configGlobal.execDir + configGlobal.dbFile); os.IsNotExist(err) {
		//first start
		fmt.Print("processing full backup...")
	}else {
		//open file database ... only when requested ..
		if (configGlobal.scheduleFullArchiveDays[int64(configGlobal.timeStart.Day())] == 0){
			backupType = "incremental"
			fmt.Print("processing incremental backup...")
			file, err := os.Open(configGlobal.execDir + configGlobal.dbFile)
			check(err, "Error open data file")
			decoder := gob.NewDecoder(file)
			OldFilesListTmp := new(map[string]FileInfoStruct)
			err = decoder.Decode(OldFilesListTmp)
			//check(err, "Error decode data file")
			if err != nil {
				file.Close()
				os.Remove(configGlobal.execDir + configGlobal.dbFile)
				fmt.Println("Error decode data file")
				panic(err)
			}
			file.Close()
			for k, v := range *OldFilesListTmp {
				OldFilesList[k] = v
			}
			OldFilesListTmp = nil
		}else{
			backupType = "full"
			fmt.Print("processing full backup...")
		}
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
	RI, _  := strconv.ParseInt(strings.Trim(s, " "), 10, 64)
	return RI
}

func DropboxCLean()  {
	fmt.Println("Check old archives...")

	files, err := dropboxConnection.ListFiles("/")
	check(err, "Error 5342")

	archiveKeepDate := configGlobal.timeStart.AddDate(0, 0, -int(configGlobal.scheduleKeep)).Unix()

	for _, file := range files {
		if (file.IsDir()){
			continue
		}

		rFileTime, _ := regexp.Compile(`^`+ configGlobal.archivePrefix + `_(\d\d\d\d_\d\d_\d\d_\d\d-\d\d-\d\d).+?`)
		FileTimeArray := rFileTime.FindStringSubmatch(file.Name())
		if (len(FileTimeArray) == 2){
			archiveDate, err := time.Parse("2006_01_02_15-04-05", FileTimeArray[1])
			if err != nil {
				continue
			}
			if (archiveDate.Unix() < archiveKeepDate){
				fmt.Println("\t deleting /" +  file.Name())
				dropboxConnection.Delete("/" + file.Name())
			}
		}
	}


}

func SecToTime(seconds int64) string   {
	minutes := seconds/60
	hours := minutes/60
	m_diff := minutes - (hours*60)
	s_diff := seconds - (minutes*60)

	return fmt.Sprintf("%02d:%02d:%02d", hours, m_diff, s_diff)
}

func addToArchive(path string, writer io.Writer, info os.FileInfo ) error {

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
	check(err, "Error: can not open " + archiveFile)

	info, _ := zipfile.Stat()
	zipArchiveSizeTotal += info.Size()

	err = dropboxConnection.UploadSession(archiveFileName, zipfile)
	zipfile.Close()
	return  err

}

func main() {
	configGlobal.timeStart = time.Now()
	fmt.Print(headText())
	fmt.Print("Init system...")
	initSystem()
	fmt.Print("\n")

	fmt.Print("Archive files...\n")

	archiveName := configGlobal.archivePrefix + fmt.Sprintf("_%d_%02d_%02d_%02d-%02d-%02d", configGlobal.timeStart.Year(), configGlobal.timeStart.Month(), configGlobal.timeStart.Day(), configGlobal.timeStart.Hour(), configGlobal.timeStart.Minute(), configGlobal.timeStart.Second())

	var fileName string = ""
	for _, folder := range configGlobal.Sources {
		if _, err := os.Stat(folder); err == nil {
			fmt.Println("\tprocessing " + folder)
			filepath.Walk(folder, func(path string, info os.FileInfo, err error) error {
				//new archive
				if (zipFile == nil){
					zipArchivePart = 1
					fileName =  archiveName + fmt.Sprintf("_part%d", zipArchivePart) + ".zip"
					fmt.Println("\tcreate " + fileName)
					zipFile, err = os.Create(configGlobal.execDir + fileName)
					check(err, "Error 845")
					zipArchive = zip.NewWriter(zipFile)
					//new archive PART
				}else if (zipFile != nil && (sizeCurrent > zipSourceSize || (sizeCurrent + info.Size() > zipSourceSize && sizeCurrent/2 > zipSourceSize))){
					zipArchive.Close()
					zipInfo, _ := zipFile.Stat()
					zipFile.Close()
					//upload to dropbox
					fmt.Printf("\tupload %s (%s)\n", fileName, humanize.Bytes(uint64(zipInfo.Size())))
					err = archiveUpload(configGlobal.execDir + fileName)
					check(err, "Upload error")
					os.Remove(configGlobal.execDir + fileName)

					zipArchivePart += 1
					fileName =  archiveName + fmt.Sprintf("_part%d", zipArchivePart) + ".zip"
					fmt.Println("\tcreate " + fileName)
					zipFile, err = os.Create(configGlobal.execDir + fileName)
					check(err, "Error 846")
					zipArchive = zip.NewWriter(zipFile)
				}

				header, err := zip.FileInfoHeader(info)
				check(err, "error getting header " + path)
				header.Name = path

				if info.IsDir() {
					header.Name += "/"
					header.Method = zip.Store
				} else {
					header.Method = zip.Deflate
				}

				writer, err := zipArchive.CreateHeader(header)
				check(err, "Error 847")

				//TODO check не архивировать свои текущие архивы
				r, _ := regexp.Compile(filepath.Clean(zipFile.Name()) + "$")
				if (len(r.FindStringSubmatch(filepath.Clean(path))) > 0){
					return nil
				}

				if info.IsDir() {
					return nil
				}
				//TODO check for other file types
				if header.Mode().IsRegular() {
					var MD5FileName string = GetMD5Hash(path)
					var ctime int64 = 0
					fi, _ := times.Stat(path)
					ctime = fi.ChangeTime().Unix()

					currentFile := FileInfoStruct{path, info.Size(), info.ModTime().Unix(), ctime}

					if oldFile, ok := OldFilesList[MD5FileName]; ok {
						if (reflect.DeepEqual(oldFile, currentFile)){
							//unchanged files
							sizeUnchangedFiles += info.Size()
							UnchangedFilesList[MD5FileName] = FileInfoStructSmall{path, zipArchivePart}
						}else{
							//updated files
							err = addToArchive(path, writer, info)
							if err != nil { fmt.Println(err) }
							sizeUpdatedFiles += info.Size()
							//sizeCurrent += info.Size()
							UpdatedFilesList[MD5FileName] = FileInfoStructSmall{path, zipArchivePart}
						}
						delete(OldFilesList, MD5FileName)
					}else{
						//new files
						err = addToArchive(path, writer, info)
						if err != nil { fmt.Println(err) }
						sizeNewFiles += info.Size()
						//sizeCurrent += info.Size()
						NewFilesList[MD5FileName] = FileInfoStructSmall{path, zipArchivePart}
					}
					AllFilesList[MD5FileName] = currentFile
				}

				zipInfo, _ := zipFile.Stat()
				sizeCurrent = zipInfo.Size()

				return nil
			})
		}
	}
	DeletedFilesList = OldFilesList

	if (zipFile != nil){
		zipArchive.Close()
		zipInfo, _ := zipFile.Stat()
		zipFile.Close()

		//nothing found
		totalArchivedFiles := len(NewFilesList) + len(UpdatedFilesList)
		if (totalArchivedFiles == 0){
			os.Remove(configGlobal.execDir + fileName)
			fmt.Println("New/updated files not found.")
			fmt.Printf("\nAll work done! (take %s)\n", SecToTime(int64(time.Now().Sub(configGlobal.timeStart).Seconds())))
			os.Exit(0)
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
	fmt.Fprintf(logBuffer,"\t found %d new files (%s)\n", len(NewFilesList), humanize.Bytes(uint64(sizeNewFiles)))
	fmt.Fprintf(logBuffer,"\t found %d updated files (%s)\n", len(UpdatedFilesList), humanize.Bytes(uint64(sizeUpdatedFiles)))
	fmt.Fprintf(logBuffer,"\t found %d deleted files\n", len(DeletedFilesList))
	fmt.Fprintf(logBuffer,"\t found %d unchanged files (%s)\n", len(UnchangedFilesList), humanize.Bytes(uint64(sizeUnchangedFiles)))
	fmt.Fprintf(logBuffer,"\t total %d files found (%s)\n", len(AllFilesList), humanize.Bytes(uint64(sizeNewFiles + sizeUnchangedFiles + sizeUpdatedFiles)))
	fmt.Fprintf(logBuffer,"\t %d archives created (%s)\n", zipArchivePart, humanize.Bytes(uint64(zipArchiveSizeTotal)))
	fmt.Fprint(logBuffer, "===============================================================================\n")
	if (len(NewFilesList) > 0){
		fmt.Fprintf(logBuffer, "New files (%d):\n", len(NewFilesList))
		for _, v := range NewFilesList { fmt.Fprintf(logBuffer, "\t(archive %d) %s\n", v.ArchivePart, v.Name) }
		fmt.Fprint(logBuffer, "===============================================================================\n")
	}
	if (len(UpdatedFilesList) > 0){
		fmt.Fprintf(logBuffer, "Updated files (%d):\n", len(UpdatedFilesList))
		for _, v := range UpdatedFilesList { fmt.Fprintf(logBuffer, "\t(archive %d) %s\n", v.ArchivePart, v.Name) }
		fmt.Fprint(logBuffer, "===============================================================================\n")
	}
	if (len(DeletedFilesList) > 0){
		fmt.Fprintf(logBuffer, "Deleted files (%d):\n", len(DeletedFilesList))
		for _, v := range DeletedFilesList { fmt.Fprintf(logBuffer, "\t%s\n", v.Name) }
		fmt.Fprint(logBuffer, "===============================================================================\n")
	}

	logBuffer.Flush()
	logfile.Close()
	fmt.Println("Upload log file ...")
	err = archiveUpload(configGlobal.execDir + reportFileName)
	check(err, "Can not upload log file")
	os.Remove(configGlobal.execDir + reportFileName)

	//TODO debug
	//fmt.Printf("\n%s\n", SecToTime(int64(time.Now().Sub(configGlobal.timeStart).Seconds())))
	//os.Exit(0)


	//dropbox clean
	DropboxCLean()


	fmt.Println("Result:")
	fmt.Printf("\t found %d new files (%s)\n", len(NewFilesList), humanize.Bytes(uint64(sizeNewFiles)))
	fmt.Printf("\t found %d updated files (%s)\n", len(UpdatedFilesList), humanize.Bytes(uint64(sizeUpdatedFiles)))
	fmt.Printf("\t found %d deleted files\n", len(DeletedFilesList))
	fmt.Printf("\t found %d unchanged files (%s)\n", len(UnchangedFilesList), humanize.Bytes(uint64(sizeUnchangedFiles)))
	fmt.Printf("\t total %d files found (%s)\n", len(AllFilesList), humanize.Bytes(uint64(sizeNewFiles + sizeUnchangedFiles + sizeUpdatedFiles)))

	fmt.Printf("\t %d archives created (%s)\n", zipArchivePart, humanize.Bytes(uint64(zipArchiveSizeTotal)))

	//save on exit
	file, err := os.Create(configGlobal.execDir + configGlobal.dbFile)
	if err == nil {
		encoder := gob.NewEncoder(file)
		encoder.Encode(AllFilesList)
	}
	file.Close()
	fmt.Printf("\nAll work done! (take %s)\n", SecToTime(int64(time.Now().Sub(configGlobal.timeStart).Seconds())))

}
