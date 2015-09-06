#!/bin/bash

[ -n "$DEBUG" ] && set -o xtrace
set -o nounset
set -o errexit
shopt -s nullglob

cd $(dirname "${0}")

cgroup_path="${GARDEN_CGROUP_PATH}"

## Description  : systemd cgroup combine cpu,cpuacct together in centos 7
## Autor        : lvguanglin
## Modified Time: 2015/05/27
function command_exists() {
  command -v "$@" > /dev/null 2>&1
}

function num_cmp(){
  if [ $# -lt  3 ];then
    return 1
  fi
  local res=$(echo "$1 $3"|awk "{ res = \$1 $2 \$2; print res;}")
  if [ $res -eq 1 ];then
    return 0
  else
    return 1
  fi
}

function get_lsb() {
  # perform some very rudimentary platform detection
  lsb_dist=''
  if command_exists lsb_release; then
    lsb_dist="$(lsb_release -si)"
  fi
  if [ -z "$lsb_dist" ] && [ -r /etc/lsb-release ]; then
    lsb_dist="$(. /etc/lsb-release && echo "$DISTRIB_ID")"
  fi
  if [ -z "$lsb_dist" ] && [ -r /etc/centos-release ]; then
    num=$(cat /etc/centos-release|\
      awk '{for(i=1;i<=NF;i++){if($i ~ /[0-9]/) print $i}}')
    lsb_dist=centos-${num:0:3}
  fi
  if [ -z "$lsb_dist" ] && [ -r /etc/os-release ]; then
    lsb_dist="$(. /etc/os-release && echo "$ID")"
  fi

  lsb_dist="$(echo "$lsb_dist" | tr '[:upper:]' '[:lower:]')"

  case "$lsb_dist" in
    centos*)
        ver=${lsb_dist#centos-}
        if num_cmp $ver '>=' '6.5' && num_cmp $ver '<' '7.0'; then
          echo "centos6"
	  	  return 0
        elif num_cmp $ver '>=' '7.0' ;then
          echo "centos7"
	  	  return 0
        else
          echo "centos"
	  	  return 0
        fi
        ;;
    ubuntu)
        echo "ubuntu"
        ;;
    *)
        echo ""
		return 0
  esac
}

function mount_flat_cgroup() {
  cgroup_parent_path=$(dirname $1)

  mkdir -p $cgroup_parent_path

  if ! mountpoint -q $cgroup_parent_path; then
    mount -t tmpfs none $cgroup_parent_path
  fi

  mkdir -p $1
  mount -t cgroup cgroup $1

  # bind-mount cgroup subsystems to make file tree consistent
  for subsystem in $(tail -n +2 /proc/cgroups | awk '{print $1}'); do
    # in centos 7,cpu and cpuacct is combined
    if [ ${2} == "centos7" -a $subsystem == "cpu" -o ${2} == "centos7" -a $subsystem == "cpuacct" ]; then
        combine_subsystem="cpu,cpuacct"
        if [ ! -d ${1}/$combine_subsystem ]; then
          mkdir -p ${1}/$combine_subsystem
        fi
        if ! mountpoint -q ${1}/$combine_subsystem; then
          mount --bind $1 ${1}/$combine_subsystem
        fi
        ln -s ${1}/$combine_subsystem ${1}/$subsystem
    else

      mkdir -p ${1}/$subsystem

      if ! mountpoint -q ${1}/$subsystem; then
        mount --bind $1 ${1}/$subsystem
      fi
    fi
  done
}

function mount_nested_cgroup() {
  mkdir -p $1

  if ! mountpoint -q $1; then
    mount -t tmpfs -o uid=0,gid=0,mode=0755 cgroup $1
  fi

  for subsystem in $(tail -n +2 /proc/cgroups | awk '{print $1}'); do
    # in centos 7,cpu and cpuacct is combined
    if [ ${2} == "centos7" -a $subsystem == "cpu" -o ${2} == "centos7" -a $subsystem == "cpuacct" ]; then
        combine_subsystem="cpu,cpuacct"
        if [ ! -d ${1}/$combine_subsystem ]; then
	  mkdir -p ${1}/$combine_subsystem
 	fi
	if ! mountpoint -q ${1}/$combine_subsystem; then
	  mount -n -t cgroup -o $combine_subsystem cgroup ${1}/$combine_subsystem
	fi
	ln -s ${1}/$combine_subsystem ${1}/$subsystem
    else

      mkdir -p ${1}/$subsystem

      if ! mountpoint -q ${1}/$subsystem; then
        mount -n -t cgroup -o $subsystem cgroup ${1}/$subsystem
      fi
    fi
  done
}

# get the linux issue,currently only support ubuntu/centos
LINUX_ISSUE=$(get_lsb)

if [ ! -d $cgroup_path ]
then
  mount_nested_cgroup $cgroup_path $LINUX_ISSUE || \
    mount_flat_cgroup $cgroup_path $LINUX_ISSUE
fi

./net.sh setup

./bridge.sh setup

# Disable AppArmor if possible
if [ -x /etc/init.d/apparmor ]; then
  /etc/init.d/apparmor teardown
fi

# quotaon(8) exits with non-zero status when quotas are ENABLED
if [ "$DISK_QUOTA_ENABLED" = "true" ] && quotaon -p $CONTAINER_DEPOT_MOUNT_POINT_PATH > /dev/null 2>&1
then
  mount -o remount,usrjquota=aquota.user,grpjquota=aquota.group,jqfmt=vfsv0 $CONTAINER_DEPOT_MOUNT_POINT_PATH
  quotacheck -ugmb -F vfsv0 $CONTAINER_DEPOT_MOUNT_POINT_PATH
  quotaon $CONTAINER_DEPOT_MOUNT_POINT_PATH
elif [ "$DISK_QUOTA_ENABLED" = "false" ] && ! quotaon -p $CONTAINER_DEPOT_MOUNT_POINT_PATH > /dev/null 2>&1
then
  quotaoff $CONTAINER_DEPOT_MOUNT_POINT_PATH
fi
