#!/bin/bash
for user in $(ls /home); do
  pid=$(pgrep -u "$user" -f 'Runner.Listener')
  if [ -n "$pid" ]; then
    echo "Runner.Listener is running for user $user (pid: $pid)"
  else
    echo "Runner.Listener is NOT running for user $user, starting..."
    sudo -u "$user" /home/$user/actions-runner/run.sh &
    # Optionally: add logging or error handling here
  fi
done
