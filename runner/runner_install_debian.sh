#!/bin/sh
# Inside your LXC/QEMU/KVM container (e.g., based on Debian 10, 11, 12 and Ubuntu 20.04, 22.04, 24.04)
# install requires:
# 0. wget https://raw.githubusercontent.com/go-while/NZBreX/refs/heads/8-rewrite-workflow-for-nzbrex/runner/runner_install_debian.sh -O runner_install_debian.sh
# 1. run this script twice without arguments: /root/runner_install_debian.sh
# 2. run this script with arguments /root/runner_install_debian.sh "SYSUSER" "USERDIR" "GITNAME" "GITREPO" "GATOKEN" "LABEL" "NAME" "GROUP"

GO_VER="go1.24.3"
RUNNER_VERSION="2.324.0"
RUNNER_FILENAME="actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz"
RUNNER_SHA256="e8e24a3477da17040b4d6fa6d34c6ecb9a2879e800aa532518ec21e49e21d7b4"

GITHUB_URL="https://github.com"
GITREL_URL="${GITHUB_URL}/actions/runner/releases/download"

test $(whoami) != "root" && echo "error: you are not root" && exit 1
test $(pwd) != "/root" && echo "error: you are not in /root" && exit 1

if [ ! -e ".runner_apt_update" ]; then
 echo "preparing machine...."
 apt update -y && apt full-upgrade -y && echo "$(date +%s)" > ".runner_apt_update"
 test $? -eq 0 && echo "... rebooting in 10 seconds" && sleep 10 && reboot
 exit 10
elif [ ! -e ".runner_apt_install" ]; then
 apt update -y && apt install -y aptitude build-essential ca-certificates curl git dpkg-dev haveged nano nginx net-tools htop psmisc sudo tar tmux unattended-upgrades vim vnstat vnstati wget zip && echo "$(date +%s)" > ".runner_apt_install" || exit 11
 apt clean
 vnstat --create -i eth0 || vnstat --add -i eth0
 systemctl restart vnstat
 touch ".runner_apt_install"
fi

if [ ! -e ".install_$GO_VER" ]; then
 wget https://go.dev/dl/$GO_VER.linux-amd64.tar.gz -O /tmp/$GO_VER.linux-amd64.tar.gz
 tar -C /usr/local -xzf /tmp/$GO_VER.linux-amd64.tar.gz
 echo 'export PATH=$PATH:/usr/local/go/bin' >> "$USERDIR"/.profile
 #source ~/.profile
 touch ".install_$GO_VER"
fi

test -z "$8" && echo "usage: $0 SYSUSER USERDIR GITNAME GITREPO GATOKEN LABEL" && exit 1
SYSUSER="$1"; USERDIR="$2"; GITNAME="$3"; GITREPO="$4"; GATOKEN="$5"; LABEL="$6"; NAME="$7" GROUP="$8"
echo "setup SYSUSER=$SYSUSER USERDIR=$USERDIR GITNAME=$GITNAME GITREPO=$GITREPO GATOKEN=$GATOKEN LABEL=$LABEL NAME=$NAME GROUP=$GROUP"
if [ ! -e "$USERDIR" ]; then
 useradd --create-home --home-dir "$USERDIR" --shell /bin/bash "$SYSUSER" || exit 2
fi
cd "$USERDIR" || exit 3
mkdir -p actions-runner && cd actions-runner || exit 4
curl -o "$RUNNER_FILENAME" -L "${GITREL_URL}/v${RUNNER_VERSION}/${RUNNER_FILENAME}" || exit 5
tar xzf "$RUNNER_FILENAME" || exit 6
echo "$RUNNER_SHA256  $RUNNER_FILENAME" | shasum -a 256 -c || exit 7
chown -R "$SYSUSER" "$USERDIR" || exit 8
sudo -u "$SYSUSER" ./config.sh --url "https://github.com/${GITNAME}/${GITREPO}" --token "${GATOKEN}" --labels "${LABELS}" --name "${NAME}" --runnergroup "${GROUP}" --replace --unattended || exit 9
echo -n > /root/.bash_history
rm -v "$RUNNER_FILENAME"
echo "sudo -u \"$SYSUSER\" tmux new-session -d -s \"${NAME}-${SYSUSER}\" \"${USERDIR}\"/actions-runner/run.sh" > "${USERDIR}"/actions-runner/RUN.sh
test -e "${USERDIR}"/actions-runner/RUN.sh && echo "created: ${USERDIR}/actions-runner/RUN.sh" || exit 10
chmod +x "${USERDIR}"/actions-runner/RUN.sh && "${USERDIR}"/actions-runner/RUN.sh || exit 11


mkdir -p /var/www/html/ga
cat <<EOF > /root/cron.sh
echo -n > /var/www/html/ga/netstats.dat
echo "rx_bytes:eth0:\$(cat /sys/class/net/eth0/statistics/rx_bytes)" >> /var/www/html/ga/netstats.dat.new
echo "tx_bytes:eth0:\$(cat /sys/class/net/eth0/statistics/tx_bytes)" >> /var/www/html/ga/netstats.dat.new
mv /var/www/html/ga/netstats.dat.old.1 /var/www/html/ga/netstats.dat.old.2
mv /var/www/html/ga/netstats.dat /var/www/html/ga/netstats.dat.old.1
mv /var/www/html/ga/netstats.dat.new /var/www/html/ga/netstats.dat
EOF
chmod +x /root/cron.sh


cat <<EOF > /var/spool/cron/crontabs/root
MAILTO=""
@reboot /home/$SYSUSER/actions-runner/RUN.sh
* * * * * /root/cron.sh
EOF
systemctl restart cron.service

sudo apt-get update
sudo apt-get install -y

exit 0

