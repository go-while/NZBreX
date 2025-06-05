go build -o NZBreX $(ls *.go|grep -v signals_windows) 
exit $?
