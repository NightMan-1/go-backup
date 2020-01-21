# Backup files to Dropbox

Compress files and upload zip archives to Dropbox folder

Support incremental backup

Work on Linux, Mac, Windows

## Installation

1) open https://www.dropbox.com/developers/apps/
2) create application
3) modify config.ini __(not forgot create and add token!)__

~~~sh
go mod download
go build go-backup.go
~~~

## Command line parameters
`--full` -  force full backup


## Credits
Copyright © 2017-2020 [Sergey Gurinovich](mailto:sergey@fsky.info)
