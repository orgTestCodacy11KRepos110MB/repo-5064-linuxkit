package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"

	log "github.com/sirupsen/logrus"
)

//Version 12 relates to Fusion 8 and WS 12
//virtualHW.version = "12"

const vmxHW string = `config.version = "8"
virtualHW.version = "12"
vmci0.present = "TRUE"
floppy0.present = "FALSE"
displayName = "%s"
numvcpus = "%d"
memsize = "%d"
scsi0.present = "TRUE"
scsi0.sharedBus = "none"
scsi0.virtualDev = "lsilogic"
`

const vmxDisk string = `
scsi0:0.present = "TRUE"
scsi0:0.fileName = "%s"
scsi0:0.deviceType = "scsi-hardDisk"
`

const vmxDiskPersistent string = `scsi0:1.present = "TRUE"
scsi0:1.fileName = "%s"
scsi0:1.deviceType = "scsi-hardDisk"
`

const vmxCdrom string = `ide1:0.present = "TRUE"
ide1:0.fileName = "%s"
ide1:0.deviceType = "cdrom-image"
`

const vmxPCI string = `pciBridge0.present = "TRUE"
pciBridge4.present = "TRUE"
pciBridge4.virtualDev = "pcieRootPort"
pciBridge4.functions = "8"
pciBridge5.present = "TRUE"
pciBridge5.virtualDev = "pcieRootPort"
pciBridge5.functions = "8"
pciBridge6.present = "TRUE"
pciBridge6.virtualDev = "pcieRootPort"
pciBridge6.functions = "8"
pciBridge7.present = "TRUE"
pciBridge7.virtualDev = "pcieRootPort"
pciBridge7.functions = "8"
ethernet0.pciSlotNumber = "32"
ethernet0.present = "TRUE"
ethernet0.virtualDev = "e1000"
ethernet0.networkName = "Inside"
ethernet0.generatedAddressOffset = "0"
guestOS = "other3xlinux-64"
`

