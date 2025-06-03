#!/bin/sh

GO_VER="go1.24.3"

test -z "$1" && echo "usage: $0 USERDIR"

if [ ! -e "~/.install_$GO_VER" ]; then
 wget https://go.dev/dl/$GO_VER.linux-amd64.tar.gz -O /tmp/$GO_VER.linux-amd64.tar.gz
 tar -C /usr/local -xzf /tmp/$GO_VER.linux-amd64.tar.gz
 echo 'export PATH=$PATH:/usr/local/go/bin' >> "$USERDIR"/.bashrc
 #source ~/.profile
 touch ".install_$GO_VER"
fi

# uninstall
USERDIR=/home/go-while
rm -r /usr/local/go
sed '/go\/bin/d' -i "$USERDIR"/.bashrc "$USERDIR"/.profile

