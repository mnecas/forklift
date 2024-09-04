package main

const (
	DYNAMIC_SCRIPTS_MOUNT_PATH = "/mnt/dynamic_scripts"
	SHELL_SUFFIX               = ".sh"
	LINUX_DYNAMIC_REGEX        = `(linux_(run|firstboot)_((.*).sh))$`
	WIN_FIRSTBOOT_PATH         = "/Program Files/Guestfs/Firstboot"
	WIN_FIRSTBOOT_SCRIPTS_PATH = "/Program Files/Guestfs/Firstboot/scripts"
	WINDOWS_DYNAMIC_REGEX      = `(win_firstboot_((.*).ps1))$`

	OVA     = "ova"
	vSphere = "vSphere"
	DIR     = "/var/tmp/v2v"
	FS      = "/mnt/disks/disk[0-9]*"
	Block   = "/dev/block[0-9]*"
	VDDK    = "/opt/vmware-vix-disklib-distrib"
	LUKSDIR = "/etc/luks"

	LETTERS        = "abcdefghijklmnopqrstuvwxyz"
	LETTERS_LENGTH = len(LETTERS)
)
