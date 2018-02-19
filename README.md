# GoLang backup to Dropbox

Tested on Linux, Mac, Windows

## Installation

1) open https://www.dropbox.com/developers/apps/
2) create application
3) modify config.ini __(not forgot create and add token!)__

~~~sh
go get github.com/djherbis/times
go get github.com/dustin/go-humanize
go get github.com/NightMan-1/go-dropbox
go get github.com/NightMan-1/go-dropy
go get github.com/siddontang/ledisdb/ledis

go build go-backup.go
~~~

## Credits
Copyright © 2017 [Sergey Gurinovich](mailto:sergey@fsky.info).
