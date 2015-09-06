#!/bin/bash

[ -n "$DEBUG" ] && set -o xtrace
set -o nounset
set -o errexit
shopt -s nullglob

## Description  : create specify veth pairs and configue ip for the container
##				  script must run at parent process not child(container) process
## Autor        : lvguanglin
## Modified Time: 2015/06/23

cd $(dirname ${0})

source ./etc/config

function check_variables() {
	ContainerPID=${container_pid:-}
	if [ -z "$ContainerPID" ]; then echo "container pid is null" >&2 ; exit 1 ; fi
	ContainerVethIp=${container_veth_ip}
	ContainerVethCidrSuffix=${container_veth_ip_cidr_suffix:-24}
	ContainerInRouteCidr=${container_in_route_cidr:-10.0.0.0/8}
	ContainerHostBr=${container_host_brname}
	ContainerVethName=${container_veth_iface:-eth1}
	
	ContainerHostBrGw=$(ip route|grep "$ContainerHostBr"|awk '/default via .* dev /{print $3}')
	
	VethHostIf="h_${ContainerPID}"
	max_id_len=$(expr 15 - ${#VethHostIf} - 1)
	id_tail=$(tail -c $max_id_len <<< $id)
	VethHostIf="${VethHostIf}_${id_tail}"
	VethContIf="c_${ContainerPID}"
	max_id_len=$(expr 15 - ${#VethContIf} - 1)
	id_tail=$(tail -c $max_id_len <<< $id)
	VethContIf="${VethContIf}_${id_tail}"

	ip_used=$(ping -c1 $ContainerVethIp -w1|grep -o "[[:digit:]] received" |awk '{print $1}')
	if [ $ip_used != 0 ];then
		echo "'$ContainerVethIp' is used,Please specify a unused container_ip..." 1>&2
		exit 1
	fi
}

function init_veth_pairs() {
	[ ! -d "/var/run/netns" ] && {
		mkdir -p /var/run/netns
	}

	ln -s /proc/$ContainerPID/ns/net /var/run/netns/$ContainerPID
	
	ip link add name $VethHostIf type veth peer name $VethContIf
	(ip link set $VethHostIf master $ContainerHostBr > /dev/null 2>&1) || (brctl addif $ContainerHostBr $VethHostIf)
	ip link set $VethHostIf up
	
	ip link set $VethContIf netns $ContainerPID
	ip netns exec $ContainerPID ip link set $VethContIf name $ContainerVethName
	ip netns exec $ContainerPID ip addr add $ContainerVethIp/$ContainerVethCidrSuffix brd + dev $ContainerVethName
	ip netns exec $ContainerPID ip link set $ContainerVethName up
	[ $ContainerHostBrGw ] && {
		#actualy it will works without this default route,because it's connected with bridge
		#ip netns exec $ContainerPID ip route add default via $ContainerHostBrGw > /dev/null 2>&1
		ip netns exec $ContainerPID ip route add $ContainerInRouteCidr via $ContainerHostBrGw dev $ContainerVethName
		ip netns exec $ContainerPID ping -c1 -w1 $ContainerHostBrGw > /dev/null 2>&1
	} || true #just in case
	
	rm -rf /var/run/netns/$ContainerPID || true #just in case
	
}

function setup_veth() {
	[ ${container_host_brname:-} ] && [ ${container_veth_ip:-} ] && {
		check_variables
		init_veth_pairs
	} || true #just in case
}

case "${1}" in
  setup)
    setup_veth

    ;;
  *)
    echo "Unknown command: ${1}" 1>&2
    exit 1
    ;;
esac