func runVMware(args []string) {
	invoked := filepath.Base(os.Args[0])
	flags := flag.NewFlagSet("vmware", flag.ExitOnError)
	flags.Usage = func() {
		fmt.Printf("USAGE: %s run vmware [options] prefix\n\n", invoked)
		fmt.Printf("'prefix' specifies the path to the VM image.\n")
		fmt.Printf("\n")
		fmt.Printf("Options:\n")
		flags.PrintDefaults()
	}
	cpus := flags.Int("cpus", 1, "Number of CPUs")
	mem := flags.Int("mem", 1024, "Amount of memory in MB")
	var disks Disks
	flags.Var(&disks, "disk", "Disk config. [file=]path[,size=1G]")
	state := flags.String("state", "", "Path to directory to keep VM state in")

	if err := flags.Parse(args); err != nil {
		log.Fatal("Unable to parse args")
	}
	remArgs := flags.Args()

	if len(remArgs) == 0 {
		fmt.Println("Please specify the prefix to the image to boot")
		flags.Usage()
		os.Exit(1)
	}
	prefix := remArgs[0]

	if *state == "" {
		*state = prefix + "-state"
	}
	if err := os.MkdirAll(*state, 0755); err != nil {
		log.Fatalf("Could not create state directory: %v", err)
	}

	var vmrunPath, vmDiskManagerPath string
	var vmrunArgs []string

	switch runtime.GOOS {
	case "windows":
		vmrunPath = "C:\\Program\\ files\\VMware Workstation\\vmrun.exe"
		vmDiskManagerPath = "C:\\Program\\ files\\VMware Workstation\\vmware-vdiskmanager.exe"
		vmrunArgs = []string{"-T", "ws", "start"}
	case "darwin":
		vmrunPath = "/Applications/VMware Fusion.app/Contents/Library/vmrun"
		vmDiskManagerPath = "/Applications/VMware Fusion.app/Contents/Library/vmware-vdiskmanager"
		vmrunArgs = []string{"-T", "fusion", "start"}
	default:
		vmrunPath = "vmrun"
		vmDiskManagerPath = "vmware-vdiskmanager"
		fullVMrunPath, err := exec.LookPath(vmrunPath)
		if err != nil {
			// Kept as separate error as people may manually change their environment vars
			log.Fatalf("Unable to find %s within the $PATH", vmrunPath)
		}
		vmrunPath = fullVMrunPath
		vmrunArgs = []string{"-T", "ws", "start"}
	}

	// Check vmrunPath exists before attempting to execute
	if _, err := os.Stat(vmrunPath); os.IsNotExist(err) {
		log.Fatalf("ERROR VMware executables can not be found, ensure software is installed")
	}

	for i, d := range disks {
		id := ""
		if i != 0 {
			id = strconv.Itoa(i)
		}
		if d.Size != 0 && d.Path == "" {
			d.Path = filepath.Join(*state, "disk"+id+".vmdk")
		}
		if d.Format != "" && d.Format != "vmdk" {
			log.Fatalf("only vmdk supported for VMware driver")
		}
		if d.Path == "" {
			log.Fatalf("disk specified with no size or name")
		}
		disks[i] = d
	}

	for _, d := range disks {
		// Check vmDiskManagerPath exist before attempting to execute
		if _, err := os.Stat(vmDiskManagerPath); os.IsNotExist(err) {
			log.Fatalf("ERROR VMware Disk Manager executables can not be found, ensure software is installed")
		}

		// If disk doesn't exist then create one, error if disk is unreadable
		if _, err := os.Stat(d.Path); err != nil {
			if os.IsPermission(err) {
				log.Fatalf("Unable to read file [%s], please check permissions", d.Path)
			} else if os.IsNotExist(err) {
				log.Infof("Creating new VMware disk [%s]", d.Path)
				vmDiskCmd := exec.Command(vmDiskManagerPath, "-c", "-s", fmt.Sprintf("%dMB", d.Size), "-a", "lsilogic", "-t", "0", d.Path)
				if err = vmDiskCmd.Run(); err != nil {
					log.Fatalf("Error creating disk [%s]:  %v", d.Path, err)
				}
			} else {
				log.Fatalf("Unable to read file [%s]: %v", d.Path, err)
			}
		} else {
			log.Infof("Using existing disk [%s]", d.Path)
		}
	}

	if len(disks) > 1 {
		log.Fatalf("VMware driver currently only supports a single disk")
	}

	disk := ""
	if len(disks) == 1 {
		disk = disks[0].Path
	}

	// Build the contents of the VMWare .vmx file
	vmx := buildVMX(*cpus, *mem, disk, prefix)
	if vmx == "" {
		log.Fatalf("VMware .vmx file could not be generated, please confirm inputs")
	}

	// Create the .vmx file
	vmxPath := filepath.Join(*state, "linuxkit.vmx")
	err := os.WriteFile(vmxPath, []byte(vmx), 0644)
	if err != nil {
		log.Fatalf("Error writing .vmx file: %v", err)
	}
	vmrunArgs = append(vmrunArgs, vmxPath)

	cmd := exec.Command(vmrunPath, vmrunArgs...)
	out, err := cmd.Output()
	if err != nil {
		log.Fatalf("Error starting vmrun: %v", err)
	}

	// check there is output to push to logging
	if len(out) > 0 {
		log.Info(out)
	}
}

func buildVMX(cpus int, mem int, persistentDisk string, prefix string) string {
	// CD-ROM can be added for use in a further release
	cdromPath := ""

	var returnString string

	returnString += fmt.Sprintf(vmxHW, prefix, cpus, mem)

	if cdromPath != "" {
		returnString += fmt.Sprintf(vmxCdrom, cdromPath)
	}

	vmdkPath, err := filepath.Abs(prefix + ".vmdk")
	if err != nil {
		log.Fatalf("Unable get absolute path for boot vmdk: %v", err)
	}
	if _, err := os.Stat(vmdkPath); err != nil {
		if os.IsPermission(err) {
			log.Fatalf("Unable to read file [%s], please check permissions", vmdkPath)
		}
		if os.IsNotExist(err) {
			log.Fatalf("File [%s] does not exist in current directory", vmdkPath)
		}
	} else {
		returnString += fmt.Sprintf(vmxDisk, vmdkPath)
	}
	// Add persistentDisk to the vmx if it has been specified in the args.
	if persistentDisk != "" {
		persistentDisk, err = filepath.Abs(persistentDisk)
		if err != nil {
			log.Fatalf("Unable get absolute path for persistent disk: %v", err)
		}
		returnString += fmt.Sprintf(vmxDiskPersistent, persistentDisk)
	}

	returnString += vmxPCI
	return returnString
}
