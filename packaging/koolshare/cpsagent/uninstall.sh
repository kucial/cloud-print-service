#!/bin/sh
source /koolshare/scripts/base.sh

sh /koolshare/scripts/cpsagent_config.sh stop >/dev/null 2>&1

find /koolshare/init.d/ -name "*cpsagent*" | xargs rm -rf >/dev/null 2>&1
rm -f /koolshare/bin/cps-agent
rm -f /koolshare/res/icon-cpsagent.png
rm -f /koolshare/scripts/cpsagent_*.sh
rm -f /koolshare/scripts/uninstall_cpsagent.sh
rm -f /koolshare/webs/Module_cpsagent.asp
# agent.json holds the agent's credential: don't leave it behind.
rm -rf /koolshare/configs/cpsagent
rm -f /tmp/upload/cpsagent_* /tmp/cpsagent.enroll

for value in $(dbus list cpsagent_ | cut -d "=" -f 1); do
	dbus remove $value
done
