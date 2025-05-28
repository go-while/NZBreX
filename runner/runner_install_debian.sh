#!/bin/sh
#
# WARNING !
# This Script: OVERWRITES SYSTEM STUFF! USE /AT YOUR OWN RISK AND/ ONLY ON A NEWLY INSTALLED MACHINE!
#
# Inside your LXC/QEMU/KVM container (e.g., based on Debian 10, 11, 12 and Ubuntu 20.04, 22.04, 24.04)
# install requires:
# 0. wget https://github.com/go-while/NZBreX/raw/refs/heads/gpt-tests-1748319193/runner/runner_install_debian.sh -O runner_install_debian.sh
# 1. run this script !twice! without arguments: /root/runner_install_debian.sh
# 2. run this script with arguments /root/runner_install_debian.sh "SYSUSER" "USERDIR" "GITNAME" "GITREPO" "GATOKEN" "LABELS" "NAME" "GROUP"

RUNNER_VERSION="2.324.0"
RUNNER_FILENAME="actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz"
RUNNER_SHA256="e8e24a3477da17040b4d6fa6d34c6ecb9a2879e800aa532518ec21e49e21d7b4"

GITHUB_URL="https://github.com"
GITREL_URL="${GITHUB_URL}/actions/runner/releases/download"

CHECK_RUNNERS_URL="https://github.com/go-while/NZBreX/raw/ee7ab8d8bdbc675c32c4dc7044d50092e03c72df/runner/check_runners.sh"

test $(whoami) != "root" && echo "error: you are not root" && exit 1
test $(pwd) != "/root" && echo "error: you are not in /root" && exit 1

# Exit immediately if a command exits with a non-zero status.
set -e

ping -c 3 8.8.8.8
ping -c 3 google.com

if [ ! -e ".runner_apt_update" ]; then
 echo "preparing machine...."
 apt update -y && apt full-upgrade -y && echo "$(date +%s)" > ".runner_apt_update"
 test $? -eq 0 && echo "... rebooting in 10 seconds" && sleep 10 && reboot
 exit 10
elif [ ! -e ".runner_apt_install" ]; then
 apt update -y && apt install -y aptitude build-essential ca-certificates curl git dpkg-dev haveged musl-tools nano nginx net-tools htop psmisc sudo tar tmux unattended-upgrades vim vnstat vnstati wget zip && echo "$(date +%s)" > ".runner_apt_install" || exit 11
 apt clean
 touch ".runner_apt_install"
fi

test -z "$8" && echo "usage: $0 SYSUSER USERDIR GITNAME GITREPO GATOKEN LABELS" && exit 1
SYSUSER="$1"; USERDIR="$2"; GITNAME="$3"; GITREPO="$4"; GATOKEN="$5"; LABELS="$6"; NAME="$7" GROUP="$8"
if [ ! -e "$USERDIR" ]; then
 echo "setup SYSUSER=$SYSUSER USERDIR=$USERDIR GITNAME=$GITNAME GITREPO=$GITREPO GATOKEN=$GATOKEN LABELS=$LABELS NAME=$NAME GROUP=$GROUP"
 useradd --create-home --home-dir "$USERDIR" --shell /bin/bash "$SYSUSER" || exit 2
else
 echo "ERROR USER $SYSUSER already setup in same $USERDIR!"
 exit 3
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
echo "sudo -u \"$SYSUSER\" tmux new-session -d -s \"${NAME}-${SYSUSER}\" \"${USERDIR}/actions-runner/run.sh\"" > "${USERDIR}/actions-runner/RUN.sh"
test -e "${USERDIR}/actions-runner/RUN.sh" && echo "created: ${USERDIR}/actions-runner/RUN.sh" || exit 10
chmod +x "${USERDIR}"/actions-runner/RUN.sh && "${USERDIR}"/actions-runner/RUN.sh || exit 11
## print runner config
echo "Config: ${USERDIR}"/actions-runner/.runner
cat "${USERDIR}"/actions-runner/.runner


if [ ! -e /root/cron.sh ]; then
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
fi

if [ ! -e "/root/check_runners.sh" ]; then
  wget -q "$CHECK_RUNNERS_URL" -O /root/check_runners.sh.new && \
   mv -v /root/check_runners.sh.new /root/check_runners.sh && \
    chmod -v +x /root/check_runners.sh && \
     cat <<EOF > /var/spool/cron/crontabs/root
MAILTO=""
* * * * * /root/check_runners.sh
* * * * * /root/cron.sh
EOF
/etc/init.d/cron restart
fi



#cat <<EOF > "/var/spool/cron/crontabs/$SYSUSER"
#MAILTO=""
#* * * * * pidof Runner.Listener || /home/$SYSUSER/actions-runner/run.sh
#EOF



exit 0

