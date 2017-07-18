# GoLang backup to Dropbox

Tested on Linux, Mac, Windows

## Installation

1) open https://www.dropbox.com/developers/apps/
2) create application
3) modify config.ini (not forgot create and add token!)

~~~sh
go get github.com/djherbis/times
go get github.com/stacktic/dropbox
go get github.com/dustin/go-humanize

go build go-backup.go
~~~

## Credits
Copyright © 2017 [Sergey Gurinovich](mailto:sergey@fsky.info).
