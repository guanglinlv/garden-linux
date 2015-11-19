#!/bin/bash

[ -n "$DEBUG" ] && set -o xtrace
set -o nounset
set -o errexit
shopt -s nullglob

## Description  : create specify bridge for specify network interface
## Autor        : lvguanglin
## Modified Time: 2015/06/23
IFNAME=${GARDEN_HOST_IFNAME:-}
BRNAME=${GARDEN_HOST_BRNAME:-}

function command_exists() {
        for sub in "$@";do
                if ! command -v "$sub" >/dev/null 2>&1;then echo "$sub command is not exist";return 1;fi
        done
        return 0
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

function get_ostype() {
  # perform some very rudimentary platform detection
  lsb_dist=''
  if command -v lsb_release >/dev/null 2>&1; then
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
        echo "unknown"
  return 0
  esac
}

function update_centos_bridge_ifcfg() {
    cat > /etc/sysconfig/network-scripts/ifcfg-$BRNAME << EOF
DEVICE=$BRNAME
BOOTPROTO=dhcp
ONBOOT=yes
TYPE=Bridge
EOF
    cat > /etc/sysconfig/network-scripts/ifcfg-$IFNAME << EOF
DEVICE=$IFNAME
#BOOTPROTO=dhcp
ONBOOT=yes
PEERDNS=yes
BRIDGE=$BRNAME
EOF

    service NetworkManager stop 1>/dev/null 2>&1 || true
    service network restart 1>/dev/null 2>&1 || true
    service NetworkManager start 1>/dev/null 2>&1 || true
}

function update_ubuntu_bridge_ifcfg() {
    if [ ! -f /etc/network/interfaces.d/${IFNAME}.cfg ] ; then
        echo "/etc/network/interfaces.d/${IFNAME}.cfg is not exist." >&2
        exit 1
    fi
    cat > /etc/network/interfaces.d/${IFNAME}.cfg <<EOF
# The primary network interface
auto ${IFNAME}
#iface ${IFNAME} inet dhcp
EOF
    cat > /etc/network/interfaces.d/${BRNAME}.cfg <<EOF
# The bridge network interface by hipaas minion
auto ${BRNAME}
iface ${BRNAME} inet dhcp
bridge_ports ${IFNAME}
bridge_stp off
EOF
    ifdown -a --no-loopback && ifup -a --no-loopback 1>/dev/null 2>&1
}

function check_specify_if() {
	if [ -d "/sys/class/net/${IFNAME}" -a ! -d "/sys/class/net/${IFNAME}/bridge" ]; then
        # check $IFNAME is added into other bridge or not
        local sys_net=/sys/class/net
        for net in $(\ls ${sys_net});do
            if [ -d ${sys_net}/${net}/bridge ]; then
                for brif in $(\ls ${sys_net}/${net}/brif);do
                    if [ ${IFNAME} == ${brif} ]; then
                        echo "${IFNAME} has been added into bridge ${net}" >&2
                        exit 1
                    fi
                done
            fi
        done
		IFMAC=$(cat /sys/class/net/${IFNAME}/address)
		IFMTU=$(cat /sys/class/net/${IFNAME}/mtu)
		IFADDRV4=$(ip addr show ${IFNAME}|awk '/inet /{print $2}')
		IFIPV4=${IFADDRV4%%/*}
		IFADDRV6=$(ip addr show ${IFNAME}|awk '/inet6 /{print $2}')
		IFIPV6=${IFADDRV6%%/*}
		#IFCIDRV4=${IFADDRV4#*/}
		#IFCIDRV4=${IFCIDRV4%%/*}
		IFROUTESPEC=$(ip route|grep "$IFNAME"|awk '/default via .* dev /')
		IFROUTESPEC=${IFROUTESPEC#*${IFNAME} }
		IFROUTEGW=$(ip route|grep "$IFNAME"|awk '/default via .* dev /{print $3}')
    else
        echo "$IFNAME is not exist or is not a device" >&2; exit 1
	fi
}

function init_bridge_with_specify_if() {
	if [ ! -d "/sys/class/net/${BRNAME}" ]; then
		check_specify_if
		if [ -z $IFADDRV4 ]; then
			echo "${IFNAME} - IFADDRV4: ${IFADDRV4}" >&2
			exit 1
		fi
		(ip link add dev $BRNAME type bridge > /dev/null 2>&1) || (brctl addbr $BRNAME)
		ip addr add $IFADDRV4 br + dev $BRNAME
		ip addr del $IFADDRV4 dev $IFNAME
		
		[ "$IFADDRV6" ] && {
			ip addr add $IFADDRV6 dev $BRNAME
			ip addr del $IFADDRV6 dev $IFNAME
		} || true #just in case
		(ip link set $IFNAME master $BRNAME > /dev/null 2>&1) || (brctl addif $BRNAME $IFNAME)
		[ "$IFMTU" ] && {
			ip link set $BRNAME mtu $IFMTU
		} || true #just in case
		[ "$IFMAC" ] && {
			ip link set "$BRNAME" address "$IFMAC"
		} || true #just in case
		ip link set $IFNAME up
		ip link set $BRNAME up
		[ "$IFROUTEGW" ] && {
			#echo "IFROUTEGW - '$IFROUTEGW',IFROUTESPEC - '$IFROUTESPEC'" 
			ip route add default via $IFROUTEGW dev $BRNAME $IFROUTESPEC
		} || true #just in case
		#echo "${BRNAME} configured with ${IFNAME}"
        # update ifcfg for different os
        local os_type=$(get_ostype)
        case "${os_type}" in
            centos*) update_centos_bridge_ifcfg;;
            ubuntu) update_ubuntu_bridge_ifcfg;;
            *) echo "${os_type} is not supported now." >&2;exit 1;;
        esac
    else
        echo "$BRNAME is also exist"; exit 0
	fi
}

function setup_bridge() {
	[ $IFNAME ] && [ $BRNAME ] && {
		init_bridge_with_specify_if
	} || true #just in case
}

case "${1}" in
  setup)
    if ! command_exists brctl; then exit 1; fi
    setup_bridge

    ;;
  *)
    echo "Unknown command: ${1}" 1>&2
    exit 1
    ;;
esac
