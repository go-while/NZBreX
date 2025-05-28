#!/bin/bash

set -e

# Packages to install
INSTALL_PACKAGES="aptitude build-essential ca-certificates curl git dpkg-dev haveged musl-tools nano nginx net-tools htop psmisc sudo tar tmux unattended-upgrades vim vnstat vnstati wget zip"

CHECK_RUNNERS_URL="https://github.com/go-while/NZBreX/raw/ee7ab8d8bdbc675c32c4dc7044d50092e03c72df/runner/check_runners.sh"

# SSH username (replace with the correct one)
USER="root"


# Function to SSH and install on a given host
install_apt_packages() {
  local ip="$1"
  local host="$2"
  echo -e "\ninstall: Connecting to $host ($ip)..."
  ssh -n -o ConnectTimeout=5 "$USER@$ip" "apt-get -y update && apt-get install -y $INSTALL_PACKAGES"
  ret=$?
  if [ $ret -eq 0 ]; then
    echo "[$host] Success"
  else
    echo "[$host] Failed"
  fi
  # Don't use 'return' here when running from a while/read pipe
}

restart_runners_remote() {
  local ip="$1"
  local host="$2"
  echo -e "\nrrr: Connecting to $host ($ip)..."

  ssh -n -o ConnectTimeout=5 "$USER@$ip" '
while IFS="" read -r USER; do
  echo killing $USER @ $(hostname)
  killall -u "$USER"
  sleep 3
  ps aux|grep "$USER" | grep -v grep
  /home/$USER/actions-runner/RUN.sh
  ps aux|grep "$USER" | grep tmux | grep -v grep
done < <(ls /home)
'
  ret=$?
  if [ $ret -eq 0 ]; then
    echo "[$host] Success: restart runner"
  else
    echo "[$host] Failed: restart runner"
  fi
}


update_TEMPLATE() {
  # uses the function name as identifier!
  # be careful to use '${FUNCNAME[0]}' inside
  # and normal ${FUNCNAME[0]} outside of the ssh command!
  local ip="$1"
  local host="$2"
  echo -e "${FUNCNAME[0]} Connecting to $host ($ip)..."

  ssh -n -o ConnectTimeout=5 "$USER@$ip" '
if [ -e "/root/.'${FUNCNAME[0]}'" ]; then
 echo -e "$(hostname): OK found: /root/.'${FUNCNAME[0]}'"
 exit 0
fi
### info line
echo -e "'${FUNCNAME[0]}': updating XYXYZYXYX"
###
### commands here ###
### commands here ###
### commands here ###
### commands here ###
### commands here ###
### commands here ###
### commands here ###
# flag installed!
touch /root/.'${FUNCNAME[0]}'
exit 0
'
  ret=$?
  if [ $ret -eq 0 ]; then
    echo -e "[$host] Success: ${FUNCNAME[0]}"
  else
    echo -e "[$host] Failed: ${FUNCNAME[0]}"
  fi
} # end update_TEMPLATE

update_0002() {
  local ip="$1"
  local host="$2"
  echo -e "${FUNCNAME[0]} Connecting to $host ($ip)..."

  ssh -n -o ConnectTimeout=5 "$USER@$ip" '
if [ -e "/root/.'${FUNCNAME[0]}'" ]; then
 echo -e "$(hostname): OK found: /root/.'${FUNCNAME[0]}'"
 exit 0
fi
echo -e "'${FUNCNAME[0]}': updating /root/check_runners.sh"

wget -q '${CHECK_RUNNERS_URL}' -O /root/check_runners.sh.new && \
 mv -v /root/check_runners.sh.new /root/check_runners.sh && \
  chmod -v +x /root/check_runners.sh

cat <<EOF > /var/spool/cron/crontabs/root
MAILTO=
* * * * * /root/check_runners.sh
* * * * * /root/cron.sh
EOF
/etc/init.d/cron restart
# flag installed!
touch /root/.'${FUNCNAME[0]}'
exit 0
'
  ret=$?
  if [ $ret -eq 0 ]; then
    echo -e "[$host] Success: ${FUNCNAME[0]}"
  else
    echo -e "[$host] Failed: ${FUNCNAME[0]}"
  fi
} # end update_0002



# Read /etc/hosts for runners
RESPONSES_FILE=$(mktemp)
while IFS="" read -r line; do
  IP=$(echo "$line" | awk '{print $1}')
  HOST=$(echo "$line" | awk '{print $2}')
  echo -e -n "\n\n... updating $HOST ... "

  ### install apt packages
  install_apt_packages "$IP" "$HOST" >> "$RESPONSES_FILE" 2>&1
  ret=$?; echo " code $ret"
  echo -e "\n" >> "$RESPONSES_FILE"

  ### execute updates
  #update_0001 "$IP" "$HOST"
  update_0002 "$IP" "$HOST"

  ### restart runners
  #restart_runners_remote "$IP" "$HOST"

done < <(
  grep -E '\s(runner-deb1[0-2]|runner-ubu(1604|1804|2004|2204|2404|2504)|runner-ubu2204-[0-9]{3}|runner-ubu(2204|2404|2504)-gor)$' /etc/hosts \
    | grep -v "#")
#cat "$RESPONSES_FILE"
rm -f "$RESPONSES_FILE"



